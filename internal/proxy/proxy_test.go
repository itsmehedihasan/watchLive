package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRewritePlaylist(t *testing.T) {
	target := "https://cdn.example.com/tv/ch1/index.m3u8"
	in := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x1234`,
		`#EXT-X-MAP:URI="/init/init.mp4"`,
		"#EXTINF:6.0,",
		"seg001.ts",
		"#EXTINF:6.0,",
		"/abs/seg002.ts",
		"#EXTINF:6.0,",
		"//other.example.com/seg003.ts",
		"#EXTINF:6.0,",
		"https://full.example.com/seg004.ts",
		"",
	}, "\n")

	out := RewritePlaylist(in, target)
	lines := strings.Split(out, "\n")

	want := map[int]string{
		5:  "/api/proxy?url=" + url.QueryEscape("https://cdn.example.com/tv/ch1/seg001.ts"),
		7:  "/api/proxy?url=" + url.QueryEscape("https://cdn.example.com/abs/seg002.ts"),
		9:  "/api/proxy?url=" + url.QueryEscape("https://other.example.com/seg003.ts"),
		11: "/api/proxy?url=" + url.QueryEscape("https://full.example.com/seg004.ts"),
	}
	for idx, expected := range want {
		if lines[idx] != expected {
			t.Errorf("line %d:\n got  %q\n want %q", idx, lines[idx], expected)
		}
	}

	// URI attributes inside tags must be rewritten too.
	keyWant := `URI="/api/proxy?url=` + url.QueryEscape("https://cdn.example.com/tv/ch1/key.bin") + `"`
	if !strings.Contains(lines[2], keyWant) {
		t.Errorf("EXT-X-KEY not rewritten:\n got  %q\n want substring %q", lines[2], keyWant)
	}
	mapWant := `URI="/api/proxy?url=` + url.QueryEscape("https://cdn.example.com/init/init.mp4") + `"`
	if !strings.Contains(lines[3], mapWant) {
		t.Errorf("EXT-X-MAP not rewritten:\n got  %q\n want substring %q", lines[3], mapWant)
	}

	// Comment-only lines without URI stay untouched.
	if lines[0] != "#EXTM3U" || lines[1] != "#EXT-X-VERSION:3" {
		t.Errorf("comment lines modified: %q, %q", lines[0], lines[1])
	}
}

func TestProxySegmentCachingAndSingleflight(t *testing.T) {
	// Uses an fMP4 (.m4s) segment: the buffered/single-flight path. Raw .ts URLs
	// are handled separately by serveTS (see TestProxyRawTSStreaming) and do not
	// single-flight, so they are deliberately not exercised here.
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		time.Sleep(50 * time.Millisecond) // widen the race window for concurrent requests
		w.Header().Set("Content-Type", "video/mp4")
		fmt.Fprint(w, "SEGMENTDATA")
	}))
	defer upstream.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback
	segURL := upstream.URL + "/seg001.m4s"

	// 8 concurrent requests for the same segment → exactly one upstream fetch.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(segURL), nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != "SEGMENTDATA" {
				t.Errorf("bad response: %d %q", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Errorf("expected 1 upstream hit (singleflight), got %d", got)
	}

	// A later request must be a cache HIT with no new upstream fetch.
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(segURL), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("expected X-Cache HIT, got %q", rec.Header().Get("X-Cache"))
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Errorf("cache HIT still reached upstream: %d hits", got)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestProxyPlaylistRewriteEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n")
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback
	plURL := upstream.URL + "/live/index.m3u8"
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(plURL), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	wantSeg := "/api/proxy?url=" + url.QueryEscape(upstream.URL+"/live/seg1.ts")
	if !strings.Contains(rec.Body.String(), wantSeg) {
		t.Errorf("playlist not rewritten:\n%s\nwant substring %q", rec.Body.String(), wantSeg)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("content type %q", ct)
	}
}

// TestProxyPlaylistRewriteAfterRedirect covers the Xtream pattern: the manifest
// URL 302-redirects from an entry host to a tokenized edge host, and the edge's
// playlist body has root-relative segment paths. Those must be resolved against
// the EDGE (the final URL after redirects), not the originally-requested entry
// host — otherwise every segment points at the entry host and 401s.
func TestProxyPlaylistRewriteAfterRedirect(t *testing.T) {
	// Edge host: serves the real (redirected) media playlist with a root-relative
	// tokenized segment path.
	edgeMux := http.NewServeMux()
	edgeMux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:6.0,\n/hlsr/TOKEN123/seg1.ts\n")
	})
	edge := httptest.NewServer(edgeMux)
	defer edge.Close()

	// Entry host: 302-redirects the manifest request to the edge, preserving the
	// path (the token would normally ride the query string).
	entryMux := http.NewServeMux()
	entryMux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, edge.URL+"/live/index.m3u8?token=abc", http.StatusFound)
	})
	entry := httptest.NewServer(entryMux)
	defer entry.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback

	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(entry.URL+"/live/index.m3u8"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// The root-relative /hlsr/TOKEN123/seg1.ts must resolve against the EDGE host,
	// not the entry host.
	wantSeg := "/api/proxy?url=" + url.QueryEscape(edge.URL+"/hlsr/TOKEN123/seg1.ts")
	if !strings.Contains(rec.Body.String(), wantSeg) {
		t.Errorf("segment resolved against wrong host:\n%s\nwant substring %q", rec.Body.String(), wantSeg)
	}
	// It must NOT resolve against the entry host.
	badSeg := "/api/proxy?url=" + url.QueryEscape(entry.URL+"/hlsr/TOKEN123/seg1.ts")
	if strings.Contains(rec.Body.String(), badSeg) {
		t.Errorf("segment wrongly resolved against entry host:\n%s", rec.Body.String())
	}
}

func TestProxyDASHBaseURLInjected(t *testing.T) {
	const manifest = `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation><BaseURL>video/</BaseURL>
<SegmentTemplate media="seg-$Number$.m4s" initialization="init.mp4"/>
</Representation></AdaptationSet></Period></MPD>`
	mux := http.NewServeMux()
	mux.HandleFunc("/live/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, manifest)
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(upstream.URL+"/live/manifest.mpd"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// An absolute BaseURL (the manifest's own directory) must be injected as the
	// first child of <MPD> so the client resolves relative segment URLs against
	// the real origin, not the /api/proxy URL it fetched the manifest through.
	want := "<MPD><BaseURL>" + upstream.URL + "/live/</BaseURL>"
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("expected injected BaseURL %q in:\n%s", want, rec.Body.String())
	}
	// The original relative BaseURL below must be preserved (it chains under the
	// injected absolute one).
	if !strings.Contains(rec.Body.String(), "<BaseURL>video/</BaseURL>") {
		t.Errorf("original relative BaseURL was lost:\n%s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/dash+xml" {
		t.Errorf("content type %q, want application/dash+xml", ct)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-cache") {
		t.Errorf("DASH manifest must not be browser-cached: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestInjectDASHBaseURL(t *testing.T) {
	cases := []struct {
		name, mpd, url, wantContains string
	}{
		{
			name:         "attributes on MPD tag",
			mpd:          `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic"><Period/></MPD>`,
			url:          "https://cdn.example.com/a/b/index.mpd",
			wantContains: `type="dynamic"><BaseURL>https://cdn.example.com/a/b/</BaseURL><Period/>`,
		},
		{
			name:         "query on manifest url is dropped from base",
			mpd:          `<MPD><Period/></MPD>`,
			url:          "https://cdn.example.com/x/index.mpd?token=abc",
			wantContains: `<BaseURL>https://cdn.example.com/x/</BaseURL>`,
		},
		{
			name:         "ampersand in base is escaped",
			mpd:          `<MPD></MPD>`,
			url:          "https://cdn.example.com/p&q/index.mpd",
			wantContains: `<BaseURL>https://cdn.example.com/p&amp;q/</BaseURL>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := injectDASHBaseURL(c.mpd, c.url)
			if !strings.Contains(got, c.wantContains) {
				t.Errorf("injectDASHBaseURL = %q, want it to contain %q", got, c.wantContains)
			}
		})
	}

	// A document with no <MPD> tag is returned unchanged.
	const notMPD = `<xml>nope</xml>`
	if got := injectDASHBaseURL(notMPD, "https://x/y.mpd"); got != notMPD {
		t.Errorf("non-MPD doc was modified: %q", got)
	}
}

func TestProxyRawTSStreaming(t *testing.T) {
	// Continuous live stream: no Content-Length (chunked) → streamed through.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/MP2T")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprint(w, "CHUNK")
			if fl != nil {
				fl.Flush() // forces chunked transfer-encoding (no Content-Length)
			}
		}
	}))
	defer live.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(live.URL+"/channel.ts"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "CHUNKCHUNKCHUNK" {
		t.Fatalf("bad stream response: %d %q", rec.Code, rec.Body.String())
	}
	if x := rec.Header().Get("X-Cache"); x != "STREAM" {
		t.Errorf("expected X-Cache STREAM, got %q", x)
	}

	// Finite .ts (Content-Length set) is buffered and cached instead.
	var hits int32
	finite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "video/MP2T")
		fmt.Fprint(w, "SEGDATA") // small body → Content-Length set, no flush
	}))
	defer finite.Close()

	segURL := finite.URL + "/seg.ts"
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(segURL), nil)
		rc := httptest.NewRecorder()
		h.ServeHTTP(rc, r)
		if rc.Body.String() != "SEGDATA" {
			t.Fatalf("finite .ts body %q", rc.Body.String())
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("finite .ts not cached: %d upstream hits, want 1", got)
	}
}

func TestMediaSegments(t *testing.T) {
	target := "https://cdn.example.com/tv/ch1/index.m3u8"

	media := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		`#EXT-X-MAP:URI="init.mp4"`,
		"#EXTINF:6.0,",
		"seg001.ts",
		"#EXTINF:6.0,",
		"/abs/seg002.ts",
		"#EXTINF:6.0,",
		"https://full.example.com/seg003.ts",
		"",
	}, "\n")
	// Init map first (it is needed before any media), then segments in order.
	want := []string{
		"https://cdn.example.com/tv/ch1/init.mp4",
		"https://cdn.example.com/tv/ch1/seg001.ts",
		"https://cdn.example.com/abs/seg002.ts",
		"https://full.example.com/seg003.ts",
	}
	if got := mediaSegments(media, target); !reflect.DeepEqual(got, want) {
		t.Errorf("mediaSegments:\n got  %v\n want %v", got, want)
	}

	// A master playlist lists renditions, not segments — nothing to prefetch.
	master := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=1280x720`,
		"v720/index.m3u8",
		`#EXT-X-STREAM-INF:BANDWIDTH=480000,RESOLUTION=640x360`,
		"v360/index.m3u8",
		"",
	}, "\n")
	if got := mediaSegments(master, target); got != nil {
		t.Errorf("master playlist should yield no prefetch targets, got %v", got)
	}
}

func TestProxyPrefetchWarmsSegments(t *testing.T) {
	var segHits int32
	seg := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&segHits, 1)
		w.Header().Set("Content-Type", "video/MP2T")
		fmt.Fprint(w, "SEGDATA") // small body → Content-Length set (finite, cacheable)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n#EXTINF:6.0,\nseg2.ts\n")
	})
	mux.HandleFunc("/live/seg1.ts", seg)
	mux.HandleFunc("/live/seg2.ts", seg)
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback

	// Serving the media playlist must schedule prefetch of both segments.
	plReq := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(upstream.URL+"/live/index.m3u8"), nil)
	h.ServeHTTP(httptest.NewRecorder(), plReq)

	seg1 := upstream.URL + "/live/seg1.ts"
	seg2 := upstream.URL + "/live/seg2.ts"
	if !waitCached(h, seg1) || !waitCached(h, seg2) {
		t.Fatalf("segments not warmed by prefetch within timeout (upstream hits=%d)", atomic.LoadInt32(&segHits))
	}
	if got := atomic.LoadInt32(&segHits); got != 2 {
		t.Fatalf("expected 2 prefetch fetches, got %d", got)
	}

	// The player's own request now hits the warm cache — no extra upstream fetch.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(seg1), nil))
	if rec.Header().Get("X-Cache") != "HIT" || rec.Body.String() != "SEGDATA" {
		t.Errorf("expected warm cache HIT, got X-Cache=%q body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
	}
	if got := atomic.LoadInt32(&segHits); got != 2 {
		t.Errorf("cache HIT reached upstream: %d hits, want 2", got)
	}
}

// TestProxyPrefetchResolvesEdgeAfterRedirect guards against prefetch warming the
// wrong host: when the manifest 302-redirects to a tokenized edge, the warm-ups
// must target the edge (where root-relative segment paths + token are valid),
// not the entry host — otherwise every warm-up 401s and the player pays the full
// round trip anyway.
func TestProxyPrefetchResolvesEdgeAfterRedirect(t *testing.T) {
	var edgeHits int32
	edgeMux := http.NewServeMux()
	edgeMux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:6.0,\n/hlsr/TOKEN/seg1.ts\n")
	})
	edgeMux.HandleFunc("/hlsr/TOKEN/seg1.ts", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&edgeHits, 1)
		w.Header().Set("Content-Type", "video/MP2T")
		fmt.Fprint(w, "SEGDATA")
	})
	edge := httptest.NewServer(edgeMux)
	defer edge.Close()

	entryMux := http.NewServeMux()
	entryMux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, edge.URL+"/live/index.m3u8?token=abc", http.StatusFound)
	})
	entry := httptest.NewServer(entryMux)
	defer entry.Close()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback

	plReq := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(entry.URL+"/live/index.m3u8"), nil)
	h.ServeHTTP(httptest.NewRecorder(), plReq)

	// Prefetch must warm the EDGE segment URL, not an entry-host one.
	if !waitCached(h, edge.URL+"/hlsr/TOKEN/seg1.ts") {
		t.Fatalf("edge segment not warmed by prefetch (edge hits=%d)", atomic.LoadInt32(&edgeHits))
	}
	if got := atomic.LoadInt32(&edgeHits); got != 1 {
		t.Errorf("expected 1 edge prefetch fetch, got %d", got)
	}
}

// waitCached polls the segment cache until target is present or a short
// deadline passes (prefetch is asynchronous).
func waitCached(h *Handler, target string) bool {
	for i := 0; i < 100; i++ {
		if _, _, ok := h.segments.get(target); ok {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestProxyRejectsBadURLs(t *testing.T) {
	h := New(1 << 20)
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "javascript:alert(1)"} {
		req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(bad), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("url %q: expected 400, got %d", bad, rec.Code)
		}
	}
}

func TestProxyAppliesUpstreamHeaders(t *testing.T) {
	var mu sync.Mutex
	var ua, ref, origin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ua, ref, origin = r.Header.Get("User-Agent"), r.Header.Get("Referer"), r.Header.Get("Origin")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "SEG")
	}))
	defer srv.Close()
	host := func() string { u, _ := url.Parse(srv.URL); return u.Host }()

	h := New(10 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback

	// With an override for this host: the channel's UA + referer win, and Origin
	// is realigned to the referer's origin (not the stream host).
	h.SetUpstreamHeaders(map[string]UpstreamHeader{
		host: {UserAgent: "CustomUA/9.9", Referer: "https://embed.example.com/p?x=1"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(srv.URL+"/seg.bin"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	mu.Lock()
	if ua != "CustomUA/9.9" {
		t.Errorf("UA override not applied: %q", ua)
	}
	if ref != "https://embed.example.com/p?x=1" {
		t.Errorf("referer override not applied: %q", ref)
	}
	if origin != "https://embed.example.com" {
		t.Errorf("Origin not realigned to referer origin: %q", origin)
	}
	mu.Unlock()

	// No override → default browser UA and same-origin referer (fresh path to dodge cache).
	h.SetUpstreamHeaders(nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(srv.URL+"/seg2.bin"), nil))
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(ua, "Mozilla/") {
		t.Errorf("default UA not applied: %q", ua)
	}
	if ref != srv.URL+"/" {
		t.Errorf("default same-origin referer not applied: %q", ref)
	}
}

func TestProxyBlocksPrivateUpstreams(t *testing.T) {
	h := New(1 << 20)
	// Loopback, private, and link-local (cloud metadata) must be refused with 403.
	blocked := []string{
		"http://127.0.0.1:6379/",
		"http://localhost/x.m3u8",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/seg.ts",
		"http://192.168.1.1/admin",
		"http://[::1]/x.ts",
		"http://0.0.0.0/x.ts",
	}
	for _, u := range blocked {
		req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(u), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("url %q: expected 403, got %d", u, rec.Code)
		}
		if strings.Contains(rec.Body.String(), u) {
			t.Errorf("url %q: error body echoed the address", u)
		}
	}

	// A public hostname passes the guard (it then fails at fetch, but not 403/400).
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape("https://cdn.example.com/live/index.m3u8"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusBadRequest {
		t.Errorf("public host wrongly blocked: got %d", rec.Code)
	}

	// With the opt-out, a private address is allowed past the guard.
	h.SetAllowPrivateUpstreams(true)
	req = httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape("http://127.0.0.1:1/x.ts"), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Errorf("opt-out should allow private upstream, still got 403")
	}
}

func TestProxyUpstreamErrorPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	h := New(1 << 20)
	h.SetAllowPrivateUpstreams(true) // httptest binds loopback
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(upstream.URL+"/x.ts"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 passthrough, got %d", rec.Code)
	}
}

func TestLRUEviction(t *testing.T) {
	c := newLRUCache(100, 60)
	c.set("a", make([]byte, 60), "x", time.Minute)
	c.set("b", make([]byte, 60), "x", time.Minute) // evicts "a" (would exceed 100 bytes)
	if _, _, ok := c.get("a"); ok {
		t.Error("expected a evicted")
	}
	if _, _, ok := c.get("b"); !ok {
		t.Error("expected b present")
	}
	// Oversized items are skipped entirely.
	c.set("big", make([]byte, 61), "x", time.Minute)
	if _, _, ok := c.get("big"); ok {
		t.Error("oversized item should not be cached")
	}
}

func TestLRUTTLExpiry(t *testing.T) {
	c := newLRUCache(1000, 1000)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.set("k", []byte("v"), "x", time.Second)
	if _, _, ok := c.get("k"); !ok {
		t.Fatal("expected fresh entry")
	}
	now = now.Add(2 * time.Second)
	if _, _, ok := c.get("k"); ok {
		t.Error("expected entry expired")
	}
}
