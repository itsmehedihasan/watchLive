//go:build windows

package native

import (
	"encoding/json"
	"log"
	neturl "net/url"
)

// bridge.go is the wire protocol between the WebView2 remote UI and Go. One
// remote window drives many mpv windows, so every message carries the screen id
// it targets. Messages arrive as JSON strings via window.chrome.webview.
// postMessage; onMessage runs on the UI thread (from the message pump).
//
// JS→Go:  {t:"open",id} · {t:"play",id,url,referer,ua} · {t:"stop",id}
//         {t:"close",id} · {t:"audio",id} · {t:"focus",id}
// Go→JS:  window.__native.onScreen({id,state}) · window.__native.onClosed({id})

type inbound struct {
	T       string `json:"t"`
	ID      int    `json:"id"`
	URL     string `json:"url"`
	Referer string `json:"referer"`
	UA      string `json:"ua"`
}

func (wm *WindowManager) onMessage(raw string) {
	var m inbound
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		log.Printf("native: bad bridge message %q: %v", raw, err)
		return
	}
	switch m.T {
	case "open":
		wm.openScreen(m.ID)
	case "play":
		// Wrap through the loopback proxy (PNG-unwrap, per-host UA/referer, SSRF,
		// cache). The proxy injects the upstream headers itself keyed on host, so
		// mpv just fetches the proxied URL from loopback. m.URL is already the
		// resolved stream URL (the page resolves dynamic channels before play).
		proxied := wm.baseURL + "/api/proxy?url=" + neturl.QueryEscape(m.URL)
		wm.playScreen(m.ID, proxied)
	case "stop":
		wm.stopScreen(m.ID)
	case "close":
		wm.closeScreen(m.ID)
	case "audio":
		wm.audioScreen(m.ID)
	case "focus":
		wm.focusScreen(m.ID)
	default:
		log.Printf("native: unknown bridge message t=%q", m.T)
	}
}
