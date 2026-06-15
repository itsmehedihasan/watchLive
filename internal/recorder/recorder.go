// Package recorder captures a live stream to a 720p H.264/AAC MP4 by driving an
// ffmpeg child process. H.264 (yuv420p) + AAC in MP4 is the universally
// hardware-decodable combination, and a fragmented MP4 stays playable even if
// ffmpeg is killed mid-recording. DRM-protected streams can't be recorded:
// ffmpeg only sees encrypted segments.
package recorder

import (
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"os"
)

// Spoof a real browser so picky CDNs don't reject ffmpeg (mirrors the proxy).
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// States a recording can be in.
const (
	stateRecording = "recording"
	stateDone      = "done"
	stateError     = "error"
)

type rec struct {
	id        string
	name      string
	file      string // base filename within the recordings dir
	startedAt time.Time
	state     string
	errMsg    string

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *cappedWriter
	done    chan struct{}
	stopped bool
}

// Status is the JSON-friendly view of a recording.
type Status struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	File    string `json:"file"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Elapsed int    `json:"elapsed"` // seconds since start
}

// Recorder manages the lifecycle of ffmpeg recording processes.
type Recorder struct {
	ffmpeg string
	dir    string

	mu   sync.Mutex
	recs map[string]*rec
	seq  int
}

// New creates a recorder. ffmpegPath == "" disables recording (Available reports
// false); dir is where MP4s are written (created if missing).
func New(ffmpegPath, dir string) *Recorder {
	if dir == "" {
		dir = "recordings"
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Recorder{ffmpeg: ffmpegPath, dir: dir, recs: make(map[string]*rec)}
}

// Available reports whether recording can run (an ffmpeg binary was found).
func (r *Recorder) Available() bool { return r.ffmpeg != "" }

// Dir is the directory recordings are written to.
func (r *Recorder) Dir() string { return r.dir }

var unsafeName = regexp.MustCompile(`[^\w.-]+`)

func sanitize(name string) string {
	name = unsafeName.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_.")
	if name == "" {
		name = "recording"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

// Start launches ffmpeg to record streamURL, transcoding to 720p H.264/AAC MP4.
// now is passed in so the caller controls the timestamp. Returns the recording's
// id and output filename.
func (r *Recorder) Start(streamURL, name string, now time.Time) (id, file string, err error) {
	if r.ffmpeg == "" {
		return "", "", fmt.Errorf("recording unavailable: ffmpeg not found")
	}
	if strings.TrimSpace(streamURL) == "" {
		return "", "", fmt.Errorf("missing stream url")
	}

	file = sanitize(name) + "-" + now.Format("20060102-150405") + ".mp4"
	out := filepath.Join(r.dir, file)

	origin := ""
	if u, perr := url.Parse(streamURL); perr == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-user_agent", userAgent}
	if origin != "" {
		args = append(args, "-headers", "Referer: "+origin+"/\r\nOrigin: "+origin+"\r\n")
	}
	args = append(args,
		"-i", streamURL,
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-vf", "scale=-2:'min(720,ih)'", // 720p cap, never upscale, keep aspect
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof", // resilient to a hard kill
		"-f", "mp4", "-y", out,
	)

	cmd := exec.Command(r.ffmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", "", err
	}
	stderr := &cappedWriter{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start ffmpeg: %w", err)
	}

	r.mu.Lock()
	r.seq++
	id = fmt.Sprintf("rec%d", r.seq)
	rc := &rec{
		id: id, name: name, file: file, startedAt: now, state: stateRecording,
		cmd: cmd, stdin: stdin, stderr: stderr, done: make(chan struct{}),
	}
	r.recs[id] = rc
	r.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		r.mu.Lock()
		switch {
		case rc.stopped:
			rc.state = stateDone // user-requested stop ('q') — clean finalize
		case waitErr != nil:
			rc.state = stateError
			rc.errMsg = tail(stderr.String())
		default:
			rc.state = stateDone // stream ended on its own
		}
		r.mu.Unlock()
		close(rc.done)
	}()

	return id, file, nil
}

// Stop asks ffmpeg to finish (writing 'q' so the MP4 is finalized cleanly), then
// force-kills if it doesn't exit promptly. Blocks until the process is gone.
func (r *Recorder) Stop(id string) (file string, err error) {
	r.mu.Lock()
	rc := r.recs[id]
	if rc == nil {
		r.mu.Unlock()
		return "", fmt.Errorf("unknown recording")
	}
	file = rc.file
	if rc.state != stateRecording {
		r.mu.Unlock()
		return file, nil
	}
	rc.stopped = true
	stdin, cmd, done := rc.stdin, rc.cmd, rc.done
	r.mu.Unlock()

	io.WriteString(stdin, "q\n")
	stdin.Close()

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	return file, nil
}

// StopAll stops every active recording (used on server shutdown).
func (r *Recorder) StopAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.recs))
	for id, rc := range r.recs {
		if rc.state == stateRecording {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Stop(id)
	}
}

// Status returns every recording (active and finished), newest first.
func (r *Recorder) Status(now time.Time) []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.recs))
	for _, rc := range r.recs {
		out = append(out, Status{
			ID: rc.id, Name: rc.name, File: rc.file, State: rc.state, Error: rc.errMsg,
			Elapsed: int(now.Sub(rc.startedAt).Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// cappedWriter keeps only the first max bytes — enough to surface an ffmpeg
// error without unbounded memory if a stream spews warnings.
type cappedWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.max - len(w.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		w.buf = append(w.buf, p[:room]...)
	}
	return len(p), nil
}

func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = "…" + s[len(s)-300:]
	}
	if s == "" {
		s = "ffmpeg failed (no output captured)"
	}
	return s
}
