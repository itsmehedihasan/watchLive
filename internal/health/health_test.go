package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLooksAlive(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ct   string
		body string
		want bool
	}{
		{"m3u8 with marker", "http://x/stream.m3u8", "application/vnd.apple.mpegurl", "#EXTM3U\n#EXT-X-VERSION:3", true},
		{"m3u8 url, no marker", "http://x/stream.m3u8", "", "not a playlist", false},
		{"m3u8 marker via content-type", "http://x/stream", "application/x-mpegurl", "#EXTM3U", true},
		{"html error page 200", "http://x/stream.m3u8", "text/html; charset=utf-8", "<html>blocked</html>", false},
		{"html-ish body, no content-type", "http://x/stream.m3u8", "", "<!DOCTYPE html>", false},
		{"direct stream, non-html", "http://x/live/123", "video/mp2t", "\x47\x40\x00", true},
		{"m3u8 url but html body", "http://x/a.m3u8", "", "<html></html>", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksAlive(c.url, c.ct, []byte(c.body)); got != c.want {
				t.Errorf("looksAlive(%q, %q, %q) = %v, want %v", c.url, c.ct, c.body, got, c.want)
			}
		})
	}
}

// waitFinished polls the prober until a pass completes or the deadline passes.
func waitFinished(t *testing.T, p *Prober) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := p.Snapshot()
		if snap.Finished && !snap.Running {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("probe did not finish within deadline")
	return Snapshot{}
}

func TestProberVerdicts(t *testing.T) {
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n"))
	}))
	defer alive.Close()

	geoBlocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer geoBlocked.Close()

	htmlNotice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>stream offline</html>"))
	}))
	defer htmlNotice.Close()

	targets := []Target{
		{ID: "alive", URLs: []string{alive.URL + "/stream.m3u8"}},
		{ID: "blocked", URLs: []string{geoBlocked.URL + "/stream.m3u8"}},
		{ID: "notice", URLs: []string{htmlNotice.URL + "/stream.m3u8"}},
		{ID: "failover", URLs: []string{geoBlocked.URL + "/dead.m3u8", alive.URL + "/ok.m3u8"}}, // first dead, second alive
		{ID: "none", URLs: nil}, // no servers → dead
	}

	p := New()
	p.Start(targets, "etag-1", false)
	snap := waitFinished(t, p)

	if snap.Total != len(targets) {
		t.Errorf("Total = %d, want %d", snap.Total, len(targets))
	}
	if snap.Done != len(targets) {
		t.Errorf("Done = %d, want %d", snap.Done, len(targets))
	}
	want := map[string]bool{"alive": true, "blocked": false, "notice": false, "failover": true, "none": false}
	for id, w := range want {
		got, ok := snap.Status[id]
		if !ok {
			t.Errorf("status for %q missing", id)
			continue
		}
		if got != w {
			t.Errorf("status[%q] = %v, want %v", id, got, w)
		}
	}
}

func TestProberReusesUntilEtagChanges(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U"))
	}))
	defer srv.Close()

	targets := []Target{{ID: "a", URLs: []string{srv.URL + "/a.m3u8"}}}

	p := New()
	p.Start(targets, "v1", false)
	waitFinished(t, p)
	if hits != 1 {
		t.Fatalf("after first pass: hits = %d, want 1", hits)
	}

	// Same etag while fresh → reuse, no new fetch.
	p.Start(targets, "v1", false)
	waitFinished(t, p)
	if hits != 1 {
		t.Errorf("same-etag Start re-probed: hits = %d, want 1", hits)
	}

	// force=true re-probes even the same, fresh etag (the toggle off→on path).
	p.Start(targets, "v1", true)
	waitFinished(t, p)
	if hits != 2 {
		t.Errorf("forced Start did not re-probe: hits = %d, want 2", hits)
	}

	// Changed etag (playlist re-synced) → re-probe.
	p.Start(targets, "v2", false)
	waitFinished(t, p)
	if hits != 3 {
		t.Errorf("changed-etag Start did not re-probe: hits = %d, want 3", hits)
	}
}

// OnFinish fires once a pass completes, carrying the verdicts; Seed restores a
// prior pass so a fresh Prober reports finished and reuses it without probing.
func TestProberOnFinishAndSeed(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U"))
	}))
	defer srv.Close()

	targets := []Target{{ID: "a", URLs: []string{srv.URL + "/a.m3u8"}}}

	// First process: probe once; OnFinish captures the verdicts (as the store
	// would, to persist them).
	var saved map[string]bool
	p1 := New()
	p1.OnFinish(func(v map[string]bool, _ time.Time) { saved = v })
	p1.Start(targets, "v1", false)
	waitFinished(t, p1)
	if hits != 1 {
		t.Fatalf("first pass: hits = %d, want 1", hits)
	}
	if saved == nil || !saved["a"] {
		t.Fatalf("OnFinish verdicts = %+v, want a=true", saved)
	}

	// Second process: seed from the saved verdicts — finished, no probe.
	p2 := New()
	p2.Seed("v1", saved, time.Now())
	snap := p2.Snapshot()
	if !snap.Finished || snap.Etag != "v1" || !snap.Status["a"] {
		t.Fatalf("seeded snapshot = %+v, want finished v1 with a=true", snap)
	}
	// Same etag → reuse the seeded verdicts, still no new fetch.
	p2.Start(targets, "v1", false)
	waitFinished(t, p2)
	if hits != 1 {
		t.Errorf("seeded reuse re-probed: hits = %d, want 1", hits)
	}
}

// A probe for a channel carrying #EXTVLCOPT hints sends that exact UA/referer
// (with Origin realigned), so a strict-CDN channel is judged with the same
// headers playback will use — not the defaults.
func TestProbeAppliesHeaderHints(t *testing.T) {
	var ua, ref, origin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, ref, origin = r.Header.Get("User-Agent"), r.Header.Get("Referer"), r.Header.Get("Origin")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U"))
	}))
	defer srv.Close()

	targets := []Target{{
		ID:        "a",
		URLs:      []string{srv.URL + "/a.m3u8"},
		UserAgent: "CustomUA/Pixel7",
		Referer:   "https://embed.example.com/watch",
	}}
	p := New()
	p.Start(targets, "v1", false)
	waitFinished(t, p)

	if ua != "CustomUA/Pixel7" {
		t.Errorf("probe UA override not applied: %q", ua)
	}
	if ref != "https://embed.example.com/watch" {
		t.Errorf("probe referer override not applied: %q", ref)
	}
	if origin != "https://embed.example.com" {
		t.Errorf("probe Origin not realigned to referer origin: %q", origin)
	}
	if !p.Snapshot().Status["a"] {
		t.Error("channel should be judged alive")
	}
}
