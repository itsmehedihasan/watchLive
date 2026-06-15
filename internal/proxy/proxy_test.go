package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestProxyDASHManifestNotRewritten(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/api/proxy?url="+url.QueryEscape(upstream.URL+"/live/manifest.mpd"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// Manifest must be passed through byte-for-byte (Shaka's request filter
	// handles proxy routing, not server-side rewriting).
	if rec.Body.String() != manifest {
		t.Errorf("DASH manifest was modified:\n%s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/dash+xml" {
		t.Errorf("content type %q, want application/dash+xml", ct)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-cache") {
		t.Errorf("DASH manifest must not be browser-cached: %q", rec.Header().Get("Cache-Control"))
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

func TestProxyUpstreamErrorPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	h := New(1 << 20)
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
