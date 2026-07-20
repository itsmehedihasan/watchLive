//go:build windows

package native

import (
	"log"
	"sort"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// className is the Win32 class for the single remote window.
const className = "WatchLiveNativeWindow"

// wndProcCallback is the C-callable trampoline registered on the class.
var wndProcCallback = windows.NewCallback(wndProc)

// remote is the single WindowManager. wndProc is a C callback that can't capture
// Go state, and there is exactly one native window, so a package global is the
// simplest bridge from the callback back to the manager.
var remote *WindowManager

// WindowManager owns the one WebView2 "remote" window and the set of external
// mpv player windows (Procs) it drives. Screen tile ↔ Proc is 1:1, keyed by id.
type WindowManager struct {
	baseURL string // in-process server, for building proxy URLs
	mpvPath string // resolved mpv.exe

	web        *Host
	hwnd       uintptr
	hinst      windows.Handle
	registered bool

	mu        sync.Mutex
	procs     map[int]*Proc
	dispatchQ []func() // UI-thread closures (see dispatch)
}

// NewWindowManager creates the manager. baseURL points the WebView2 at the
// in-process server; mpvPath is the mpv.exe the Procs launch.
func NewWindowManager(baseURL, mpvPath string) *WindowManager {
	return &WindowManager{
		baseURL: baseURL,
		mpvPath: mpvPath,
		procs:   map[int]*Proc{},
	}
}

func (wm *WindowManager) ensureClass() {
	if wm.registered {
		return
	}
	wm.hinst = getModuleHandle()
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProcCallback,
		hInstance:     wm.hinst,
		hCursor:       loadCursor(idcArrow),
		hbrBackground: windows.Handle(colorWindow + 1),
		lpszClassName: must16(className),
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		log.Printf("native: RegisterClassEx: %v", err)
	}
	wm.registered = true
}

// Open creates the remote window, embeds the WebView2, and navigates it to the
// UI. Must run on the UI thread. Returns an error if WebView2 embedding fails
// (missing runtime).
func (wm *WindowManager) Open() error {
	remote = wm
	wm.ensureClass()

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(must16(className))),
		uintptr(unsafe.Pointer(must16("WatchLive"))),
		wsOverlappedWindow|wsClipChildren,
		cwUseDefault, cwUseDefault, 1200, 800,
		0, 0, uintptr(wm.hinst), 0,
	)
	if hwnd == 0 {
		return errNative("CreateWindowEx failed")
	}
	wm.hwnd = hwnd
	showWindow(hwnd, swShow)
	updateWindow(hwnd)

	wm.web = NewHost()
	wm.web.OnMessage(func(m string) { wm.onMessage(m) })
	if !wm.web.Embed(hwnd) {
		return errNative("WebView2 embed failed (is the Runtime installed?)")
	}
	wm.web.Resize()
	wm.web.Navigate(wm.baseURL)
	return nil
}

type errNative string

func (e errNative) Error() string { return string(e) }

// Loop runs the single message pump. Returns when the window closes (WM_QUIT).
func (wm *WindowManager) Loop() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if m.message == wmApp {
			wm.drainDispatch()
			continue
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	wm := remote
	if wm == nil || hwnd != wm.hwnd {
		return defWindowProc(hwnd, uint32(message), wparam, lparam)
	}
	switch message {
	case wmSize:
		if wm.web != nil {
			wm.web.Resize()
		}
	case wmMove:
		if wm.web != nil {
			wm.web.NotifyMoved()
		}
	case wmClose:
		destroyWindow(hwnd)
		return 0
	case wmDestroy:
		wm.shutdown()
		postQuitMessage()
		return 0
	}
	return defWindowProc(hwnd, uint32(message), wparam, lparam)
}

// shutdown closes every mpv window when the remote window is destroyed.
func (wm *WindowManager) shutdown() {
	wm.mu.Lock()
	procs := make([]*Proc, 0, len(wm.procs))
	for _, p := range wm.procs {
		procs = append(procs, p)
	}
	wm.procs = map[int]*Proc{}
	wm.mu.Unlock()
	for _, p := range procs {
		p.Close()
	}
}

// --- Screen (Proc) management. All called on the UI thread (from onMessage or
// drained dispatch), so map access + retile stay on one thread. ---

// openScreen launches an mpv window for id (idempotent).
func (wm *WindowManager) openScreen(id int) {
	wm.mu.Lock()
	_, exists := wm.procs[id]
	wm.mu.Unlock()
	if exists {
		return
	}
	if wm.mpvPath == "" {
		wm.pushState(id, "error")
		return
	}
	p, err := LaunchProc(wm.mpvPath, id,
		func(sid int, st string) { wm.dispatch(func() { wm.pushState(sid, st) }) },
		func(sid int) { wm.dispatch(func() { wm.retile() }) },
		func(sid int) { wm.dispatch(func() { wm.onScreenClosed(sid) }) },
	)
	if err != nil {
		log.Printf("native: open screen %d: %v", id, err)
		wm.pushState(id, "error")
		return
	}
	wm.mu.Lock()
	wm.procs[id] = p
	wm.mu.Unlock()
	wm.retile()
}

func (wm *WindowManager) proc(id int) *Proc {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.procs[id]
}

func (wm *WindowManager) playScreen(id int, url string) {
	if p := wm.proc(id); p != nil {
		p.Play(url)
	}
}

func (wm *WindowManager) stopScreen(id int) {
	if p := wm.proc(id); p != nil {
		p.Stop()
	}
}

func (wm *WindowManager) closeScreen(id int) {
	if p := wm.proc(id); p != nil {
		p.Close() // process exit → onClosed → onScreenClosed removes + retiles
	}
}

func (wm *WindowManager) focusScreen(id int) {
	if p := wm.proc(id); p != nil {
		p.Focus()
	}
}

// audioScreen unmutes id and mutes every other mpv window (single-audio model).
func (wm *WindowManager) audioScreen(id int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for pid, p := range wm.procs {
		p.SetMute(pid != id)
	}
}

func (wm *WindowManager) onScreenClosed(id int) {
	wm.mu.Lock()
	_, ok := wm.procs[id]
	delete(wm.procs, id)
	wm.mu.Unlock()
	if !ok {
		return
	}
	wm.retile()
	if wm.web != nil {
		wm.web.Eval("window.__native&&window.__native.onClosed&&window.__native.onClosed({id:" + itoa(id) + "})")
	}
}

// retile positions all discovered mpv windows into a grid on the remote window's
// monitor. Procs whose HWND isn't known yet are skipped (retile re-runs via the
// onReady callback once discovered).
func (wm *WindowManager) retile() {
	wm.mu.Lock()
	ids := make([]int, 0, len(wm.procs))
	for id := range wm.procs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	hwnds := make([]uintptr, 0, len(ids))
	for _, id := range ids {
		if h := wm.procs[id].HWND(); h != 0 {
			hwnds = append(hwnds, h)
		}
	}
	wm.mu.Unlock()

	if len(hwnds) == 0 {
		return
	}
	area := monitorRectForWindow(wm.hwnd)
	rects := tileRects(area, len(hwnds))
	for i, h := range hwnds {
		if i >= len(rects) {
			break
		}
		r := rects[i]
		setWindowPos(h, hwndTop, r.left, r.top, r.right-r.left, r.bottom-r.top, swpNoActivate)
	}
}

// tileRects splits area into n non-overlapping rects: 1→full, 2→halves,
// 3→two top + one bottom, 4→quadrants. n is clamped to 4 (the screen cap).
func tileRects(area rect, n int) []rect {
	w := area.right - area.left
	h := area.bottom - area.top
	x, y := area.left, area.top
	mk := func(cx, cy, cw, ch int32) rect { return rect{left: cx, top: cy, right: cx + cw, bottom: cy + ch} }
	switch {
	case n <= 0:
		return nil
	case n == 1:
		return []rect{mk(x, y, w, h)}
	case n == 2:
		return []rect{mk(x, y, w/2, h), mk(x+w/2, y, w-w/2, h)}
	case n == 3:
		return []rect{mk(x, y, w/2, h/2), mk(x+w/2, y, w-w/2, h/2), mk(x, y+h/2, w, h-h/2)}
	default: // 4 (and any n>4 clamped to a 2×2)
		return []rect{
			mk(x, y, w/2, h/2), mk(x+w/2, y, w-w/2, h/2),
			mk(x, y+h/2, w/2, h-h/2), mk(x+w/2, y+h/2, w-w/2, h-h/2),
		}
	}
}

// pushState sends an mpv state change for a screen to the UI. Runs on the UI
// thread (dispatched).
func (wm *WindowManager) pushState(id int, state string) {
	if wm.web == nil {
		return
	}
	wm.web.Eval(`window.__native&&window.__native.onScreen&&window.__native.onScreen({id:` + itoa(id) + `,state:"` + state + `"})`)
}

// dispatch queues fn to run on the UI thread and wakes the message loop.
func (wm *WindowManager) dispatch(fn func()) {
	wm.mu.Lock()
	wm.dispatchQ = append(wm.dispatchQ, fn)
	target := wm.hwnd
	wm.mu.Unlock()
	if target != 0 {
		_, _, _ = procPostMessageW.Call(target, wmApp, 0, 0)
	}
}

func (wm *WindowManager) drainDispatch() {
	wm.mu.Lock()
	q := wm.dispatchQ
	wm.dispatchQ = nil
	wm.mu.Unlock()
	for _, fn := range q {
		fn()
	}
}

// itoa is a tiny int→string for building the fixed Eval snippets (avoids pulling
// strconv into the hot path and keeps the JS literal obviously integer-only).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
