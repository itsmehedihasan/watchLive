package viewers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore returns a store with a controllable clock.
func newTestStore() (*Store, *time.Time) {
	s := NewStore()
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestHeartbeatCounts(t *testing.T) {
	s, _ := newTestStore()

	snap := s.Heartbeat("alice", "5", 5)
	if snap.Total != 1 {
		t.Errorf("total = %d, want 1", snap.Total)
	}
	if snap.ChannelCount == nil || *snap.ChannelCount != 1 {
		t.Errorf("channelCount = %v, want 1", snap.ChannelCount)
	}

	snap = s.Heartbeat("bob", "5", 5)
	if snap.Total != 2 || *snap.ChannelCount != 2 {
		t.Errorf("total=%d channel=%d, want 2/2", snap.Total, *snap.ChannelCount)
	}

	// bob switches channels: channel 5 drops to 1, channel 7 is 1.
	snap = s.Heartbeat("bob", "7", 5)
	if *snap.ChannelCount != 1 {
		t.Errorf("channel 7 count = %d, want 1", *snap.ChannelCount)
	}
	snap = s.Read("5", 5)
	if *snap.ChannelCount != 1 {
		t.Errorf("channel 5 count = %d, want 1", *snap.ChannelCount)
	}
}

func TestRapidSwitchingDoesNotInflate(t *testing.T) {
	s, _ := newTestStore()

	// One session flapping between two channels many times.
	for i := 0; i < 50; i++ {
		s.Heartbeat("flapper", "1", 5)
		s.Heartbeat("flapper", "2", 5)
	}
	snap := s.Read("1", 5)
	c1 := *snap.ChannelCount
	snap = s.Read("2", 5)
	c2 := *snap.ChannelCount
	if snap.Total != 1 || c1+c2 != 1 {
		t.Errorf("total=%d ch1=%d ch2=%d — live counts inflated by rapid switching", snap.Total, c1, c2)
	}
}

func TestSessionExpiry(t *testing.T) {
	s, now := newTestStore()

	s.Heartbeat("alice", "3", 5)
	*now = now.Add(30 * time.Second)
	s.Heartbeat("bob", "3", 5)

	// alice (60s+ stale) expires, bob stays.
	*now = now.Add(45 * time.Second)
	snap := s.Read("3", 5)
	if snap.Total != 1 || *snap.ChannelCount != 1 {
		t.Errorf("after expiry: total=%d channel=%d, want 1/1", snap.Total, *snap.ChannelCount)
	}
}

func TestTopChannels(t *testing.T) {
	s, _ := newTestStore()

	// Channel 9 gets 3 tune-ins, channel 4 gets 2, channel 1 gets 1.
	s.Heartbeat("a", "9", 5)
	s.Heartbeat("b", "9", 5)
	s.Heartbeat("c", "9", 5)
	s.Heartbeat("a", "4", 5)
	s.Heartbeat("b", "4", 5)
	s.Heartbeat("a", "1", 5)

	snap := s.Read("", 5)
	if snap.ChannelCount != nil {
		t.Error("channelCount should be null without channelId")
	}
	if len(snap.Top) != 3 {
		t.Fatalf("top length = %d, want 3", len(snap.Top))
	}
	if snap.Top[0].ID != "9" || snap.Top[0].Count != 3 {
		t.Errorf("top[0] = %+v, want id=9 count=3", snap.Top[0])
	}
	if snap.Top[1].ID != "4" || snap.Top[2].ID != "1" {
		t.Errorf("top order wrong: %+v", snap.Top)
	}

	// topN limits the list.
	snap = s.Read("", 2)
	if len(snap.Top) != 2 {
		t.Errorf("top length = %d, want 2", len(snap.Top))
	}
}

func TestConcurrentHeartbeats(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := string(rune('a' + n%26))
			s.Heartbeat(sid, "1", 5)
		}(i)
	}
	wg.Wait()
	snap := s.Read("1", 5)
	if snap.Total != 26 || *snap.ChannelCount != 26 {
		t.Errorf("total=%d channel=%d, want 26/26", snap.Total, *snap.ChannelCount)
	}
}

func TestHandlerPOSTAndGET(t *testing.T) {
	h := &Handler{Store: NewStore()}

	post := httptest.NewRequest(http.MethodPost, "/api/viewers",
		strings.NewReader(`{"sessionId":"s1","channelId":"2"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d", rec.Code)
	}
	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if snap.Total != 1 || snap.ChannelCount == nil || *snap.ChannelCount != 1 {
		t.Errorf("POST snapshot: %+v", snap)
	}

	// Malformed body still returns counts (original behavior).
	bad := httptest.NewRequest(http.MethodPost, "/api/viewers", strings.NewReader("not json"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusOK {
		t.Errorf("malformed POST status %d, want 200", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil || snap.Total != 1 {
		t.Errorf("malformed POST snapshot: %+v err=%v", snap, err)
	}
	if snap.ChannelCount != nil {
		t.Error("channelCount should be null for malformed body")
	}

	// GET with channelId.
	get := httptest.NewRequest(http.MethodGet, "/api/viewers?channelId=2&top=3", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("GET invalid JSON: %v", err)
	}
	if snap.Total != 1 || snap.ChannelCount == nil || *snap.ChannelCount != 1 {
		t.Errorf("GET snapshot: %+v", snap)
	}

	// JSON shape check: channelCount must be literal null when absent.
	getNoCh := httptest.NewRequest(http.MethodGet, "/api/viewers", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, getNoCh)
	if !strings.Contains(rec.Body.String(), `"channelCount":null`) {
		t.Errorf("expected channelCount:null in body: %s", rec.Body.String())
	}
}
