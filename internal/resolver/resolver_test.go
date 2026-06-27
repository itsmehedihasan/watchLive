package resolver

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestExtractExposestratURL(t *testing.T) {
	html, err := os.ReadFile("testdata/exposestrat_nctvhd.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := extractExposestratURL(string(html))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	const want = "https://cdn13.zohanayaan.com:1686/hls/nctvhd.m3u8?md5=MLJKlfkyX2C3Ttl6Z5VFMg&expires=1782556559"
	if got != want {
		t.Errorf("extracted URL mismatch:\n got: %s\nwant: %s", got, want)
	}
	if exp := parseExpires(got); exp.Unix() != 1782556559 {
		t.Errorf("parseExpires: got %d, want 1782556559", exp.Unix())
	}
}

func TestExtractRejectsGarbage(t *testing.T) {
	if _, err := extractExposestratURL("<html>no token here</html>"); err == nil {
		t.Error("expected error when token function is absent")
	}
}

// stubProvider lets us exercise the Manager cache deterministically.
type stubProvider struct {
	name  string
	calls int
	res   Resolved
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Resolve(context.Context, string) (Resolved, error) {
	s.calls++
	return s.res, nil
}

func TestManagerCachesUntilExpiry(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	stub := &stubProvider{name: "x", res: Resolved{URL: "u", Referer: "r", Expires: base.Add(10 * time.Minute)}}

	m := NewManager()
	m.now = func() time.Time { return clock }
	m.Add(stub)

	if _, err := m.Resolve(context.Background(), "x", "slug"); err != nil {
		t.Fatal(err)
	}
	// Second call well within validity → served from cache, no extra Resolve.
	clock = base.Add(5 * time.Minute)
	if _, err := m.Resolve(context.Background(), "x", "slug"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("expected 1 upstream resolve while cached, got %d", stub.calls)
	}

	// Past expiry-minus-margin → refetched.
	clock = base.Add(10 * time.Minute)
	if _, err := m.Resolve(context.Background(), "x", "slug"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 2 {
		t.Fatalf("expected refetch near expiry, got %d calls", stub.calls)
	}
}

func TestManagerUnknownProvider(t *testing.T) {
	m := NewManager()
	if _, err := m.Resolve(context.Background(), "nope", "x"); err == nil {
		t.Error("expected ErrUnknownProvider")
	}
}
