// watchlive is a single-binary live TV streaming server: it serves the web
// UI and proxies HLS/DASH streams through an in-memory segment cache. All
// assets are embedded. The channel catalog is a SQLite database
// (store/catalog.db next to the binary) populated from the iptv-org API and
// refreshed by Sync; favourites and manual channels persist there. Pass
// -playlist FILE.m3u to instead run a throwaway session from a custom playlist:
// a separate DB that is reset on every start and seeded only from that file,
// with the API refresh and Sync disabled and your real catalog left untouched.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"watchlive/internal/ffmpeg"
	"watchlive/internal/genre"
	"watchlive/internal/keystore"
	"watchlive/internal/playlist"
	"watchlive/internal/proxy"
	"watchlive/internal/recorder"
	"watchlive/internal/resolver"
	"watchlive/internal/store"
	"watchlive/internal/xtream"
)

//go:embed web/templates/index.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

// errPlaylistMode is returned by refresh() when the server is running a
// throwaway -playlist session: pulling the upstream catalog would clobber the
// custom playlist, so Sync is refused.
var errPlaylistMode = errors.New("sync is disabled in playlist mode")

// channelStore is the read-side cache in front of the SQLite catalog. The JSON
// and gzipped-JSON payloads for /api/channels are precomputed once per change —
// with 10k+ channels the list is megabytes, so compressing per request would
// burn CPU for an identical result. rebuild() refreshes them from the DB after
// any mutation (sync, favourite toggle, manual add/delete).
type channelStore struct {
	st        *store.Store
	sourceURL string
	prune     bool
	// playlistMode disables the API refresh/Sync: the catalog is a throwaway
	// session loaded from a user-supplied -playlist file and must not be
	// overwritten by an upstream fetch.
	playlistMode bool

	mu      sync.RWMutex
	jsonRaw []byte
	jsonGz  []byte
	etag    string
	count   int
	// refreshing reports whether a source refresh (API fetch) is in flight, so
	// the UI can show an "updating" state and re-fetch when it lands.
	refreshing bool

	// refreshMu serializes source refreshes (startup + manual Sync) so two
	// fetches never race.
	refreshMu sync.Mutex

	// afterRebuild, if set, is called with the freshly-listed catalog after every
	// rebuild — used to refresh the proxy's per-host upstream header overrides so
	// they track the catalog (sync, manual add/update, import).
	afterRebuild func([]store.Channel)
}

func newChannelStore(st *store.Store, sourceURL string, prune bool) *channelStore {
	cs := &channelStore{st: st, sourceURL: sourceURL, prune: prune}
	if err := cs.rebuild(); err != nil {
		log.Printf("playlist: initial payload build: %v", err)
	}
	return cs
}

// rebuild reloads the catalog from the DB and recomputes the cached payloads.
// The DB read and marshalling happen outside the lock; only the pointer swap
// holds it, so concurrent /api/channels reads never block on disk I/O.
func (cs *channelStore) rebuild() error {
	chs, err := cs.st.ListChannels()
	if err != nil {
		return err
	}
	if chs == nil {
		chs = []store.Channel{} // marshal to [] not null, so the UI always gets an array
	}
	raw, err := json.Marshal(chs)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gz.Write(raw)
	gz.Close()
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`

	cs.mu.Lock()
	cs.jsonRaw = raw
	cs.jsonGz = buf.Bytes()
	cs.etag = etag
	cs.count = len(chs)
	cs.mu.Unlock()

	// Outside the payload lock: refresh any derived state (proxy header map).
	if cs.afterRebuild != nil {
		cs.afterRebuild(chs)
	}
	return nil
}

// seedIfEmpty populates an empty catalog from the given seed playlist so the
// first run is never blank. A non-empty catalog is left untouched. Only used in
// -playlist mode, where the throwaway DB is seeded from the user's file.
func (cs *channelStore) seedIfEmpty(seed []byte) error {
	empty, err := cs.st.IsEmpty()
	if err != nil || !empty {
		return err
	}
	chs := playlist.Parse(string(seed))
	if len(chs) == 0 {
		return nil
	}
	if _, _, _, err := cs.st.UpsertCatalog(chs); err != nil {
		return err
	}
	return cs.rebuild()
}

// payload returns the precomputed /api/channels response bodies.
func (cs *channelStore) payload() (raw, gz []byte, etag string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.jsonRaw, cs.jsonGz, cs.etag
}

func (cs *channelStore) etagValue() string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.etag
}

func (cs *channelStore) channelCount() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.count
}

func (cs *channelStore) setRefreshing(v bool) {
	cs.mu.Lock()
	cs.refreshing = v
	cs.mu.Unlock()
}

func (cs *channelStore) isRefreshing() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.refreshing
}

// refresh downloads the upstream playlist, resolves each channel's country and
// category from the iptv-org database, and upserts the result into the catalog
// by stable ID — new channels inserted, existing ones updated, user state
// (favourites, manual rows) preserved. Returns the channel count after the
// rebuild.
func (cs *channelStore) refresh() (int, error) {
	if cs.playlistMode {
		return 0, errPlaylistMode
	}
	cs.refreshMu.Lock()
	defer cs.refreshMu.Unlock()
	cs.setRefreshing(true)
	defer cs.setRefreshing(false)

	enriched, n, err := fetchAndEnrich(cs.sourceURL)
	if err != nil {
		return 0, err
	}
	chs := playlist.Parse(string(enriched))
	ins, upd, seen, err := cs.st.UpsertCatalog(chs)
	if err != nil {
		return 0, fmt.Errorf("upsert: %w", err)
	}
	if cs.prune {
		if pruned, perr := cs.st.PruneOrphans(seen); perr != nil {
			log.Printf("source: prune: %v", perr)
		} else if pruned > 0 {
			log.Printf("source: pruned %d orphaned channels", pruned)
		}
	}
	cs.st.SetMeta("last_sync", strconv.FormatInt(time.Now().Unix(), 10))
	if err := cs.rebuild(); err != nil {
		return 0, err
	}
	log.Printf("source: synced %d entries from %s (%d new, %d updated)", n, cs.sourceURL, ins, upd)
	return cs.channelCount(), nil
}

// fetchAndEnrich downloads the source playlist and rewrites it with resolved
// country (group-title) and category (tvg-genre) per channel. Network-only:
// both the playlist and the iptv-org database must be reachable.
func fetchAndEnrich(sourceURL string) ([]byte, int, error) {
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Get(sourceURL)
	if err != nil {
		return nil, 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("read: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data[:min(len(data), 64)])), "#EXTM3U") {
		return nil, 0, fmt.Errorf("response is not an M3U playlist")
	}
	db, err := genre.LoadDB()
	if err != nil {
		return nil, 0, fmt.Errorf("category database: %w", err)
	}
	enriched, n := db.Enrich(data)
	return enriched, n, nil
}

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	dbPath := flag.String("db", "", "path to the SQLite catalog (default: catalog.db next to the binary)")
	playlistPath := flag.String("playlist", "", "run a throwaway session from this .m3u file: a fresh separate DB seeded only from it, API refresh and Sync off, real catalog untouched")
	sourceURL := flag.String("source-url", "https://iptv-org.github.io/iptv/index.m3u", "upstream playlist fetched at startup and by Sync")
	noRefresh := flag.Bool("no-refresh", false, "skip the startup fetch from -source-url and use the catalog as-is")
	prune := flag.Bool("prune", false, "on sync, delete channels no longer upstream (keeps favourited and manual ones)")
	cacheMB := flag.Int64("cache-mb", 200, "segment cache size in MB")
	recDir := flag.String("rec-dir", "recordings", "directory for saved screen recordings")
	open := flag.Bool("open", true, "open the web UI in the default browser once the server is listening")
	allowPrivate := flag.Bool("allow-private-upstreams", false, "allow the proxy to fetch loopback/private/link-local addresses (off by default to block SSRF)")
	flag.Parse()

	// baseDir is where the catalog directory and keys.json live: beside the
	// executable for a distributed single binary, so it keeps its state next to
	// it across restarts. EXCEPTION: under `go run` the binary lives in a temp
	// go-build dir that's deleted on exit, so exe-relative state would be
	// ephemeral (favourites/manual adds/keys would vanish every run). Detect that
	// and fall back to the working directory instead.
	baseDir := "."
	if exe, err := os.Executable(); err == nil && !isEphemeralExe(exe) {
		baseDir = filepath.Dir(exe)
	}

	// Playlist mode: -playlist FILE runs a throwaway session from a custom .m3u.
	// Read and validate the file up front so a missing/empty/non-m3u path fails
	// fast before we touch any database.
	var seedFromPlaylist []byte
	playlistMode := *playlistPath != ""
	if playlistMode {
		data, err := os.ReadFile(*playlistPath)
		if err != nil {
			log.Fatalf("playlist: read %s: %v", *playlistPath, err)
		}
		if len(playlist.Parse(string(data))) == 0 {
			log.Fatalf("playlist: %s contains no channels (is it a valid .m3u?)", *playlistPath)
		}
		seedFromPlaylist = data
	}

	path := *dbPath
	if path == "" {
		// Playlist mode gets its own DB so the real catalog is never involved.
		name := "catalog.db"
		if playlistMode {
			name = "playlist.db"
		}
		path = filepath.Join(baseDir, "store", name)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("store: create %s: %v", dir, err)
		}
	}

	// Reset the playlist-mode DB (and its WAL/SHM sidecars) before opening so each
	// run starts clean — "like a new server" — discarding any state from a prior
	// -playlist session. The real catalog.db is only ever wiped this way if the
	// user explicitly points -playlist at it via -db.
	if playlistMode {
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				log.Fatalf("playlist: reset %s: %v", p, err)
			}
		}
		log.Printf("playlist mode: throwaway session from %s (catalog %s reset, API refresh off)", *playlistPath, path)
	}

	st, err := store.Open(path)
	if err != nil {
		log.Fatalf("store: open %s: %v", path, err)
	}
	defer st.Close()

	// ClearKey keystore: a standalone keys.json beside the catalog dir (NOT inside
	// it), so DRM keys survive a catalog wipe and apply globally to any DASH
	// stream whose manifest KID matches. Seed the keys we've already captured so a
	// fresh file still has them; Merge is idempotent and only writes on a change.
	keysPath := filepath.Join(baseDir, "keys.json")
	ks, err := keystore.Open(keysPath)
	if err != nil {
		log.Fatalf("keystore: open %s: %v", keysPath, err)
	}
	// Seed only KIDs not already present, so a hand-edit or a UI update to one of
	// these keys is never reverted on the next startup.
	existing := ks.All()
	toSeed := map[string]string{}
	for kid, key := range seedClearKeys {
		if _, ok := existing[kid]; !ok {
			toSeed[kid] = key
		}
	}
	if n, err := ks.Merge(toSeed); err != nil {
		log.Printf("keystore: seed: %v", err)
	} else if n > 0 {
		log.Printf("keystore: seeded %d known key(s)", n)
	}
	log.Printf("keystore: %d ClearKey pair(s) in %s", ks.Count(), keysPath)

	channels := newChannelStore(st, *sourceURL, *prune)
	channels.playlistMode = playlistMode
	// In playlist mode the DB was just reset, so seed it synchronously from the
	// user's file before serving. The real catalog is loaded as-is from its DB.
	if playlistMode {
		if err := channels.seedIfEmpty(seedFromPlaylist); err != nil {
			log.Printf("playlist: seed: %v", err)
		}
	}
	log.Printf("playlist: %d channels in catalog %s", channels.channelCount(), path)

	proxyHandler := proxy.New(*cacheMB << 20)
	proxyHandler.SetAllowPrivateUpstreams(*allowPrivate)

	// Per-channel upstream header overrides (UA / referer from #EXTVLCOPT) live in
	// the catalog. Rebuild the proxy's host→header map on every catalog change so a
	// CDN that gates on a specific UA/referer gets exactly what its channel
	// prescribes. The constructor's rebuild ran before this hook was set, so push
	// the initial map explicitly too.
	channels.afterRebuild = func(chs []store.Channel) {
		proxyHandler.SetUpstreamHeaders(upstreamHeadersFromCatalog(chs))
	}
	if chs, err := st.ListChannels(); err == nil {
		proxyHandler.SetUpstreamHeaders(upstreamHeadersFromCatalog(chs))
	}

	// Screen recording: resolve an ffmpeg (embedded copy, else PATH) and wire a
	// recorder that transcodes to 720p H.264/AAC MP4. Absent ffmpeg → recording
	// is simply disabled and the UI hides the button.
	rec := recorder.New(ffmpeg.Resolve(), *recDir)
	if rec.Available() {
		log.Printf("recording: enabled, saving to %s", rec.Dir())
	} else {
		log.Printf("recording: disabled (ffmpeg not found)")
	}

	// Live background sync from -source-url is DISABLED. refresh()/fetchAndEnrich
	// and the -source-url/-no-refresh/-prune flags remain in place for a future
	// sync rework, but nothing fetches the API on its own now.
	_ = *noRefresh // flag retained for the future sync rework; sync is off regardless

	indexTmpl := template.Must(template.ParseFS(templateFS, "web/templates/index.html"))
	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		log.Fatal(err)
	}

	// Resolver: some embed providers serve only short-lived signed URLs that must
	// be fetched fresh at play time (the token is unforgeable). Register provider
	// families here; channels store a recipe (resolver + arg) instead of a URL.
	resolverMgr := resolver.NewManager()
	resolverMgr.Add(resolver.Exposestrat{})

	mux := newMux(proxyHandler, staticSub, channels, st, ks, rec, resolverMgr, indexTmpl)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout reaps dead keep-alive connections. Deliberately NO
		// WriteTimeout: it would abort long-lived live-stream responses.
		IdleTimeout: 120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One-shot startup sweep: auto-refresh any saved Xtream playlist whose
	// update cadence has elapsed. Runs in the background so a slow/dead panel
	// never delays startup; failures are logged and skipped.
	go func() {
		due, err := st.DueXtreamPlaylists()
		if err != nil {
			log.Printf("xtream: startup sweep: %v", err)
			return
		}
		refreshed := false
		for _, p := range due {
			streams, err := xtream.LiveStreams(p.Server, p.Username, p.Password)
			if err != nil {
				log.Printf("xtream: auto-refresh %q: %v", p.Name, err)
				continue
			}
			cats, err := xtream.LiveCategories(p.Server, p.Username, p.Password)
			if err != nil {
				log.Printf("xtream: auto-refresh categories %q: %v", p.Name, err)
				cats = nil
			}
			added, updated, err := importXtreamStreams(st, p, streams, cats)
			if err != nil {
				log.Printf("xtream: auto-refresh import %q: %v", p.Name, err)
				continue
			}
			if err := st.StampXtreamRefreshed(p.ID); err != nil {
				log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
			}
			log.Printf("xtream: auto-refreshed %q: +%d new, %d updated", p.Name, added, updated)
			refreshed = true
		}
		if refreshed {
			channels.rebuild()
		}
	}()

	// Bind explicitly before serving so a failure (e.g. the port is already in
	// use) is reported clearly and we only open the browser once the listener is
	// actually accepting connections — no "can't connect" race.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v (is the port already in use? pass -addr to pick another)", *addr, err)
	}
	url := "http://localhost" + displayAddr(*addr)
	go func() {
		log.Printf("listening on %s", url)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	if *open {
		openBrowser(url)
	}

	<-ctx.Done()
	log.Println("shutting down…")
	rec.StopAll() // finalize any in-progress recordings into playable files
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// isStreamURL reports whether line is an http(s) URL — used to validate a
// manually-added channel's stream link.
func isStreamURL(line string) bool {
	return strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://")
}

// upstreamHeadersFromCatalog builds the proxy's host→header override map from the
// catalog: every server host of a channel that prescribes a UA/referer maps to
// those headers. Keyed by lowercased host, so segment requests on the same host
// inherit the channel's headers. Last writer wins on the rare host shared by two
// channels with different hints.
func upstreamHeadersFromCatalog(chs []store.Channel) map[string]proxy.UpstreamHeader {
	m := map[string]proxy.UpstreamHeader{}
	for _, ch := range chs {
		if ch.UserAgent == "" && ch.Referer == "" {
			continue
		}
		for _, s := range ch.Servers {
			u, err := url.Parse(s.URL)
			if err != nil || u.Host == "" {
				continue
			}
			m[strings.ToLower(u.Host)] = proxy.UpstreamHeader{UserAgent: ch.UserAgent, Referer: ch.Referer}
		}
	}
	return m
}

// importXtreamStreams turns a panel's live-stream list into catalog rows for the
// given saved playlist: it builds each stream's playable URL from the playlist
// credentials, resolves each stream's category_id to its panel category name and
// order (cats, in panel order), then upserts by the stable
// xtream:<playlist>:<stream> id. Returns added/updated counts.
func importXtreamStreams(st *store.Store, p store.XtreamPlaylist, streams []xtream.Stream, cats []xtream.Category) (added, updated int, err error) {
	type catInfo struct {
		name  string
		order int
	}
	byID := make(map[string]catInfo, len(cats))
	for i, c := range cats {
		// 1-based so every real Xtream category has a non-zero order; the
		// frontend treats cat_order == 0 as "no ordering signal" and sorts
		// those groups alphabetically after all ordered Xtream groups.
		byID[c.ID] = catInfo{name: c.Name, order: i + 1}
	}
	rows := make([]store.XtreamStream, 0, len(streams))
	for _, s := range streams {
		ci := byID[s.CategoryID] // zero value (empty name, order 0) when unknown/uncategorized
		rows = append(rows, store.XtreamStream{
			StreamID: s.StreamID,
			Name:     s.Name,
			Logo:     s.Icon,
			URL:      xtream.StreamURL(p.Server, p.Username, p.Password, s.StreamID, p.StreamType),
			Group:    ci.name,
			CatOrder: ci.order,
		})
	}
	return st.UpsertXtreamChannels(p.ID, rows)
}

// seedClearKeys are ClearKey pairs (KID→KEY, lowercase hex) we've already
// captured and want available regardless of catalog wipes. They're merged into
// keys.json on startup; the merge is idempotent, so editing keys.json by hand
// wins on later runs only if a KID here isn't present (a present KID is left as
// stored). New keys normally arrive via the UI, not here.
var seedClearKeys = map[string]string{
	"549ab7cd35a64bb6bb479ecead04d69d": "829799ed534d11fcadeb4b192467e050", // WC Tv -en (ch299)
	"893bc63340876605f52886a42e0ccce5": "d6c46d2d691056fbd091bf1f01b21a91", // captured, channel TBD
}

// isEphemeralExe reports whether the binary is a throwaway build produced by
// `go run` (it lives in a temp go-build dir that's removed on exit). In that
// case an exe-relative DB would be discarded every run, so we prefer a stable
// working-directory path instead.
func isEphemeralExe(exe string) bool {
	dir := strings.ToLower(filepath.Dir(exe))
	if strings.Contains(dir, "go-build") {
		return true
	}
	if tmp := strings.ToLower(os.TempDir()); tmp != "" && strings.HasPrefix(dir, tmp) {
		return true
	}
	return false
}

// serverError logs the real error server-side and returns a generic message,
// so internal details (DB errors, upstream URLs) never leak to clients.
func serverError(w http.ResponseWriter, op string, err error) {
	log.Printf("%s: %v", op, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// writeJSON marshals v and writes it, gzipping when the client accepts it.
// Larger responses (the Xtream import debug payloads especially) run to
// hundreds of KB, so it is worth compressing even on localhost.
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		serverError(w, "api", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write(raw)
		gz.Close()
		return
	}
	w.Write(raw)
}

// openBrowser launches the default browser at url (Windows). Best-effort: a
// failure is logged, not fatal — the URL is also printed to the console.
func openBrowser(url string) {
	// "start" is a cmd builtin; the empty "" is its (ignored) window-title arg,
	// which keeps a url containing spaces or & from being misread as the title.
	if err := exec.Command("cmd", "/c", "start", "", url).Start(); err != nil {
		log.Printf("open browser: %v (open %s manually)", err, url)
	}
}

func displayAddr(addr string) string {
	if addr != "" && addr[0] == ':' {
		return addr
	}
	return " (" + addr + ")"
}

// newMux registers every HTTP route and returns the configured mux. Extracted
// from main so handlers are exercisable in tests against a temp store.
func newMux(proxyHandler *proxy.Handler, staticSub fs.FS, channels *channelStore, st *store.Store, ks *keystore.Store, rec *recorder.Recorder, resolverMgr *resolver.Manager, indexTmpl *template.Template) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /api/proxy", proxyHandler)
	mux.Handle("OPTIONS /api/proxy", proxyHandler)
	// Service-Worker-Allowed widens the SW scope from /static/ to / so the SW
	// can intercept navigations to the root and control the full app.
	swHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/static/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		http.StripPrefix("/static/", http.FileServerFS(staticSub)).ServeHTTP(w, r)
	})
	mux.Handle("GET /static/", swHandler)

	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		raw, gz, etag := channels.payload()
		h := w.Header()
		h.Set("Content-Type", "application/json; charset=utf-8")
		// no-cache (unlike no-store) lets the browser keep the body and
		// revalidate with If-None-Match — unchanged lists cost one 304.
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", etag)
		h.Set("Vary", "Accept-Encoding")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			w.Write(gz)
			return
		}
		w.Write(raw)
	})

	// Sync re-fetches the full catalog from the API and upserts it into the
	// catalog by stable ID (preserving favourites and manual rows). refresh()
	// serializes itself, so a double-click can't race two downloads.
	// Sync is disabled for now. The route stays registered (and refresh()
	// remains) so a future sync rework can re-enable it cleanly.
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sync is disabled", http.StatusForbidden)
	})

	// Lightweight status the UI polls so it can show an "updating" state and
	// re-fetch /api/channels once the background refresh lands.
	mux.HandleFunc("GET /api/source", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"refreshing":%v,"channels":%d,"recordingAvailable":%v}`,
			channels.isRefreshing(), channels.channelCount(), rec.Available())
	})

	// Favourites and manual channels live in the catalog (replacing the old
	// browser localStorage), keyed by stable ID so they survive a re-sync.
	mux.HandleFunc("POST /api/favourite", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
			On bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ok, err := st.SetFavourite(body.ID, body.On)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if !ok {
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		channels.rebuild()
		writeJSON(w, r, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/channels/add", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, URL, License, Referer, UserAgent string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name, url := strings.TrimSpace(body.Name), strings.TrimSpace(body.URL)
		if name == "" || !isStreamURL(url) {
			http.Error(w, "channel name and an http(s) stream link are required", http.StatusBadRequest)
			return
		}
		ch, err := st.AddManual(name, url, nil, body.Referer, body.UserAgent)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		// Optional ClearKey for CENC streams, entered as "KID:KEY"; ignored if not
		// a valid hex pair (e.g. a stray Widevine URL). Keys live in the global
		// keystore (keys.json), not the catalog, so they survive a DB wipe and
		// apply to any DASH stream advertising a matching KID.
		if keys := playlist.ParseClearKeys(body.License); len(keys) > 0 {
			if n, err := ks.Merge(keys); err != nil {
				log.Printf("keystore: merge on add: %v", err)
			} else if n > 0 {
				log.Printf("keystore: +%d key(s) from add %q", n, name)
			}
		}
		channels.rebuild()
		log.Printf("channels: added manual %q (%s)", name, ch.ID)
		writeJSON(w, r, ch)
	})

	// Add a dynamic channel resolved at play time (e.g. an exposestrat slug). The
	// stream URL is not stored; the recipe (provider + arg) is. referer/userAgent
	// are what the resolved stream needs when played.
	mux.HandleFunc("POST /api/channels/add-resolvable", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, Provider, Arg, Referer, UserAgent string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		provider := strings.TrimSpace(body.Provider)
		arg := strings.TrimSpace(body.Arg)
		if name == "" || arg == "" {
			http.Error(w, "channel name and stream arg are required", http.StatusBadRequest)
			return
		}
		if !resolverMgr.Has(provider) {
			http.Error(w, "unknown resolver provider", http.StatusBadRequest)
			return
		}
		ch, err := st.AddResolvable(name, provider, arg, body.Referer, body.UserAgent)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		channels.rebuild()
		log.Printf("channels: added resolvable %q via %s:%s (%s)", name, provider, arg, ch.ID)
		writeJSON(w, r, ch)
	})

	// Resolve a dynamic channel to a fresh playable URL. Caches the result into the
	// channel's servers[0] (so the proxy header map picks up a rotated CDN host) and
	// returns {url, referer} for the player to load via /api/proxy.
	mux.HandleFunc("GET /api/resolve", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		ch, err := st.Get(id)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if ch.Resolver == "" {
			http.Error(w, "channel is not resolvable", http.StatusBadRequest)
			return
		}
		res, err := resolverMgr.Resolve(r.Context(), ch.Resolver, ch.ResolverArg)
		if err != nil {
			log.Printf("resolve %q (%s:%s): %v", ch.Name, ch.Resolver, ch.ResolverArg, err)
			http.Error(w, "could not resolve stream", http.StatusBadGateway)
			return
		}
		// Persist the fresh URL so the proxy's host→header map covers the (possibly
		// rotated) CDN host before the player fetches through the proxy.
		if err := st.SetResolvedURL(id, res.URL); err != nil {
			log.Printf("resolve: cache url for %s: %v", id, err)
		} else {
			channels.rebuild()
		}
		ref := res.Referer
		if ref == "" {
			ref = ch.Referer
		}
		writeJSON(w, r, map[string]string{"url": res.URL, "referer": ref, "user_agent": res.UserAgent})
	})

	mux.HandleFunc("POST /api/channels/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID, Name, URL, License, Referer, UserAgent string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name, url := strings.TrimSpace(body.Name), strings.TrimSpace(body.URL)
		if body.ID == "" || name == "" || !isStreamURL(url) {
			http.Error(w, "channel id, name and an http(s) stream link are required", http.StatusBadRequest)
			return
		}
		ch, err := st.UpdateManual(body.ID, name, url, body.Referer, body.UserAgent)
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		case errors.Is(err, store.ErrNotManual):
			http.Error(w, "only manually-added channels can be edited", http.StatusConflict)
			return
		case err != nil:
			serverError(w, "api", err)
			return
		}
		// Optional ClearKey edit — merged into the global keystore like /add.
		if keys := playlist.ParseClearKeys(body.License); len(keys) > 0 {
			if n, err := ks.Merge(keys); err != nil {
				log.Printf("keystore: merge on update: %v", err)
			} else if n > 0 {
				log.Printf("keystore: +%d key(s) from update %q", n, ch.Name)
			}
		}
		channels.rebuild()
		log.Printf("channels: updated manual %q (%s)", ch.Name, ch.ID)
		writeJSON(w, r, ch)
	})

	mux.HandleFunc("DELETE /api/channels/add", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch err := st.DeleteManual(body.ID); {
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		case errors.Is(err, store.ErrNotManual):
			http.Error(w, "only manually-added channels can be removed", http.StatusConflict)
			return
		case err != nil:
			serverError(w, "api", err)
			return
		}
		channels.rebuild()
		writeJSON(w, r, map[string]bool{"ok": true})
	})

	// Import a user-supplied .m3u: /parse extracts (name,url) entries for the
	// review popup without saving; /save persists the reviewed list as manual
	// channels. Parsing reuses the canonical playlist extractor.
	mux.HandleFunc("POST /api/import/parse", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
			return
		}
		entries := playlist.ParseEntries(string(data))
		out := make([]store.ImportEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, store.ImportEntry{Name: e.Name, URL: e.URL, ClearKeys: e.ClearKeys})
		}
		if len(out) == 0 {
			http.Error(w, "no channels found — is this a valid .m3u playlist?", http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, r, map[string]any{"entries": out})
	})

	// Cross-check reviewed entries against the catalog by LINK (exact URL). It
	// mutates nothing — it returns the new entries plus the duplicates (with the
	// existing channel they collide with) so the UI can report conflicts before
	// the user commits. Save is still authoritative (it re-dedupes by URL).
	mux.HandleFunc("POST /api/import/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entries []store.ImportEntry `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		idx, err := st.URLIndex()
		if err != nil {
			serverError(w, "api", err)
			return
		}
		type duplicate struct {
			Imported     store.ImportEntry `json:"imported"`
			ExistingID   string            `json:"existingId"`
			ExistingName string            `json:"existingName"`
		}
		newOnes := []store.ImportEntry{}
		dups := []duplicate{}
		seen := map[string]bool{}
		for _, e := range body.Entries {
			name, url := strings.TrimSpace(e.Name), strings.TrimSpace(e.URL)
			if name == "" || !isStreamURL(url) {
				continue
			}
			if ref, ok := idx[url]; ok {
				dups = append(dups, duplicate{Imported: store.ImportEntry{Name: name, URL: url}, ExistingID: ref.ID, ExistingName: ref.Name})
				continue
			}
			if seen[url] {
				continue // repeated link within the file — keep the first only
			}
			seen[url] = true
			newOnes = append(newOnes, store.ImportEntry{Name: name, URL: url, ClearKeys: e.ClearKeys})
		}
		writeJSON(w, r, map[string]any{"new": newOnes, "duplicates": dups})
	})

	mux.HandleFunc("POST /api/import/save", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entries []store.ImportEntry `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		added, err := st.ImportManual(body.Entries)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		// Auto-harvest any ClearKey pairs the playlist carried (e.g. #KODIPROP
		// license_key) into the global keystore in one write, so imported DRM
		// channels just play without re-entering keys.
		harvest := map[string]string{}
		for _, e := range body.Entries {
			for kid, key := range e.ClearKeys {
				harvest[kid] = key
			}
		}
		if n, err := ks.Merge(harvest); err != nil {
			log.Printf("keystore: merge on import: %v", err)
		} else if n > 0 {
			log.Printf("keystore: +%d key(s) harvested from import", n)
		}
		channels.rebuild()
		log.Printf("channels: imported %d manual channel(s)", added)
		writeJSON(w, r, map[string]int{"added": added})
	})

	// ── Xtream Codes playlists ──────────────────────────────────────────────
	// Save a panel's credentials and immediately import its live channels. The
	// fetch is synchronous (the UI shows a spinner): the request returns the saved
	// playlist plus how many channels were imported.
	mux.HandleFunc("POST /api/xtream/playlists", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, Server, Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		server := xtream.NormalizeServer(body.Server)
		username := strings.TrimSpace(body.Username)
		password := body.Password
		if name == "" || username == "" || password == "" || !isStreamURL(server) {
			http.Error(w, "name, server (http/https), username and password are required", http.StatusBadRequest)
			return
		}
		// Verify credentials before persisting anything, so a typo doesn't leave a
		// dead playlist behind.
		streams, streamsRaw, err := xtream.LiveStreamsRaw(server, username, password)
		if err != nil {
			log.Printf("xtream: import %q: %v", name, err)
			http.Error(w, "could not reach the panel or the credentials were rejected", http.StatusBadGateway)
			return
		}
		p, err := st.SaveXtreamPlaylist(name, server, username, password)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		cats, catsRaw, err := xtream.LiveCategoriesRaw(server, username, password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (importing without groups)", name, err)
			cats = nil
			catsRaw = nil
		}
		added, _, err := importXtreamStreams(st, p, streams, cats)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if err := st.StampXtreamRefreshed(p.ID); err != nil {
			log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
		}
		channels.rebuild()
		log.Printf("xtream: saved playlist %q (%s), imported %d channel(s)", name, p.ID, added)
		writeJSON(w, r, map[string]any{
			"playlist": p,
			"imported": added,
			"debug": map[string]any{
				"login":      nil,
				"categories": catsRaw,
				"streams":    streamsRaw,
			},
		})
	})

	// List saved playlists (password omitted from the response), each carrying an
	// imported flag so the UI knows whether selecting it needs a fetch.
	mux.HandleFunc("GET /api/xtream/playlists", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListXtreamPlaylists()
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if list == nil {
			list = []store.XtreamPlaylist{}
		}
		writeJSON(w, r, list)
	})

	// Re-fetch an existing playlist's live channels and upsert them by the stable
	// xtream:<playlist>:<stream> id (updates existing rows, adds new ones).
	mux.HandleFunc("POST /api/xtream/playlists/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, err := st.GetXtreamPlaylist(id)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		streams, streamsRaw, err := xtream.LiveStreamsRaw(p.Server, p.Username, p.Password)
		if err != nil {
			log.Printf("xtream: refresh %q: %v", p.Name, err)
			http.Error(w, "could not reach the panel or the credentials were rejected", http.StatusBadGateway)
			return
		}
		cats, catsRaw, err := xtream.LiveCategoriesRaw(p.Server, p.Username, p.Password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (refreshing without groups)", p.Name, err)
			cats = nil
			catsRaw = nil
		}
		added, updated, err := importXtreamStreams(st, p, streams, cats)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if err := st.StampXtreamRefreshed(p.ID); err != nil {
			log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
		}
		channels.rebuild()
		log.Printf("xtream: refreshed %q (%s): +%d new, %d updated", p.Name, p.ID, added, updated)
		writeJSON(w, r, map[string]any{
			"added":   added,
			"updated": updated,
			"debug": map[string]any{
				"login":      nil, // refresh doesn't call LoginRaw separately; streams call logs in already
				"categories": catsRaw,
				"streams":    streamsRaw,
			},
		})
	})

	// Update a saved playlist's per-playlist settings (auto-refresh cadence and
	// stream type). Applying a new stream type takes effect on the next refresh;
	// this endpoint only persists the choice.
	mux.HandleFunc("PATCH /api/xtream/playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			UpdateFreq string `json:"update_freq"`
			StreamType string `json:"stream_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p, err := st.UpdateXtreamSettings(id, body.UpdateFreq, body.StreamType)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, store.ErrInvalidSetting) {
			http.Error(w, "update_freq must be manual/daily/3days/weekly and stream_type ts/m3u8", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		writeJSON(w, r, p)
	})

	mux.HandleFunc("GET /api/keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, ks.All())
	})

	// ── Screen recording (server-side, ffmpeg → 720p H.264/AAC MP4) ──────────
	mux.HandleFunc("POST /api/record/start", func(w http.ResponseWriter, r *http.Request) {
		if !rec.Available() {
			http.Error(w, "recording unavailable: ffmpeg not found", http.StatusServiceUnavailable)
			return
		}
		var body struct{ URL, Name string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, file, err := rec.Start(body.URL, body.Name, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("recording: started %s → %s", id, file)
		writeJSON(w, r, map[string]string{"id": id, "file": file})
	})
	mux.HandleFunc("POST /api/record/stop", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		file, err := rec.Stop(body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Printf("recording: stopped %s → %s", body.ID, file)
		writeJSON(w, r, map[string]string{"id": body.ID, "file": file})
	})
	mux.HandleFunc("GET /api/record", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, rec.Status(time.Now()))
	})
	// Serve a saved recording for download. Base-name only so the path can't
	// escape the recordings directory.
	mux.HandleFunc("GET /api/record/file", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			http.Error(w, "bad name", http.StatusBadRequest)
			return
		}
		// Strip quotes/control chars so the filename can't break out of the
		// header value (filepath.Base already prevents path traversal).
		safe := strings.Map(func(r rune) rune {
			if r == '"' || r == '\\' || r < 0x20 {
				return -1
			}
			return r
		}, name)
		w.Header().Set("Content-Disposition", `attachment; filename="`+safe+`"`)
		http.ServeFile(w, r, filepath.Join(rec.Dir(), name))
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, nil); err != nil {
			log.Printf("template: %v", err)
		}
	})
	return mux
}
