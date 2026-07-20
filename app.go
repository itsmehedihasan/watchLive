package app

// app.go is the importable entry point for the whole server. main.go holds the
// channel store, HTTP handlers, and embedded assets; this file assembles them
// into a runnable App. Both binaries build on it: cmd/watchlive (the plain web
// server, a drop-in replacement for the old func main) and cmd/watchlive-native
// (the WebView2 + libmpv shell), which starts the same server in-process on a
// loopback ephemeral port and reads back Addr() to point its WebView2 at it.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"html/template"

	"watchlive/internal/ffmpeg"
	"watchlive/internal/health"
	"watchlive/internal/keystore"
	"watchlive/internal/playlist"
	"watchlive/internal/proxy"
	"watchlive/internal/recorder"
	"watchlive/internal/resolver"
	"watchlive/internal/store"
	"watchlive/internal/viewers"
)

// Config is the full runtime configuration for the server. It mirrors the flags
// the old func main parsed; cmd/watchlive fills it from those same flags, while
// cmd/watchlive-native fills it in code (loopback ephemeral port, no browser).
type Config struct {
	Addr         string // listen address, e.g. ":3000" or "127.0.0.1:0"
	DBPath       string // SQLite catalog path (empty → store/<name>.db beside the binary)
	PlaylistPath string // -playlist FILE: throwaway session from a custom .m3u
	SourceURL    string // upstream playlist (retained for a future sync rework)
	RecDir       string // directory for screen recordings
	NoRefresh    bool   // retained; sync is off regardless
	Prune        bool   // on sync, delete channels no longer upstream
	Open         bool   // open the web UI in the default browser once listening
	AllowPrivate bool   // allow the proxy to fetch loopback/private addresses
	CacheMB      int64  // segment cache size in MB
}

// App is a built, ready-to-serve server. Handler is the mux exposed for tests
// and for direct in-process use; the unexported fields are what Serve and
// StartStaleProbe need. The TCP listener is bound in Build so Addr() is
// available before Serve blocks — cmd/watchlive-native needs the chosen port.
type App struct {
	Handler http.Handler

	channels *channelStore
	prober   *health.Prober
	st       *store.Store
	rec      *recorder.Recorder
	ks       *keystore.Store
	viewers  *viewers.Store

	ln   net.Listener
	url  string // browser-facing URL (localhost + configured addr), for Open
	open bool   // open the browser once listening (from Config.Open)
}

// Addr returns the address the server is actually listening on. With Config.Addr
// set to an ephemeral port ("127.0.0.1:0"), this is the only way to learn the
// port the OS chose — the native shell reads it to build its base URL.
func (a *App) Addr() net.Addr { return a.ln.Addr() }

// Build assembles the server from cfg: opens the store and keystore, seeds the
// catalog, wires the proxy/recorder/prober, registers all routes, and binds the
// TCP listener. It returns the App, a cleanup func (rec.StopAll + st.Close), and
// any error. On error the cleanup has already run, so the caller must not call
// it. This is the setup body of the old func main, with log.Fatalf replaced by
// returned errors so the native shell can surface failures without exiting.
func Build(cfg Config) (*App, func() error, error) {
	// baseDir is where the catalog directory and keys.json live: beside the
	// executable for a distributed binary, so state persists across restarts.
	// Under `go run` the binary is in a temp dir removed on exit, so fall back to
	// the working directory instead.
	baseDir := "."
	if exe, err := os.Executable(); err == nil && !isEphemeralExe(exe) {
		baseDir = filepath.Dir(exe)
	}

	// Playlist mode: -playlist FILE runs a throwaway session from a custom .m3u.
	// Validate the file up front so a missing/empty/non-m3u path fails fast.
	var seedFromPlaylist []byte
	playlistMode := cfg.PlaylistPath != ""
	if playlistMode {
		data, err := os.ReadFile(cfg.PlaylistPath)
		if err != nil {
			return nil, nil, fmt.Errorf("playlist: read %s: %w", cfg.PlaylistPath, err)
		}
		if len(playlist.Parse(string(data))) == 0 {
			return nil, nil, fmt.Errorf("playlist: %s contains no channels (is it a valid .m3u?)", cfg.PlaylistPath)
		}
		seedFromPlaylist = data
	}

	path := cfg.DBPath
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
			return nil, nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}

	// Reset the playlist-mode DB (and its WAL/SHM sidecars) before opening so each
	// run starts clean, discarding any state from a prior -playlist session. The
	// real catalog.db is only wiped this way if -playlist is pointed at it via -db.
	if playlistMode {
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("playlist: reset %s: %w", p, err)
			}
		}
		log.Printf("playlist mode: throwaway session from %s (catalog %s reset, API refresh off)", cfg.PlaylistPath, path)
	}

	st, err := store.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// ClearKey keystore: a standalone keys.json beside the catalog dir (NOT inside
	// it), so DRM keys survive a catalog wipe and apply globally to any DASH
	// stream whose manifest KID matches. Seed known keys so a fresh file still has
	// them; Merge is idempotent and only writes on a change.
	keysPath := filepath.Join(baseDir, "keys.json")
	ks, err := keystore.Open(keysPath)
	if err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("keystore: open %s: %w", keysPath, err)
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

	channels := newChannelStore(st, cfg.SourceURL, cfg.Prune)
	channels.playlistMode = playlistMode
	// Seed an empty catalog synchronously before serving so the first paint is
	// never blank. In playlist mode the DB was just reset, so this loads the
	// custom file; otherwise it's the embedded starter list (offline cold start).
	seed := seedPlaylist
	if playlistMode {
		seed = seedFromPlaylist
	}
	if err := channels.seedIfEmpty(seed); err != nil {
		log.Printf("playlist: seed: %v", err)
	}
	log.Printf("playlist: %d channels in catalog %s", channels.channelCount(), path)

	viewerStore := viewers.NewStore()
	proxyHandler := proxy.New(cfg.CacheMB << 20)
	proxyHandler.SetAllowPrivateUpstreams(cfg.AllowPrivate)

	// Per-channel upstream header overrides (UA / referer from #EXTVLCOPT) live in
	// the catalog. Rebuild the proxy's host→header map on every catalog change so
	// a CDN that gates on a specific UA/referer gets exactly what its channel
	// prescribes. The constructor's rebuild ran before this hook was set, so push
	// the initial map explicitly too.
	channels.afterRebuild = func(chs []store.Channel) {
		proxyHandler.SetUpstreamHeaders(upstreamHeadersFromCatalog(chs))
	}
	if chs, err := st.ListChannels(); err == nil {
		proxyHandler.SetUpstreamHeaders(upstreamHeadersFromCatalog(chs))
	}

	// seed.m3u is the source of truth for header hints: backfill http-user-agent /
	// http-referrer onto matching catalog channels (by exact stream URL) from the
	// embedded seed. Fill-only and header-only. Skipped in playlist mode.
	if !playlistMode {
		if n, err := st.BackfillHeaders(playlist.Parse(string(seedPlaylist))); err != nil {
			log.Printf("backfill headers: %v", err)
		} else {
			log.Printf("backfill: filled UA/referer on %d channel(s) from seed (exact-URL match)", n)
			if n > 0 {
				channels.rebuild() // refresh proxy header map + /api/channels payload
			}
		}
	}

	// Screen recording: resolve an ffmpeg (embedded copy, else PATH) and wire a
	// recorder that transcodes to 720p H.264/AAC MP4. Absent ffmpeg → recording
	// is simply disabled and the UI hides the button.
	rec := recorder.New(ffmpeg.Resolve(), cfg.RecDir)
	if rec.Available() {
		log.Printf("recording: enabled, saving to %s", rec.Dir())
	} else {
		log.Printf("recording: disabled (ffmpeg not found)")
	}

	// cleanup is the definitive teardown: finalize any in-progress recordings into
	// playable files, then close the store. Returned to the caller to defer.
	cleanup := func() error {
		rec.StopAll()
		return st.Close()
	}

	prober := health.New()
	// Verdicts persist in the catalog (channels.is_working). On completion of a
	// pass, write them back and rebuild the payload so the new state ships in
	// /api/channels. Seed the prober from the catalog so a fresh process serves
	// health results for the current list without re-probing until they go stale.
	prober.OnFinish(func(verdicts map[string]bool, at time.Time) {
		if err := st.SetHealth(verdicts, at); err != nil {
			log.Printf("health: persist verdicts: %v", err)
		}
		if err := channels.rebuild(); err != nil {
			log.Printf("health: rebuild after verdicts: %v", err)
		}
	})
	if verdicts, err := st.HealthVerdicts(); err == nil && len(verdicts) > 0 {
		prober.Seed(channels.etagValue(), verdicts, time.Now())
	}

	// Live background sync from -source-url is DISABLED: seed.m3u is the source of
	// truth. The flag and refresh()/fetchAndEnrich remain for a future rework.
	_ = cfg.NoRefresh

	indexTmpl := template.Must(template.ParseFS(templateFS, "web/templates/index.html"))
	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("static assets: %w", err)
	}

	// Resolver: some embed providers serve only short-lived signed URLs that must
	// be fetched fresh at play time. Register provider families here; channels
	// store a recipe (resolver + arg) instead of a URL.
	resolverMgr := resolver.NewManager()
	resolverMgr.Add(resolver.Exposestrat{})

	mux := newMux(proxyHandler, viewerStore, staticSub, channels, st, ks, rec, prober, resolverMgr, indexTmpl)

	// Bind before returning so a bind failure (e.g. the port is in use) is
	// reported clearly, and so Addr() is valid before Serve blocks.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("listen on %s: %w (is the port already in use?)", cfg.Addr, err)
	}

	app := &App{
		Handler:  mux,
		channels: channels,
		prober:   prober,
		st:       st,
		rec:      rec,
		ks:       ks,
		viewers:  viewerStore,
		ln:       ln,
		url:      "http://localhost" + displayAddr(cfg.Addr),
		open:     cfg.Open,
	}
	return app, cleanup, nil
}

// StartStaleProbe kicks a silent background pass over channels whose verdict is
// missing or older than healthTTL. It is a no-op when nothing is stale, so a
// restart right after a probe costs nothing; a fresh DB probes everything once.
// The server owns this (not the page), so browser refreshes just observe the
// running pass instead of each starting their own.
func (a *App) StartStaleProbe() {
	targets, err := a.st.StaleTargets(healthTTL, false)
	if err != nil {
		log.Printf("health: stale targets: %v", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	// Targets are already filtered to the stale set, so force past the prober's
	// own freshness gate.
	a.prober.Start(targets, a.channels.etagValue(), true)
	log.Printf("health: background re-check of %d stale channel(s)", len(targets))
}

// Serve runs the server until ctx is cancelled, then shuts down gracefully. It
// starts the viewer-prune loop and the stale-probe pass, serves on the listener
// bound in Build, and (if Config.Open) opens the browser once accepting.
func (a *App) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout reaps dead keep-alive connections. Deliberately NO
		// WriteTimeout: it would abort long-lived live-stream responses.
		IdleTimeout: 120 * time.Second,
	}

	// Background maintenance: prune stale viewer sessions.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.viewers.Prune()
			}
		}
	}()

	go a.StartStaleProbe()

	go func() {
		log.Printf("listening on %s", a.url)
		if err := srv.Serve(a.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	if a.open {
		openBrowser(a.url)
	}

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Run is the one-call entry point for the plain web binary: Build the server,
// defer its cleanup, and Serve until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	a, cleanup, err := Build(cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	return a.Serve(ctx)
}
