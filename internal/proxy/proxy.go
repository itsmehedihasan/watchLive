// Package proxy implements the HLS stream proxy: it fetches upstream
// playlists and segments with spoofed browser headers (many stream servers
// block unknown clients), rewrites playlist URLs to route back through the
// proxy, and serves segments from an in-memory LRU cache so concurrent
// viewers of the same channel hit upstream once per segment.
package proxy

import (
	"fmt"
	"io"
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
)

// browserHeaders spoof a real browser so stream servers don't block us.
var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Accept":          "*/*",
	"Accept-Language": "en-US,en;q=0.9",
}

var m3u8URLRe = regexp.MustCompile(`(?i)\.m3u8(\?|$)`)

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
	client   *http.Client
	segments *lruCache
	// playlists caches *rewritten* playlist bodies; rewriting is
	// deterministic for a given URL so caching post-rewrite is safe.
	playlists *lruCache

	mu       sync.Mutex
	inflight map[string]*inflightCall
}

// New creates a proxy handler with a segment cache of cacheBytes capacity.
func New(cacheBytes int64) *Handler {
	perItem := cacheBytes / 8
	if perItem > 32<<20 {
		perItem = 32 << 20
	}
	return &Handler{
		client: &http.Client{
			Timeout: upstreamLimit,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		segments:  newLRUCache(cacheBytes, perItem),
		playlists: newLRUCache(16<<20, maxPlaylistLen),
		inflight:  make(map[string]*inflightCall),
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

	// Fast path: serve from cache.
	if body, _, ok := h.playlists.get(target); ok {
		writePlaylistHeaders(w.Header())
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
		writePlaylistHeaders(w.Header())
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

// fetchUpstream performs the actual upstream request and populates the caches.
func (h *Handler) fetchUpstream(target string) fetchResult {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return fetchResult{err: err}
	}
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
	if u, err := url.Parse(target); err == nil {
		origin := u.Scheme + "://" + u.Host
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Origin", origin)
	}

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
	isPlaylist := strings.Contains(contentType, "mpegurl") ||
		strings.Contains(contentType, "m3u8") ||
		m3u8URLRe.MatchString(target)

	if isPlaylist {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistLen))
		if err != nil {
			return fetchResult{err: err}
		}
		rewritten := []byte(RewritePlaylist(string(body), target))
		h.playlists.set(target, rewritten, "application/vnd.apple.mpegurl", playlistTTL)
		return fetchResult{data: rewritten, contentType: "application/vnd.apple.mpegurl", status: resp.StatusCode, isPlaylist: true}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, h.segments.maxItem+1))
	if err != nil {
		return fetchResult{err: err}
	}
	if int64(len(body)) <= h.segments.maxItem {
		h.segments.set(target, body, contentType, segmentTTL)
	}
	return fetchResult{data: body, contentType: contentType, status: resp.StatusCode}
}

// RewritePlaylist rewrites every URL inside an M3U8 playlist (segment lines,
// variant lines and URI="…" attributes in tags such as EXT-X-KEY / EXT-X-MAP)
// to route back through this proxy. Rewritten URLs are root-relative
// ("/api/proxy?url=…"); HLS clients resolve them against the playlist URL,
// which is itself served from this origin.
func RewritePlaylist(text, targetURL string) string {
	base := targetURL
	if idx := strings.LastIndex(targetURL, "/"); idx >= 0 {
		base = targetURL[:idx+1]
	}
	origin := ""
	if u, err := url.Parse(targetURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}

	resolve := func(href string) string {
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
	proxied := func(href string) string {
		return "/api/proxy?url=" + url.QueryEscape(resolve(href))
	}

	uriAttrRe := regexp.MustCompile(`URI="([^"]+)"`)

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

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "HTTP://") || strings.HasPrefix(s, "HTTPS://")
}

func writeCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "*")
}

func writePlaylistHeaders(h http.Header) {
	writeCORS(h)
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
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
