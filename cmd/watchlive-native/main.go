//go:build windows

// Command watchlive-native is the native Windows shell: it runs the full
// watchlive server in-process on a loopback port and shows the web UI in a
// single WebView2 "remote" window. Video is played by external mpv.exe windows
// (one per screen tile) that the remote drives over mpv's JSON IPC pipe and
// auto-tiles. mpv is the media player (its own on-screen controls); this shell
// is the launcher/remote. The Go backend (proxy, resolve, store, recorder,
// keystore) is reused unchanged from the app package.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	app "watchlive"
	"watchlive/internal/native"

	"github.com/jchv/go-webview2/webviewloader"
)

const (
	webview2Download = "https://developer.microsoft.com/microsoft-edge/webview2/"
	mpvDownload      = "https://mpv.io/installation/"
	// pinnedAddr keeps a stable WebView2 origin across launches so the UI's
	// localStorage preferences persist (an ephemeral port would change the origin
	// every run). Falls back to an OS-chosen port if this one is taken.
	pinnedAddr = "127.0.0.1:37641"
)

func main() {
	// WebView2 and the Win32 message loop are single-threaded-apartment: the UI
	// thread must stay the same OS thread for the process lifetime.
	runtime.LockOSThread()
	native.SetDPIAware()
	native.CoInitializeSTA()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Fail early and legibly if the WebView2 Runtime is absent (ships with Win10
	// 22H2, but verify rather than crash mid-init).
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		native.MessageBox("WatchLive — WebView2 Runtime required",
			"The Microsoft Edge WebView2 Runtime is not installed.\n\n"+
				"Install the Evergreen Runtime from:\n"+webview2Download+"\n\nthen relaunch WatchLive.")
		log.Fatalf("watchlive-native: WebView2 Runtime not found: %v", err)
	}

	// Resolve the mpv.exe player (bundled beside the exe, or on PATH).
	mpvPath := native.ResolveMPV()
	if mpvPath == "" {
		native.MessageBox("WatchLive — mpv player required",
			"mpv.exe was not found next to WatchLive or on PATH.\n\n"+
				"Run fetch-mpv.ps1 to download it, or install mpv from:\n"+mpvDownload)
		log.Fatalf("watchlive-native: mpv.exe not found")
	}
	log.Printf("watchlive-native: using mpv at %s", mpvPath)

	a, cleanup, err := buildServer()
	if err != nil {
		log.Fatalf("watchlive-native: build server: %v", err)
	}
	defer cleanup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := a.Serve(ctx); err != nil {
			log.Printf("watchlive-native: serve: %v", err)
		}
	}()

	baseURL := "http://" + a.Addr().String()
	if !waitReady(a.Addr().String(), 5*time.Second) {
		log.Printf("watchlive-native: server not ready in time, proceeding anyway (%s)", baseURL)
	}
	log.Printf("watchlive-native: UI at %s", baseURL)

	wm := native.NewWindowManager(baseURL, mpvPath)
	if err := wm.Open(); err != nil {
		native.MessageBox("WatchLive — failed to start", err.Error())
		log.Fatalf("watchlive-native: open window: %v", err)
	}
	wm.Loop() // returns when the remote window closes

	stop() // cancel the server context so Serve shuts down cleanly
}

// buildServer starts the in-process server, preferring the pinned loopback port
// (stable origin) and falling back to an OS-chosen ephemeral port if it's taken.
func buildServer() (*app.App, func() error, error) {
	cfg := app.Config{Addr: pinnedAddr, Open: false, CacheMB: 200, RecDir: "recordings"}
	a, cleanup, err := app.Build(cfg)
	if err != nil {
		log.Printf("watchlive-native: pinned port %s unavailable (%v); using an ephemeral port", pinnedAddr, err)
		cfg.Addr = "127.0.0.1:0"
		a, cleanup, err = app.Build(cfg)
	}
	return a, cleanup, err
}

// waitReady blocks until the listener accepts a TCP connection or times out.
func waitReady(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
