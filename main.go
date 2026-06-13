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
	"net/http"
	"os"
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
}

func newChannelStore(path string) *channelStore {
	cs := &channelStore{path: path}
	cs.reload()
	return cs
}

// reload re-reads the playlist from disk. Returns the number of channels loaded.
func (cs *channelStore) reload() int {
	data, err := os.ReadFile(cs.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("playlist: read %s: %v (keeping current list)", cs.path, err)
		} else {
			log.Printf("playlist: %s not found", cs.path)
		}
		return cs.count()
	}
	info, _ := os.Stat(cs.path)
	var modTime time.Time
	if info != nil {
		modTime = info.ModTime()
	}

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

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	playlistPath := flag.String("playlist", "", "path to M3U playlist (default: list.m3u next to the binary)")
	syncURL := flag.String("sync-url", "https://iptv-org.github.io/iptv/index.country.m3u", "upstream playlist downloaded by POST /api/sync")
	cacheMB := flag.Int64("cache-mb", 200, "segment cache size in MB")
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

	// syncMu serializes syncs; a double-click must not race two downloads.
	var syncMu sync.Mutex
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		syncMu.Lock()
		defer syncMu.Unlock()

		client := &http.Client{Timeout: 2 * time.Minute}
		resp, err := client.Get(*syncURL)
		if err != nil {
			http.Error(w, "sync: download failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("sync: upstream returned %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			http.Error(w, "sync: read failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(string(data[:min(len(data), 64)])), "#EXTM3U") {
			http.Error(w, "sync: response is not an M3U playlist", http.StatusBadGateway)
			return
		}

		// Merge, never overwrite: keep every existing entry and append only the
		// upstream entries whose stream URL we don't already have. This is what
		// lets a local list larger than upstream survive a sync untouched.
		existing, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, "sync: read existing failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		merged, added := mergeNewEntries(existing, data)

		// Stamp tvg-genre on every entry (existing + new) from iptv-org's
		// category database so the UI can group by category. Best-effort: if
		// the database is unreachable, fall through with the un-enriched merge.
		stamped := 0
		if gm, err := genre.Load(); err != nil {
			log.Printf("sync: genre enrich skipped: %v", err)
		} else {
			merged, stamped = gm.Inject(merged)
		}

		if added == 0 && stamped == 0 {
			// Nothing new and nothing to enrich — leave the file as is.
			n := channels.count()
			log.Printf("sync: %d KB from %s → no changes (kept %d)", len(data)/1024, *syncURL, n)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			fmt.Fprintf(w, `{"channels":%d,"added":0}`, n)
			return
		}

		// Atomic replace: never leave a half-written playlist on disk.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, merged, 0o644); err != nil {
			http.Error(w, "sync: write failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			http.Error(w, "sync: replace failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		n := channels.reload()
		log.Printf("sync: %d KB from %s → +%d new entries, %d genre-tagged (%d channels)", len(data)/1024, *syncURL, added, stamped, n)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"channels":%d,"added":%d,"tagged":%d}`, n, added, stamped)
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

	go func() {
		log.Printf("listening on http://localhost%s", displayAddr(*addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

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

func displayAddr(addr string) string {
	if addr != "" && addr[0] == ':' {
		return addr
	}
	return " (" + addr + ")"
}
