// watchlive is a single-binary live TV streaming server: it serves the web
// UI, proxies HLS streams through an in-memory segment cache, and tracks
// live viewer counts. All assets are embedded; an external list.txt next to
// the binary (or via -playlist) overrides the embedded channel playlist and
// is hot-reloaded so channels can be added without a restart.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
// changes on disk.
type channelStore struct {
	mu       sync.RWMutex
	channels []playlist.Channel
	path     string // external playlist path; empty → embedded only
	modTime  time.Time
}

func newChannelStore(path string) *channelStore {
	cs := &channelStore{path: path}
	cs.reload()
	return cs
}

// reload re-reads the playlist source. Returns the number of channels loaded.
func (cs *channelStore) reload() int {
	content := embeddedPlaylist
	var modTime time.Time
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
	parsed := playlist.Parse(string(content))
	if len(parsed) == 0 {
		log.Printf("playlist: parsed 0 channels, keeping current list")
		return cs.count()
	}
	cs.mu.Lock()
	cs.channels = parsed
	cs.modTime = modTime
	cs.mu.Unlock()
	return len(parsed)
}

// reloadIfChanged reloads only when the external file's mtime moved.
func (cs *channelStore) reloadIfChanged() {
	if cs.path == "" {
		return
	}
	info, err := os.Stat(cs.path)
	if err != nil {
		return
	}
	cs.mu.RLock()
	changed := !info.ModTime().Equal(cs.modTime)
	cs.mu.RUnlock()
	if changed {
		n := cs.reload()
		log.Printf("playlist: reloaded %d channels from %s", n, cs.path)
	}
}

func (cs *channelStore) get() []playlist.Channel {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.channels
}

func (cs *channelStore) count() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.channels)
}

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	playlistPath := flag.String("playlist", "", "path to external M3U playlist (default: list.txt next to the binary, falling back to the embedded copy)")
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

	channels := newChannelStore(path)
	log.Printf("playlist: %d channels loaded (external source: %s)", channels.count(), path)

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
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(channels.get())
	})

	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		n := channels.reload()
		log.Printf("playlist: manual reload → %d channels", n)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"channels":%d}`, n)
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		chJSON, err := json.Marshal(channels.get())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, struct{ ChannelsJSON template.JS }{template.JS(chJSON)}); err != nil {
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
