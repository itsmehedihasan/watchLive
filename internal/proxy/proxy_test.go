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
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		time.Sleep(50 * time.Millisecond) // widen the race window for concurrent requests
		w.Header().Set("Content-Type", "video/MP2T")
		fmt.Fprint(w, "SEGMENTDATA")
	}))
	defer upstream.Close()

	h := New(10 << 20)
	segURL := upstream.URL + "/seg001.ts"

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
