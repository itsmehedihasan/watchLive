// Package proxy implements the HLS stream proxy: it fetches upstream
// playlists and segments with spoofed browser headers (many stream servers
// block unknown clients), rewrites playlist URLs to route back through the
// proxy, and serves segments from an in-memory LRU cache so concurrent
// viewers of the same channel hit upstream once per segment.
package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	playlistTTL    = 2 * time.Second  // live playlists refresh every target-duration; a micro-cache absorbs herd refreshes
	segmentTTL     = 5 * time.Minute  // live segments are write-once; 5 min covers any replay window
	maxPlaylistLen = 10 << 20         // 10 MB — sanity cap for playlist bodies
	upstreamLimit  = 30 * time.Second // total budget for one upstream fetch

	// prefetchConcurrency caps simultaneous background segment warm-ups so the
	// proxy never opens an unbounded number of upstream connections. A live
	// media playlist lists only a handful of segments, so this comfortably
	// covers several channels playing at once.
	prefetchConcurrency = 12
)

// browserHeaders spoof a real browser so stream servers don't block us.
var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Accept":          "*/*",
	"Accept-Language": "en-US,en;q=0.9",
}

var (
	m3u8URLRe = regexp.MustCompile(`(?i)\.m3u8(\?|$)`)
	mpdURLRe  = regexp.MustCompile(`(?i)\.mpd(\?|$)`)
	tsURLRe   = regexp.MustCompile(`(?i)\.ts(\?|$)`)
	uriAttrRe = regexp.MustCompile(`URI="([^"]+)"`)
)

// fetchResult is the outcome of one upstream fetch, shared between
// single-flight waiters.
type fetchResult struct {
	data        []byte
	contentType string
	status      int
	isPlaylist  bool
	err         error
}

type inflightCall struct {
	wg  sync.WaitGroup
	res fetchResult
}

// Handler serves GET /api/proxy?url=<upstream>.
type Handler struct {
	client *http.Client
	// streamClient has no overall timeout: it serves continuous live streams
	// (raw MPEG-TS channels) that never end. The request context cancels it
	// when the browser disconnects.
	streamClient *http.Client
	segments     *lruCache
	// playlists caches *rewritten* playlist bodies; rewriting is
	// deterministic for a given URL so caching post-rewrite is safe.
	playlists *lruCache

	mu       sync.Mutex
	inflight map[string]*inflightCall

	// Prefetch: warm upcoming segments into the cache off the playback path.
	// sem bounds upstream fanout; pf dedups in-flight warm-ups (the cache
	// dedups completed ones).
	sem  chan struct{}
	pfMu sync.Mutex
	pf   map[string]struct{}

	// allowPrivate disables the SSRF guard (set once at startup, before serving)
	// so a trusted operator can proxy LAN/loopback stream boxes if they opt in.
	allowPrivate bool

	// upstreamHeaders overrides per-host request headers (User-Agent / Referer)
	// from the catalog's #EXTVLCOPT hints, so a CDN that gates on a specific UA
	// or referer gets exactly what its channel prescribes. Keyed by lowercased
	// host (segments share the channel's host), swapped wholesale on each catalog
	// change. Read on every upstream fetch.
	hdrMu           sync.RWMutex
	upstreamHeaders map[string]UpstreamHeader
}

// SetAllowPrivateUpstreams toggles whether the proxy may fetch loopback/private/
// link-local addresses. Call once at startup before serving — it is not
// goroutine-safe with in-flight requests.
func (h *Handler) SetAllowPrivateUpstreams(v bool) { h.allowPrivate = v }

// UpstreamHeader holds per-channel HTTP header overrides for upstream fetches.
// An empty field falls back to the proxy's default for that header.
type UpstreamHeader struct {
	UserAgent string
	Referer   string
}

// SetUpstreamHeaders replaces the host→header override map (keys must be
// lowercased hosts). Safe to call while serving; the map is swapped under a
// write lock and read under a read lock per fetch.
func (h *Handler) SetUpstreamHeaders(m map[string]UpstreamHeader) {
	h.hdrMu.Lock()
	h.upstreamHeaders = m
	h.hdrMu.Unlock()
}

// headerOverride returns the override for a host, if any.
func (h *Handler) headerOverride(host string) (UpstreamHeader, bool) {
	h.hdrMu.RLock()
	defer h.hdrMu.RUnlock()
	ov, ok := h.upstreamHeaders[strings.ToLower(host)]
	return ov, ok
}

// New creates a proxy handler with a segment cache of cacheBytes capacity.
func New(cacheBytes int64) *Handler {
	perItem := cacheBytes / 8
	if perItem > 32<<20 {
		perItem = 32 << 20
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Handler{
		client: &http.Client{
			Timeout:   upstreamLimit,
			Transport: transport,
		},
		streamClient: &http.Client{Transport: transport},
		segments:     newLRUCache(cacheBytes, perItem),
		playlists:    newLRUCache(16<<20, maxPlaylistLen),
		inflight:     make(map[string]*inflightCall),
		sem:          make(chan struct{}, prefetchConcurrency),
		pf:           make(map[string]struct{}),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORS(w.Header())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	target := r.URL.Query().Get("url")
	if target == "" || !isHTTPURL(target) {
		http.Error(w, "Missing or invalid url parameter", http.StatusBadRequest)
		return
	}
	// SSRF guard: never let the url= param reach loopback/private/link-local
	// hosts (e.g. cloud metadata at 169.254.169.254). Opt out with the flag for
	// trusted LAN sources. Don't echo the URL back in the error.
	if !h.allowPrivate && !isSafeUpstream(target) {
		http.Error(w, "Forbidden upstream address", http.StatusForbidden)
		return
	}

	// Fast path: serve from cache.
	if body, ct, ok := h.playlists.get(target); ok {
		writePlaylistHeaders(w.Header(), ct)
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return
	}
	if body, ct, ok := h.segments.get(target); ok {
		writeSegmentHeaders(w.Header(), ct)
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return
	}

	// Raw MPEG-TS URLs may be continuous live streams that never end; the
	// buffered/single-flight path would hang on io.ReadAll. serveTS streams
	// these straight through, and still buffers+caches finite .ts segments
	// (the common HLS case) so repeat fetches stay cheap.
	if tsURLRe.MatchString(target) {
		h.serveTS(w, r, target)
		return
	}

	res := h.fetchShared(target)
	if res.err != nil {
		http.Error(w, "Proxy fetch failed", http.StatusBadGateway)
		return
	}
	if res.status < 200 || res.status > 299 {
		http.Error(w, fmt.Sprintf("Upstream returned %d", res.status), res.status)
		return
	}

	if res.isPlaylist {
		writePlaylistHeaders(w.Header(), res.contentType)
	} else {
		writeSegmentHeaders(w.Header(), res.contentType)
	}
	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(http.StatusOK)
	w.Write(res.data)
}

// fetchShared collapses concurrent requests for the same URL into a single
// upstream fetch; every caller gets the same result.
func (h *Handler) fetchShared(target string) fetchResult {
	h.mu.Lock()
	if call, ok := h.inflight[target]; ok {
		h.mu.Unlock()
		call.wg.Wait()
		return call.res
	}
	call := &inflightCall{}
	call.wg.Add(1)
	h.inflight[target] = call
	h.mu.Unlock()

	call.res = h.fetchUpstream(target)

	h.mu.Lock()
	delete(h.inflight, target)
	h.mu.Unlock()
	call.wg.Done()
	return call.res
}

// schedulePrefetch warms upcoming segments into the cache in the background.
// It is best-effort: already-cached or already-warming URLs are skipped, and
// when the worker pool is saturated it stops rather than queueing unbounded
// work — the player's own request will still fill any segment we miss. Segments
// are walked in playback order so the soonest-needed ones are warmed first.
func (h *Handler) schedulePrefetch(urls []string) {
	for _, target := range urls {
		if _, _, ok := h.segments.get(target); ok {
			continue // already warm
		}
		select {
		case h.sem <- struct{}{}:
		default:
			return // pool saturated — leave the rest to on-demand fetches
		}
		if !h.beginPrefetch(target) {
			<-h.sem // another goroutine is already warming this one
			continue
		}
		go func(target string) {
			defer func() { h.endPrefetch(target); <-h.sem }()
			h.warmSegment(target)
		}(target)
	}
}

// warmSegment fetches a finite segment into the segment cache using the same
// browser-header spoofing as the live path. It deliberately skips anything that
// isn't a bounded segment — a continuous live MPEG-TS channel (unknown or
// oversized length) must never be buffered whole into memory.
func (h *Handler) warmSegment(target string) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return
	}
	h.applyBrowserHeaders(req, target)

	resp, err := h.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.CopyN(io.Discard, resp.Body, 4096) // drain a little for connection reuse
		return
	}
	if resp.ContentLength > h.segments.maxItem {
		return // declared oversized — don't even read it
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.segments.maxItem+1))
	if err != nil {
		return
	}
	body, ct := decodeImageWrappedTS(body, resp.Header.Get("Content-Type"))
	if int64(len(body)) <= h.segments.maxItem {
		h.segments.set(target, body, ct, segmentTTL)
	}
}

// beginPrefetch claims target for warming, returning false if it is already
// being warmed. endPrefetch releases the claim.
func (h *Handler) beginPrefetch(target string) bool {
	h.pfMu.Lock()
	defer h.pfMu.Unlock()
	if _, ok := h.pf[target]; ok {
		return false
	}
	h.pf[target] = struct{}{}
	return true
}

func (h *Handler) endPrefetch(target string) {
	h.pfMu.Lock()
	delete(h.pf, target)
	h.pfMu.Unlock()
}

// applyBrowserHeaders spoofs a real browser and sets a Referer/Origin so stream
// servers don't block the request. By default it uses a same-origin Referer; if
// the target's host has a catalog override (a #EXTVLCOPT http-user-agent /
// http-referrer hint), that channel's exact UA and/or referer win — and when a
// referer is overridden, Origin is realigned to that referer's origin (or
// dropped) so the two headers never contradict each other.
func (h *Handler) applyBrowserHeaders(req *http.Request, target string) {
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
	u, err := url.Parse(target)
	if err != nil {
		return
	}
	origin := u.Scheme + "://" + u.Host
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Origin", origin)

	ov, ok := h.headerOverride(u.Host)
	if !ok {
		return
	}
	if ov.UserAgent != "" {
		req.Header.Set("User-Agent", ov.UserAgent)
	}
	if ov.Referer != "" {
		req.Header.Set("Referer", ov.Referer)
		if ru, rerr := url.Parse(ov.Referer); rerr == nil && ru.Scheme != "" && ru.Host != "" {
			req.Header.Set("Origin", ru.Scheme+"://"+ru.Host)
		} else {
			req.Header.Del("Origin")
		}
	}
}

// fetchUpstream performs the actual upstream request and populates the caches.
func (h *Handler) fetchUpstream(target string) fetchResult {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return fetchResult{err: err}
	}
	h.applyBrowserHeaders(req, target)

	resp, err := h.client.Do(req)
	if err != nil {
		return fetchResult{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Drain a little so the connection can be reused, then report status.
		io.CopyN(io.Discard, resp.Body, 4096)
		return fetchResult{status: resp.StatusCode}
	}

	contentType := resp.Header.Get("Content-Type")
	isHLS := strings.Contains(contentType, "mpegurl") ||
		strings.Contains(contentType, "m3u8") ||
		m3u8URLRe.MatchString(target)
	isDASH := strings.Contains(contentType, "dash+xml") || mpdURLRe.MatchString(target)

	if isHLS {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistLen))
		if err != nil {
			return fetchResult{err: err}
		}
		rewritten := []byte(RewritePlaylist(string(body), target))
		h.playlists.set(target, rewritten, "application/vnd.apple.mpegurl", playlistTTL)
		// Warm the listed segments into the cache so the player's own requests
		// for them hit the fast path instead of paying an upstream round trip.
		// Throttled to once per playlist refresh by the 2s playlist micro-cache.
		h.schedulePrefetch(mediaSegments(string(body), target))
		return fetchResult{data: rewritten, contentType: "application/vnd.apple.mpegurl", status: resp.StatusCode, isPlaylist: true}
	}

	if isDASH {
		// The Shaka client routes every segment request back through this proxy
		// via a request filter, so segment URLs stay absolute. But Shaka resolves
		// the manifest's RELATIVE URLs against the URL it fetched the manifest
		// from — which is our /api/proxy?url=… endpoint, not the real origin — so
		// without help a relative "seg.dash" resolves to /api/seg.dash and 404s.
		// Inject an absolute <BaseURL> (the manifest's own directory) so relative
		// resolution targets the real origin; the request filter then proxies the
		// resulting absolute URLs. Short TTL keeps the live manifest fresh.
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistLen))
		if err != nil {
			return fetchResult{err: err}
		}
		body = []byte(injectDASHBaseURL(string(body), target))
		ct := contentType
		if ct == "" {
			ct = "application/dash+xml"
		}
		h.playlists.set(target, body, ct, playlistTTL)
		return fetchResult{data: body, contentType: ct, status: resp.StatusCode, isPlaylist: true}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, h.segments.maxItem+1))
	if err != nil {
		return fetchResult{err: err}
	}
	// Unwrap MPEG-TS hidden inside a fake PNG (image-CDN smuggling); a no-op for
	// ordinary segments. Done before caching so the cache holds the bare TS.
	body, contentType = decodeImageWrappedTS(body, contentType)
	if int64(len(body)) <= h.segments.maxItem {
		h.segments.set(target, body, contentType, segmentTTL)
	}
	return fetchResult{data: body, contentType: contentType, status: resp.StatusCode}
}

// serveTS handles .ts URLs. A finite body (Content-Length set — the typical HLS
// segment) is buffered and cached like any other segment. A body of unknown
// length is a continuous live MPEG-TS channel: it is streamed straight to the
// client with periodic flushing, bypassing the cache and single-flight (those
// assume a finite, shareable body — a live stream is neither).
func (h *Handler) serveTS(w http.ResponseWriter, r *http.Request, target string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "Proxy fetch failed", http.StatusBadGateway)
		return
	}
	h.applyBrowserHeaders(req, target)

	resp, err := h.streamClient.Do(req)
	if err != nil {
		http.Error(w, "Proxy fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.CopyN(io.Discard, resp.Body, 4096)
		http.Error(w, fmt.Sprintf("Upstream returned %d", resp.StatusCode), resp.StatusCode)
		return
	}

	contentType := resp.Header.Get("Content-Type")

	// Finite segment: buffer, cache, serve — same behavior as fetchUpstream.
	if resp.ContentLength >= 0 && resp.ContentLength <= h.segments.maxItem {
		body, err := io.ReadAll(io.LimitReader(resp.Body, h.segments.maxItem+1))
		if err != nil {
			http.Error(w, "Proxy fetch failed", http.StatusBadGateway)
			return
		}
		if int64(len(body)) <= h.segments.maxItem {
			h.segments.set(target, body, contentType, segmentTTL)
		}
		writeSegmentHeaders(w.Header(), contentType)
		w.Header().Set("X-Cache", "MISS")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return
	}

	// Continuous live stream: stream through, flushing as bytes arrive.
	writeSegmentHeaders(w.Header(), contentType)
	w.Header().Set("X-Cache", "STREAM")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// RewritePlaylist rewrites every URL inside an M3U8 playlist (segment lines,
// variant lines and URI="…" attributes in tags such as EXT-X-KEY / EXT-X-MAP)
// to route back through this proxy. Rewritten URLs are root-relative
// ("/api/proxy?url=…"); HLS clients resolve them against the playlist URL,
// which is itself served from this origin.
func RewritePlaylist(text, targetURL string) string {
	resolve := urlResolver(targetURL)
	proxied := func(href string) string {
		return "/api/proxy?url=" + url.QueryEscape(resolve(href))
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Rewrite URI="…" inside EXT-X-KEY, EXT-X-MAP, etc.
			lines[i] = uriAttrRe.ReplaceAllStringFunc(line, func(match string) string {
				uri := uriAttrRe.FindStringSubmatch(match)[1]
				return `URI="` + proxied(uri) + `"`
			})
			continue
		}
		if trimmed == "" {
			continue
		}
		// Plain URL line (variant playlist or segment).
		lines[i] = proxied(trimmed)
	}
	return strings.Join(lines, "\n")
}

// urlResolver returns a closure that resolves a possibly-relative playlist href
// to an absolute upstream URL, given the playlist's own URL. Absolute,
// scheme-relative ("//host/…"), root-relative ("/…") and path-relative forms
// are all handled the same way the original RewritePlaylist did.
func urlResolver(targetURL string) func(string) string {
	base := targetURL
	if idx := strings.LastIndex(targetURL, "/"); idx >= 0 {
		base = targetURL[:idx+1]
	}
	origin := ""
	if u, err := url.Parse(targetURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	return func(href string) string {
		switch {
		case isHTTPURL(href):
			return href
		case strings.HasPrefix(href, "//"):
			return "https:" + href
		case strings.HasPrefix(href, "/"):
			return origin + href
		default:
			return base + href
		}
	}
}

// mediaSegments extracts the resolved (absolute) segment and init-map URLs from
// an HLS MEDIA playlist, in playback order. It returns nil for a MASTER
// playlist (a list of variant renditions) so we don't waste bandwidth warming
// every rendition the player will never pick — the chosen variant's own media
// playlist triggers prefetch when the player loads it.
func mediaSegments(text, targetURL string) []string {
	if strings.Contains(text, "#EXT-X-STREAM-INF") {
		return nil // master playlist
	}
	resolve := urlResolver(targetURL)
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// fMP4 init segment — needed before any media segment.
			if strings.HasPrefix(trimmed, "#EXT-X-MAP") {
				if m := uriAttrRe.FindStringSubmatch(trimmed); m != nil {
					out = append(out, resolve(m[1]))
				}
			}
			continue
		}
		out = append(out, resolve(trimmed))
	}
	return out
}

// mpdOpenRe matches the opening <MPD …> tag (case-insensitive, attributes and
// newlines allowed) so a BaseURL can be inserted as its first child.
var mpdOpenRe = regexp.MustCompile(`(?is)<MPD\b[^>]*>`)

// injectDASHBaseURL inserts an absolute <BaseURL> — the manifest's own
// directory — as the first child of <MPD>, so a Shaka client resolves the
// manifest's relative segment URLs against the real origin rather than the
// /api/proxy?url=… URL it was fetched through (which would 404 every segment).
// Per the DASH spec an absolute MPD-level BaseURL is the base for all relative
// URLs below it, exactly the resolution that would have happened had the
// manifest been loaded directly. Manifests with no <MPD> tag (or already an
// absolute BaseURL) are returned unchanged.
func injectDASHBaseURL(mpd, manifestURL string) string {
	loc := mpdOpenRe.FindStringIndex(mpd)
	if loc == nil {
		return mpd // not a manifest we recognize — leave it alone
	}
	base := manifestURL
	if i := strings.LastIndex(manifestURL, "/"); i >= 0 {
		base = manifestURL[:i+1]
	}
	// & is the only char that must be escaped inside an XML text node here; the
	// rest of a URL is valid character data.
	base = strings.ReplaceAll(base, "&", "&amp;")
	return mpd[:loc[1]] + "<BaseURL>" + base + "</BaseURL>" + mpd[loc[1]:]
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "HTTP://") || strings.HasPrefix(s, "HTTPS://")
}

// isSafeUpstream rejects URLs that point at the host's own network: loopback,
// RFC1918/ULA private ranges, link-local (incl. 169.254.169.254 cloud metadata)
// and the unspecified address. IP literals are checked directly; hostnames are
// allowed best-effort (we don't resolve DNS on the hot path, so DNS-rebinding
// is out of scope — acceptable for a personal app). "localhost" is special-cased.
func isSafeUpstream(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return false
		}
	}
	return true
}

func writeCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "*")
}

func writePlaylistHeaders(h http.Header, contentType string) {
	writeCORS(h)
	if contentType == "" {
		contentType = "application/vnd.apple.mpegurl"
	}
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache, no-store")
}

func writeSegmentHeaders(h http.Header, contentType string) {
	writeCORS(h)
	if contentType == "" {
		contentType = "video/MP2T"
	}
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "public, max-age=30")
}
