// Command watchlive is the plain web build of the single-binary live TV server.
// It parses the same flags as before and hands them to app.Run, which builds the
// server (web UI, HLS/DASH proxy, catalog, recorder, keystore) and serves it
// until interrupted. This is a drop-in replacement for the old repo-root binary:
// the entire server now lives in the importable `app` package so the native
// WebView2 + libmpv shell (cmd/watchlive-native) can embed it in-process.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	app "watchlive"
)

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := app.Config{
		Addr:         *addr,
		DBPath:       *dbPath,
		PlaylistPath: *playlistPath,
		SourceURL:    *sourceURL,
		RecDir:       *recDir,
		NoRefresh:    *noRefresh,
		Prune:        *prune,
		Open:         *open,
		AllowPrivate: *allowPrivate,
		CacheMB:      *cacheMB,
	}
	if err := app.Run(ctx, cfg); err != nil {
		// log.Fatalf-style exit: the error already carries the failing op.
		os.Stderr.WriteString("watchlive: " + err.Error() + "\n")
		os.Exit(1)
	}
}
