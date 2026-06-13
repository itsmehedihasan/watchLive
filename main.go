// watchlive is a single-binary live TV streaming server: it serves the web
// UI, proxies HLS streams through an in-memory segment cache, and tracks
// live viewer counts. All assets are embedded; an external list.txt next to
// the binary (or via -playlist) overrides the embedded channel playlist and
// is hot-reloaded so channels can be added without a restart.
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

	"watchlive/internal/playlist"
	"watchlive/internal/proxy"
	"watchlive/internal/viewers"
)

//go:embed web/templates/index.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

//go:embed list.txt
var embeddedPlaylist []byte

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
	path     string // curated playlist path; empty → embedded only
	// syncPath holds the playlist downloaded from the upstream source via
	// /api/sync. It is loaded after (appended to) the curated list and is the
	// only file sync overwrites — list.txt stays user-owned.
	syncPath    string
	modTime     time.Time
	syncModTime time.Time
}

func newChannelStore(path, syncPath string) *channelStore {
	cs := &channelStore{path: path, syncPath: syncPath}
	cs.reload()
	return cs
}

// reload re-reads the playlist sources. Returns the number of channels loaded.
func (cs *channelStore) reload() int {
	content := append([]byte{}, embeddedPlaylist...)
	var modTime, syncModTime time.Time
	if cs.path != "" {
		if data, err := os.ReadFile(cs.path); err == nil {
			content = data
			if info, err := os.Stat(cs.path); err == nil {
				modTime = info.ModTime()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("playlist: read %s: %v (keeping current list)", cs.path, err)
			return cs.count()
		}
	}
	if cs.syncPath != "" {
		if data, err := os.ReadFile(cs.syncPath); err == nil {
			content = append(append(content, '\n'), data...)
			if info, err := os.Stat(cs.syncPath); err == nil {
				syncModTime = info.ModTime()
			}
		}
	}
	parsed := playlist.Parse(string(content))
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
	cs.syncModTime = syncModTime
	cs.mu.Unlock()
	return len(parsed)
}

// payload returns the precomputed /api/channels response bodies.
func (cs *channelStore) payload() (raw, gz []byte, etag string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.jsonRaw, cs.jsonGz, cs.etag
}

// reloadIfChanged reloads only when a source file's mtime moved.
func (cs *channelStore) reloadIfChanged() {
	var cur, curSync time.Time
	if cs.path != "" {
		if info, err := os.Stat(cs.path); err == nil {
			cur = info.ModTime()
		}
	}
	if cs.syncPath != "" {
		if info, err := os.Stat(cs.syncPath); err == nil {
			curSync = info.ModTime()
		}
	}
	cs.mu.RLock()
	changed := !cur.Equal(cs.modTime) || !curSync.Equal(cs.syncModTime)
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

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	playlistPath := flag.String("playlist", "", "path to external M3U playlist (default: list.txt next to the binary, falling back to the embedded copy)")
	syncURL := flag.String("sync-url", "https://iptv-org.github.io/iptv/index.country.m3u", "upstream playlist downloaded by POST /api/sync (country-grouped variant)")
	cacheMB := flag.Int64("cache-mb", 200, "segment cache size in MB")
	flag.Parse()

	// Default external playlist: list.txt next to the executable, so editing
	// it adds channels without rebuilding.
	path := *playlistPath
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), "list.txt")
		} else {
			path = "list.txt"
		}
	}
	syncPath := filepath.Join(filepath.Dir(path), "list.sync.m3u")

	channels := newChannelStore(path, syncPath)
	log.Printf("playlist: %d channels loaded (curated: %s, synced: %s)", channels.count(), path, syncPath)

	store := viewers.NewStore()
	proxyHandler := proxy.New(*cacheMB << 20)

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

	// syncMu serializes syncs; a double-click must not race two downloads
	// into the same file.
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
		// Atomic replace: never leave a half-written playlist on disk.
		tmp := syncPath + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			http.Error(w, "sync: write failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, syncPath); err != nil {
			os.Remove(tmp)
			http.Error(w, "sync: replace failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		n := channels.reload()
		log.Printf("sync: downloaded %d KB from %s → %d channels", len(data)/1024, *syncURL, n)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"channels":%d}`, n)
	})

	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		n := channels.reload()
		log.Printf("playlist: manual reload → %d channels", n)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"channels":%d}`, n)
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

func displayAddr(addr string) string {
	if addr != "" && addr[0] == ':' {
		return addr
	}
	return " (" + addr + ")"
}
