//go:build windows

package native

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	winio "github.com/Microsoft/go-winio"
)

// Proc is one external mpv.exe player window driven over its JSON IPC pipe. mpv
// owns its own top-level window and on-screen controls (play/pause/seek/mute/
// fullscreen); we only launch it, swap streams with `loadfile`, arbitrate audio,
// and position it (auto-tile). One Proc per screen tile, keyed by id.
type Proc struct {
	id       int
	cmd      *exec.Cmd
	onState  func(id int, state string)
	onReady  func(id int) // fired once the window HWND is discovered (for tiling)
	onClosed func(id int)

	mu   sync.Mutex
	conn net.Conn // IPC pipe; nil until connected
	hwnd uintptr  // mpv's top-level window; 0 until discovered
	done bool
}

// LaunchProc starts an mpv.exe in idle mode (a window opens immediately, ready
// for loadfile), connects to its IPC pipe, and discovers its window HWND. onState
// reports "playing"/"error" from mpv events; onClosed fires once when the process
// exits (the user closed the mpv window). Both callbacks run on background
// goroutines — the caller must marshal any UI work onto the UI thread.
func LaunchProc(mpvPath string, id int, onState func(int, string), onReady func(int), onClosed func(int)) (*Proc, error) {
	pipeName := fmt.Sprintf(`\\.\pipe\watchlive-mpv-%d-%d`, os.Getpid(), id)
	cmd := exec.Command(mpvPath,
		"--input-ipc-server="+pipeName,
		"--force-window=yes", // window appears immediately, even before a file
		"--idle=yes",         // stay open with no file loaded / after end
		"--keep-open=no",
		"--title=WatchLive "+fmt.Sprint(id),
		"--hwdec=d3d11va",       // HW HEVC 8/10/12-bit on Intel QuickSync
		"--profile=low-latency", // small caches for live streams
		"--ontop=no",
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launch mpv: %w", err)
	}

	p := &Proc{id: id, cmd: cmd, onState: onState, onReady: onReady, onClosed: onClosed}

	// Reap the process; fire onClosed exactly once when mpv exits.
	go func() {
		_ = cmd.Wait()
		p.markDone()
	}()

	// Connect to the IPC pipe (mpv creates the server a beat after launch).
	go p.connect(pipeName)

	// Discover the mpv window HWND for auto-tiling.
	go p.discoverWindow(uint32(cmd.Process.Pid))

	return p, nil
}

func (p *Proc) connect(pipeName string) {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if p.isDone() {
			return
		}
		to := 500 * time.Millisecond
		conn, err := winio.DialPipe(pipeName, &to)
		if err == nil {
			p.mu.Lock()
			p.conn = conn
			p.mu.Unlock()
			go p.readEvents(conn)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("mpv[%d]: IPC pipe never became available", p.id)
}

func (p *Proc) discoverWindow(pid uint32) {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if p.isDone() {
			return
		}
		if hwnd := findWindowByPID(pid); hwnd != 0 {
			p.mu.Lock()
			p.hwnd = hwnd
			p.mu.Unlock()
			if fn := p.onReady; fn != nil {
				fn(p.id)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("mpv[%d]: window HWND not found (auto-tile will skip it)", p.id)
}

// readEvents scans newline-delimited IPC JSON and maps the two events the UI
// cares about onto onState. mpv's own OSC is the primary interface, so this is
// best-effort status for the screen tile.
func (p *Proc) readEvents(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev struct {
			Event  string `json:"event"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "file-loaded":
			p.emit("playing")
		case "end-file":
			if ev.Reason == "error" {
				p.emit("error")
			}
		}
	}
}

func (p *Proc) emit(state string) {
	if fn := p.onState; fn != nil {
		fn(p.id, state)
	}
}

// encodeCommand marshals an mpv IPC command line: {"command":[...]}\n. args mix
// strings and scalars (e.g. ["set_property","mute",true]), so it takes any.
func encodeCommand(args ...any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{"command": args})
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

// writeCommand sends one mpv IPC command over the pipe. Commands issued before
// the pipe connects are dropped (mpv hasn't created the server yet).
func (p *Proc) writeCommand(args ...any) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return
	}
	line, err := encodeCommand(args...)
	if err != nil {
		return
	}
	if _, err := conn.Write(line); err != nil {
		log.Printf("mpv[%d]: write command: %v", p.id, err)
	}
}

// Play swaps the stream in place (replace-on-pick). url is the loopback proxy URL.
func (p *Proc) Play(url string) { p.writeCommand("loadfile", url) }

// Stop halts playback but keeps the idle window open.
func (p *Proc) Stop() { p.writeCommand("stop") }

// SetMute mutes/unmutes this window (audio arbitration).
func (p *Proc) SetMute(on bool) { p.writeCommand("set_property", "mute", on) }

// HWND returns the mpv window handle (0 until discovered).
func (p *Proc) HWND() uintptr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hwnd
}

// Focus brings the mpv window to the foreground.
func (p *Proc) Focus() {
	if h := p.HWND(); h != 0 {
		setForeground(h)
	}
}

// Close asks mpv to quit, then force-kills if it lingers. Idempotent.
func (p *Proc) Close() {
	p.writeCommand("quit")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		p.mu.Lock()
		done := p.done
		p.mu.Unlock()
		if !done && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}()
}

func (p *Proc) markDone() {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return
	}
	p.done = true
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if fn := p.onClosed; fn != nil {
		fn(p.id)
	}
}

func (p *Proc) isDone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}
