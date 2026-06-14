// Package health probes channel streams to decide which are reachable from the
// server right now. A single Prober runs one probe pass at a time over a set of
// targets (one per channel), fetching each channel's stream manifest with
// spoofed browser headers — the same way the proxy fetches it for playback — so
// the verdict reflects what a real viewer on this machine would experience
// (dead hosts, 403 geo-blocks, error pages all read as not-working).
//
// Probing is manifest-only: one GET per server, first reachable server wins,
// no segment fetch. A pass over a large list (10k+ channels) takes a while, so
// results are cached and a Snapshot can be polled while a pass is in flight.
package health

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// concurrency bounds how many channels are probed at once. Each probe is a
	// single outbound GET; ~96 in flight keeps a full pass to a few minutes
	// without exhausting sockets on a desktop.
	concurrency = 96
	// probeTimeout is the per-request budget. A stream that hasn't sent a
	// response header in this long is treated as not-working.
	probeTimeout = 7 * time.Second
	// freshFor is how long a finished pass is reused before a re-probe. The
	// list's etag also gates reuse: a changed playlist always re-probes.
	freshFor = 10 * time.Minute
	// peekBytes is how much of a response body we read to tell a real playlist
	// from an HTML error page served with a 200.
	peekBytes = 4096
)

var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Accept":          "*/*",
	"Accept-Language": "en-US,en;q=0.9",
}

var m3u8URLRe = regexp.MustCompile(`(?i)\.m3u8(\?|$)`)

// Target is one channel to probe: its stable ID and its candidate stream URLs
// (the channel's servers, in preference order). The channel is alive if any one
// URL is reachable.
type Target struct {
	ID   string
	URLs []string
}

// Snapshot is an immutable view of the prober's state, safe to serialize.
type Snapshot struct {
	Running  bool            `json:"running"`  // a pass is in flight
	Finished bool            `json:"finished"` // the last pass completed
	Done     int             `json:"done"`     // channels probed so far this pass
	Total    int             `json:"total"`    // channels in this pass
	Etag     string          `json:"etag"`     // playlist version this pass is for
	Status   map[string]bool `json:"status"`   // channel ID → alive; present once probed
}

// Prober runs and caches one health pass at a time.
type Prober struct {
	client *http.Client

	mu       sync.Mutex
	gen      int             // identifies the current pass; stale workers don't write
	cancel   context.CancelFunc
	running  bool
	finished bool
	done     int
	total    int
	etag     string
	when     time.Time // when the current/last pass started
	results  map[string]bool
	path     string // on-disk cache file; "" disables persistence
}

// cacheFile is the on-disk form of a finished pass. The etag pins the verdicts
// to a specific playlist version: a different catalog has a different etag and
// the cache is ignored, so stale verdicts can never map to the wrong channels.
type cacheFile struct {
	Etag   string          `json:"etag"`
	When   time.Time       `json:"when"`
	Status map[string]bool `json:"status"`
}

// New builds a Prober with its own HTTP client tuned for many short-lived
// requests to many different hosts.
func New() *Prober {
	return &Prober{
		client: &http.Client{
			Timeout: probeTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        concurrency,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
			},
		},
		results: map[string]bool{},
	}
}

// LoadCache points the prober at an on-disk cache file and, if it holds a
// finished pass, loads those verdicts as the current (finished) state. This is
// what lets a fresh process serve health results without re-probing: the next
// Start for the same etag is reused, and a GET sees a finished pass. Best-effort
// — a missing or unreadable file just leaves the prober empty.
func (p *Prober) LoadCache(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = path

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil || c.Etag == "" || len(c.Status) == 0 {
		return
	}
	p.results = c.Status
	p.etag = c.Etag
	p.when = c.When
	p.finished = true
	p.total = len(c.Status)
	p.done = len(c.Status)
}

// Start begins a probe pass over targets unless one is already adequate:
//   - a pass for the same etag that is running or still fresh is reused as is;
//   - a pass for a different etag (the playlist changed) cancels the old one.
//
// force skips the reuse checks and always re-probes — used by the "Working
// only" toggle so flipping it off→on re-checks even a fresh, unchanged list.
//
// It returns the current snapshot immediately; callers poll Snapshot for
// progress.
func (p *Prober) Start(targets []Target, etag string, force bool) Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !force {
		if p.running && p.etag == etag {
			return p.snapshotLocked()
		}
		if p.finished && p.etag == etag && !p.when.IsZero() && time.Since(p.when) < freshFor {
			return p.snapshotLocked()
		}
	}

	// Either nothing has run, the list changed, or results went stale: cancel
	// any in-flight pass and start fresh.
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.gen++
	gen := p.gen
	p.cancel = cancel
	p.running = true
	p.finished = false
	p.done = 0
	p.total = len(targets)
	p.etag = etag
	p.when = clock()
	p.results = make(map[string]bool, len(targets))

	go p.run(ctx, gen, targets)
	return p.snapshotLocked()
}

// Snapshot returns the prober's current state.
func (p *Prober) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

func (p *Prober) snapshotLocked() Snapshot {
	status := make(map[string]bool, len(p.results))
	for id, alive := range p.results {
		status[id] = alive
	}
	return Snapshot{
		Running:  p.running,
		Finished: p.finished,
		Done:     p.done,
		Total:    p.total,
		Etag:     p.etag,
		Status:   status,
	}
}

// run probes every target with bounded concurrency, then marks the pass done.
// Only writes belonging to the current generation take effect, so a cancelled
// or superseded pass leaves no trace in the live state.
func (p *Prober) run(ctx context.Context, gen int, targets []Target) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, t := range targets {
		select {
		case <-ctx.Done():
			wg.Wait()
			return // superseded; the new pass owns the state
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			defer func() { <-sem }()
			alive := p.probe(ctx, t)
			p.mu.Lock()
			if p.gen == gen {
				p.results[t.ID] = alive
				p.done++
			}
			p.mu.Unlock()
		}(t)
	}

	wg.Wait()
	p.mu.Lock()
	var save *cacheFile
	if p.gen == gen {
		p.running = false
		p.finished = true
		p.cancel = nil
		if p.path != "" {
			status := make(map[string]bool, len(p.results))
			for id, alive := range p.results {
				status[id] = alive
			}
			save = &cacheFile{Etag: p.etag, When: p.when, Status: status}
		}
	}
	p.mu.Unlock()

	// Persist outside the lock — the write is small but disk I/O shouldn't block
	// concurrent Snapshot/Start calls. A cancelled/superseded pass saves nothing.
	if save != nil {
		writeCache(p.path, save)
	}
}

// writeCache atomically replaces the cache file with the finished pass. Best-
// effort: a failure just means the next process re-probes.
func writeCache(path string, c *cacheFile) {
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
}

// probe returns true as soon as one of the channel's servers is reachable.
func (p *Prober) probe(ctx context.Context, t Target) bool {
	for _, u := range t.URLs {
		if ctx.Err() != nil {
			return false
		}
		if p.reachable(ctx, u) {
			return true
		}
	}
	return false
}

// reachable fetches one stream URL and decides whether it's working. A working
// stream is a 2xx response that is not an HTML error page; when the URL is a
// playlist, the body must actually be one (#EXTM3U), which rejects servers that
// answer 200 with a redirect/notice page.
func (p *Prober) reachable(ctx context.Context, rawURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
	if u, err := url.Parse(rawURL); err == nil {
		origin := u.Scheme + "://" + u.Host
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Origin", origin)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.CopyN(io.Discard, resp.Body, 2048) // let the connection be reused
		return false
	}

	peek, _ := io.ReadAll(io.LimitReader(resp.Body, peekBytes))
	io.Copy(io.Discard, resp.Body)
	return looksAlive(rawURL, resp.Header.Get("Content-Type"), peek)
}

// looksAlive judges a 2xx response body. Split out for testing.
func looksAlive(rawURL, contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	trimmed := strings.TrimSpace(string(body))

	// An HTML page behind a 200 is an error/notice, not a stream.
	if strings.Contains(ct, "text/html") || strings.HasPrefix(strings.ToLower(trimmed), "<") {
		return false
	}
	// Playlists must actually be playlists.
	if m3u8URLRe.MatchString(rawURL) || strings.Contains(ct, "mpegurl") {
		return strings.Contains(string(body), "#EXTM3U")
	}
	// A non-playlist 2xx with a non-HTML body (a direct stream/segment) is fine.
	return true
}

// clock is a seam so tests can drive freshness deterministically; in
// production it is the wall clock.
var clock = time.Now
