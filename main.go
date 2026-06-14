// watchlive is a single-binary live TV streaming server: it serves the web
// UI, proxies HLS streams through an in-memory segment cache, and tracks
// live viewer counts. All assets are embedded; list.m3u next to the binary
// (or via -playlist) is the channel playlist and is hot-reloaded so channels
// can be added without a restart.
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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"watchlive/internal/genre"
	"watchlive/internal/health"
	"watchlive/internal/playlist"
	"watchlive/internal/proxy"
	"watchlive/internal/viewers"
)

//go:embed web/templates/index.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

// seedPlaylist is a starter channel list compiled into the binary. It is the
// cold-start fallback when no list.m3u exists next to the binary yet, so the
// app shows channels immediately on first run — even with no network. A real
// list.m3u (written by the background refresh, or user-provided) always wins.
//
//go:embed seed.m3u
var seedPlaylist []byte

// channelStore holds the parsed playlist and reloads it when the source file
// changes on disk. The JSON and gzipped-JSON payloads for /api/channels are
// precomputed once per reload — with 10k+ channels the list is megabytes, so
// compressing per request would burn CPU for an identical result.
type channelStore struct {
	mu       sync.RWMutex
	channels []playlist.Channel
	jsonRaw  []byte
	jsonGz   []byte
	etag     string
	path     string
	modTime  time.Time
	// refreshing reports whether a source refresh (API fetch) is in flight, so
	// the UI can show an "updating" state and re-fetch when it lands.
	refreshing bool

	// refreshMu serializes source refreshes (startup + manual Sync) so two
	// fetches never race on the playlist file.
	refreshMu sync.Mutex
}

func newChannelStore(path string) *channelStore {
	cs := &channelStore{path: path}
	cs.reload()
	return cs
}

// reload re-reads the playlist from disk. When the file doesn't exist yet, it
// falls back to the embedded seed playlist so the first run is never empty.
// Returns the number of channels loaded.
func (cs *channelStore) reload() int {
	data, err := os.ReadFile(cs.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("playlist: read %s: %v (keeping current list)", cs.path, err)
			return cs.count()
		}
		// No list.m3u yet — serve the embedded seed (zero modTime: reloadIfChanged
		// keys off the on-disk file, which is still absent). The background refresh
		// writes a real list.m3u and a later reload picks it up.
		log.Printf("playlist: %s not found, loading embedded seed", cs.path)
		return cs.setFromBytes(seedPlaylist, time.Time{})
	}
	info, _ := os.Stat(cs.path)
	var modTime time.Time
	if info != nil {
		modTime = info.ModTime()
	}
	return cs.setFromBytes(data, modTime)
}

// setFromBytes parses an M3U payload and, if it yields any channels, swaps it in
// as the current list along with the precomputed JSON/gzip/etag. A payload that
// parses to zero channels (or fails to marshal) is rejected and the current list
// is kept. Returns the channel count after the attempt.
func (cs *channelStore) setFromBytes(data []byte, modTime time.Time) int {
	parsed := playlist.Parse(string(data))
	if len(parsed) == 0 {
		log.Printf("playlist: parsed 0 channels, keeping current list")
		return cs.count()
	}

	raw, err := json.Marshal(parsed)
	if err != nil {
		log.Printf("playlist: marshal: %v (keeping current list)", err)
		return cs.count()
	}
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gz.Write(raw)
	gz.Close()
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`

	cs.mu.Lock()
	cs.channels = parsed
	cs.jsonRaw = raw
	cs.jsonGz = buf.Bytes()
	cs.etag = etag
	cs.modTime = modTime
	cs.mu.Unlock()
	return len(parsed)
}

// payload returns the precomputed /api/channels response bodies.
func (cs *channelStore) payload() (raw, gz []byte, etag string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.jsonRaw, cs.jsonGz, cs.etag
}

// reloadIfChanged reloads only when the file's mtime moved.
func (cs *channelStore) reloadIfChanged() {
	info, err := os.Stat(cs.path)
	if err != nil {
		return
	}
	cs.mu.RLock()
	changed := !info.ModTime().Equal(cs.modTime)
	cs.mu.RUnlock()
	if changed {
		n := cs.reload()
		log.Printf("playlist: reloaded %d channels", n)
	}
}

func (cs *channelStore) count() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.channels)
}

// healthTargets snapshots the current channels as probe targets along with the
// list's etag. The etag lets the prober reuse results until the playlist
// actually changes (a re-sync reassigns IDs, so old verdicts would be wrong).
func (cs *channelStore) healthTargets() ([]health.Target, string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	targets := make([]health.Target, 0, len(cs.channels))
	for _, ch := range cs.channels {
		urls := make([]string, 0, len(ch.Servers))
		for _, s := range ch.Servers {
			urls = append(urls, s.URL)
		}
		targets = append(targets, health.Target{ID: ch.ID, URLs: urls})
	}
	return targets, cs.etag
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
// category from the iptv-org database, writes the result to the playlist file
// (which thereby doubles as the offline fallback), and reloads. The previous
// file is backed up once to <path>.bak so a user-curated list is recoverable.
// Returns the channel count after reload.
func (cs *channelStore) refresh(sourceURL string) (int, error) {
	cs.refreshMu.Lock()
	defer cs.refreshMu.Unlock()
	cs.setRefreshing(true)
	defer cs.setRefreshing(false)

	enriched, n, err := fetchAndEnrich(sourceURL)
	if err != nil {
		return 0, err
	}

	// One-time backup of any pre-existing playlist before we start overwriting
	// it with the API catalog.
	if bak := cs.path + ".bak"; fileExists(cs.path) && !fileExists(bak) {
		if data, err := os.ReadFile(cs.path); err == nil {
			os.WriteFile(bak, data, 0o644)
		}
	}

	tmp := cs.path + ".tmp"
	if err := os.WriteFile(tmp, enriched, 0o644); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmp, cs.path); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("replace: %w", err)
	}
	log.Printf("source: enriched %d entries from %s", n, sourceURL)
	return cs.reload(), nil
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	playlistPath := flag.String("playlist", "", "path to M3U playlist cache (default: list.m3u next to the binary)")
	sourceURL := flag.String("source-url", "https://iptv-org.github.io/iptv/index.m3u", "upstream playlist fetched at startup and by Sync; list.m3u is the offline fallback")
	noRefresh := flag.Bool("no-refresh", false, "skip the startup fetch from -source-url and use the local list.m3u as-is")
	cacheMB := flag.Int64("cache-mb", 200, "segment cache size in MB")
	open := flag.Bool("open", true, "open the web UI in the default browser once the server is listening")
	flag.Parse()

	// Default playlist resolution: prefer list.m3u next to the executable, but
	// fall back to ./list.m3u (the working directory) when it isn't there. The
	// fallback matters for `go run .`, where the binary lives in a temp build
	// dir that never contains the playlist.
	path := *playlistPath
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exe), "list.m3u")
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		}
		if path == "" {
			path = "list.m3u"
		}
	}

	channels := newChannelStore(path)
	log.Printf("playlist: %d channels loaded from %s", channels.count(), path)

	// Channels come from the API; the local list.m3u is just the offline cache.
	// Serve whatever is on disk immediately, then refresh from -source-url in the
	// background so the first paint isn't blocked on the network. Mark refreshing
	// up front so the very first /api/source poll already reports it.
	if !*noRefresh {
		channels.setRefreshing(true)
		go func() {
			n, err := channels.refresh(*sourceURL)
			if err != nil {
				log.Printf("source: refresh failed, using local fallback (%d channels): %v", channels.count(), err)
				return
			}
			log.Printf("source: refreshed to %d channels from %s", n, *sourceURL)
		}()
	}

	store := viewers.NewStore()
	proxyHandler := proxy.New(*cacheMB << 20)
	prober := health.New()

	indexTmpl := template.Must(template.ParseFS(templateFS, "web/templates/index.html"))
	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api/proxy", proxyHandler)
	mux.Handle("OPTIONS /api/proxy", proxyHandler)
	mux.Handle("/api/viewers", &viewers.Handler{Store: store})
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

	// Sync re-fetches the full catalog from the API (resolving country and
	// category) and overwrites the local cache. refresh() serializes itself, so
	// a double-click can't race two downloads.
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		n, err := channels.refresh(*sourceURL)
		if err != nil {
			http.Error(w, "sync: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"channels":%d}`, n)
	})

	// Lightweight status the UI polls so it can show an "updating" state and
	// re-fetch /api/channels once the background refresh lands.
	mux.HandleFunc("GET /api/source", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"refreshing":%v,"channels":%d}`, channels.isRefreshing(), channels.count())
	})

	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		n := channels.reload()
		log.Printf("playlist: manual reload → %d channels", n)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"channels":%d}`, n)
	})

	// Server-side stream health. POST starts (or reuses) a probe pass over the
	// current playlist; GET returns its progress and verdicts. The "Working
	// only" toggle in the UI drives this: it kicks off a pass and polls until
	// done, hiding channels the server couldn't reach.
	mux.HandleFunc("POST /api/health", func(w http.ResponseWriter, r *http.Request) {
		targets, etag := channels.healthTargets()
		snap := prober.Start(targets, etag)
		log.Printf("health: probe requested (%d channels, running=%v done=%d)", snap.Total, snap.Running, snap.Done)
		writeJSON(w, r, snap)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, prober.Snapshot())
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, nil); err != nil {
			log.Printf("template: %v", err)
		}
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background maintenance: prune stale viewer sessions, pick up playlist edits.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				store.Prune()
				channels.reloadIfChanged()
			}
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// mergeNewEntries appends entries from upstream whose stream URL is not already
// present in existing, deduplicating by URL. Existing content is preserved
// verbatim; each appended entry keeps its #EXTINF line plus any directive lines
// (e.g. #EXTVLCOPT) and the URL. Returns the merged playlist and the number of
// entries added — added==0 means nothing changed and existing is returned as is.
func mergeNewEntries(existing, upstream []byte) ([]byte, int) {
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(existing), "\n") {
		if u := strings.TrimSpace(line); isStreamURL(u) {
			seen[u] = true
		}
	}

	var out strings.Builder
	out.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		out.WriteByte('\n')
	}

	var block []string // lines of the entry currently being read
	var blockURL string
	added := 0

	flush := func() {
		if blockURL != "" && !seen[blockURL] {
			seen[blockURL] = true
			added++
			for _, l := range block {
				out.WriteString(l)
				out.WriteByte('\n')
			}
		}
		block = block[:0]
		blockURL = ""
	}

	for _, raw := range strings.Split(string(upstream), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#EXTINF") {
			flush() // finalize the previous entry before starting a new one
			block = append(block, line)
		} else if len(block) > 0 && line != "" {
			block = append(block, line)
			if blockURL == "" && isStreamURL(line) {
				blockURL = line
			}
		}
	}
	flush()

	if added == 0 {
		return existing, 0
	}
	return []byte(out.String()), added
}

func isStreamURL(line string) bool {
	return strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://")
}

// writeJSON marshals v and writes it, gzipping when the client accepts it. The
// health snapshot carries a status entry per channel, so for a large playlist
// it is hundreds of KB — worth compressing even on localhost.
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
