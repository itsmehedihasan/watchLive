//go:build windows

// Package native is the Windows-only shell for watchlive: it hosts the existing
// web UI in one or more top-level WebView2 windows and decodes/renders video
// with libmpv (one mpv per window, hardware HEVC via d3d11va). winapi.go holds
// the thin Win32 wrappers the window manager needs that golang.org/x/sys/windows
// does not already provide (window class, message loop, geometry, DPI). Struct
// layouts mirror the Win32 amd64 ABI.
package native

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procUpdateWindow                  = user32.NewProc("UpdateWindow")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procEnumWindows                   = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible               = user32.NewProc("IsWindowVisible")
	procGetWindow                     = user32.NewProc("GetWindow")

	procCoInitializeEx = ole32.NewProc("CoInitializeEx")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsClipChildren     = 0x02000000
	wsClipSiblings     = 0x04000000
	wsPopup            = 0x80000000

	cwUseDefault = 0x80000000

	swShow = 5
	swHide = 0

	// Window messages we handle in wndProc.
	wmDestroy    = 0x0002
	wmMove       = 0x0003
	wmSize       = 0x0005
	wmActivate   = 0x0006
	wmClose      = 0x0010
	wmDpiChanged = 0x02E0
	// wmApp is the base of the app-private message range; we use it as the
	// UI-thread dispatch wakeup (see WindowManager.dispatch).
	wmApp = 0x8000

	// WM_ACTIVATE wParam: window is being deactivated.
	waInactive = 0

	// SetWindowPos flags.
	swpNoActivate    = 0x0010
	swpNoZOrder      = 0x0004
	swpNoMove        = 0x0002
	swpNoSize        = 0x0001
	swpShowWindow    = 0x0040
	swpFrameChanged  = 0x0020
	swpNoOwnerZOrder = 0x0200

	hwndTop = 0

	// MonitorFromWindow: pick the monitor the window most overlaps.
	monitorDefaultToNearest = 0x2

	idcArrow    = 32512
	colorWindow = 5

	coinitApartmentThreaded = 0x2
)

// dpiPerMonitorAwareV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, which the
// Win32 headers define as the handle value (DPI_AWARENESS_CONTEXT)-4.
var dpiPerMonitorAwareV2 = ^uintptr(3)

// wndClassExW mirrors WNDCLASSEXW.
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// msg mirrors MSG.
type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

// rect mirrors RECT.
type rect struct {
	left, top, right, bottom int32
}

// point mirrors POINT.
type point struct {
	x, y int32
}

// monitorInfo mirrors MONITORINFO. rcMonitor is the full monitor rect in virtual
// screen coordinates; rcWork excludes the taskbar. cbSize must be set before the
// GetMonitorInfoW call.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

// must16 converts a Go string to a UTF-16 pointer for Win32 calls; class/title
// strings are compile-time constants, so a conversion error is a programmer bug.
func must16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func getModuleHandle() windows.Handle {
	r, _, _ := procGetModuleHandleW.Call(0)
	return windows.Handle(r)
}

func loadCursor(id uintptr) windows.Handle {
	r, _, _ := procLoadCursorW.Call(0, id)
	return windows.Handle(r)
}

func defWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return r
}

func setWindowPos(hwnd, insertAfter uintptr, x, y, cx, cy int32, flags uint32) {
	_, _, _ = procSetWindowPos.Call(hwnd, insertAfter,
		uintptr(x), uintptr(y), uintptr(cx), uintptr(cy), uintptr(flags))
}

func showWindow(hwnd uintptr, cmd int) {
	_, _, _ = procShowWindow.Call(hwnd, uintptr(cmd))
}

// monitorRectForWindow returns the full pixel rect of the monitor the window is
// mostly on — the area we tile mpv windows into.
func monitorRectForWindow(hwnd uintptr) rect {
	hmon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	_, _, _ = procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
	return mi.rcMonitor
}

func updateWindow(hwnd uintptr) {
	_, _, _ = procUpdateWindow.Call(hwnd)
}

func destroyWindow(hwnd uintptr) {
	_, _, _ = procDestroyWindow.Call(hwnd)
}

func postQuitMessage() {
	_, _, _ = procPostQuitMessage.Call(0)
}

// SetDPIAware opts the process into per-monitor-v2 DPI awareness so WebView2 and
// mpv render crisply and geometry math is in true physical pixels. Best-effort:
// on an OS without the API (pre-1703) it is a no-op and the process stays at its
// manifest default.
func SetDPIAware() {
	if err := procSetProcessDpiAwarenessContext.Find(); err != nil {
		return
	}
	_, _, _ = procSetProcessDpiAwarenessContext.Call(dpiPerMonitorAwareV2)
}

// CoInitializeSTA puts the calling thread into a single-threaded apartment, as
// WebView2 requires. Call once on the UI thread (which must also be locked to
// the OS thread) before creating any window.
func CoInitializeSTA() {
	_, _, _ = procCoInitializeEx.Call(0, coinitApartmentThreaded)
}

// mbIconError | mbOK for a simple error dialog.
const (
	mbOK       = 0x0
	mbIconWarn = 0x30
)

// MessageBox shows a modal Win32 dialog. Used to surface a missing-runtime
// message before any window exists, so a bare double-click still gets feedback.
func MessageBox(title, text string) {
	t, err1 := windows.UTF16PtrFromString(text)
	c, err2 := windows.UTF16PtrFromString(title)
	if err1 != nil || err2 != nil {
		return
	}
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(t)),
		uintptr(unsafe.Pointer(c)),
		uintptr(mbOK|mbIconWarn))
}

// gwOwner is GW_OWNER for GetWindow — used to keep only owner-less top-level
// windows (mpv's main window), skipping tooltip/child helper windows.
const gwOwner = 4

func isWindowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

func windowOwner(hwnd uintptr) uintptr {
	r, _, _ := procGetWindow.Call(hwnd, gwOwner)
	return r
}

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// setForeground brings a window to the front (screen-tile "focus" button).
func setForeground(hwnd uintptr) {
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// findWindowByPID walks all top-level windows and returns the first visible,
// owner-less one belonging to pid — i.e. an mpv player's main window. Returns 0
// if none is found (mpv's window may not exist yet right after launch).
//
// EnumWindows takes a C callback that can't capture Go state, so the search runs
// under enumMu with the target/result in package vars. One reusable callback
// avoids exhausting the process-wide NewCallback limit.
func findWindowByPID(pid uint32) uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumTargetPID = pid
	enumFound = 0
	_, _, _ = procEnumWindows.Call(enumCallback, 0)
	return enumFound
}

var (
	enumMu        sync.Mutex
	enumTargetPID uint32
	enumFound     uintptr
	enumCallback  = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		if enumFound != 0 {
			return 0 // stop enumerating
		}
		if windowPID(hwnd) == enumTargetPID && isWindowVisible(hwnd) && windowOwner(hwnd) == 0 {
			enumFound = hwnd
			return 0
		}
		return 1 // continue
	})
)
