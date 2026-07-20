//go:build windows

package native

import (
	"github.com/jchv/go-webview2/pkg/edge"
)

// Host wraps a single WebView2 controller (edge.Chromium) hosted inside a parent
// HWND. One Host per window renders the real web UI; the Go↔JS bridge rides on
// its message channel. Multiple Hosts coexist in one process over a shared
// WebView2 browser process — one controller each.
type Host struct {
	chromium *edge.Chromium
}

// NewHost creates an unembedded WebView2 wrapper. Call OnMessage before Embed so
// the JS→Go channel is wired before the page can post anything, then Embed.
func NewHost() *Host {
	return &Host{chromium: edge.NewChromium()}
}

// OnMessage registers the handler for strings the page sends via
// window.chrome.webview.postMessage. The window manager passes a closure that
// captures the owning *Window, so message→window identity is implicit — the wire
// protocol carries no window id.
func (h *Host) OnMessage(fn func(string)) {
	h.chromium.MessageCallback = fn
}

// Embed creates the WebView2 environment + controller as a child of hwnd. It
// blocks (pumping the thread message queue) until the controller is ready, so
// after it returns true, Navigate/Resize/Eval are safe. Returns false if the
// WebView2 runtime is missing or environment creation fails.
func (h *Host) Embed(hwnd uintptr) bool {
	return h.chromium.Embed(hwnd)
}

// Resize makes the WebView2 controller fill the parent window's client rect.
// Call on WM_SIZE and after Embed. mpv's child HWND sits on top of this over the
// video rect (the accepted airspace tradeoff), so the web overlays there are
// occluded, but the rest of the UI is the real page at full size.
func (h *Host) Resize() {
	h.chromium.Resize()
}

// Navigate points the WebView2 at url (the in-process server's base URL).
func (h *Host) Navigate(url string) {
	h.chromium.Navigate(url)
}

// Eval runs JavaScript in the page. Used to push mpv state changes back to the
// UI (window.__native.onMpv(...)).
func (h *Host) Eval(js string) {
	h.chromium.Eval(js)
}

// NotifyMoved tells WebView2 the host window moved, so it repositions any
// out-of-window UI (e.g. autofill popups). Call on WM_MOVE.
func (h *Host) NotifyMoved() {
	_ = h.chromium.NotifyParentWindowPositionChanged()
}

// Focus gives keyboard focus to the WebView2. Call when the window activates.
func (h *Host) Focus() {
	h.chromium.Focus()
}
