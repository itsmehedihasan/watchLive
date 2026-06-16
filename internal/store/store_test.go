package store

import (
	"testing"
	"time"

	"watchlive/internal/playlist"
)

// open returns an in-memory store for fast tests. Open keeps a single
// connection, so the in-memory DB survives for the test's lifetime.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ch(id, name string, urls ...string) playlist.Channel {
	c := playlist.Channel{ID: id, Name: name, Group: "US", Type: "News"}
	for _, u := range urls {
		c.Servers = append(c.Servers, playlist.Server{URL: u})
	}
	return c
}

func TestOpenFileBacked(t *testing.T) {
	// Exercise the real WAL/PRAGMA path, not just :memory:.
	path := t.TempDir() + "/catalog.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open file: %v", err)
	}
	defer s.Close()
	if empty, _ := s.IsEmpty(); !empty {
		t.Fatal("fresh DB should be empty")
	}
}

func TestUpsertPreservesUserState(t *testing.T) {
	s := open(t)

	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{ch("tvg:a", "Alpha", "http://a/1")}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// User favourites it and the prober marks it working.
	if ok, _ := s.SetFavourite("tvg:a", true); !ok {
		t.Fatal("SetFavourite should report ok")
	}
	if err := s.SetHealth(map[string]bool{"tvg:a": true}, time.Unix(1000, 0)); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}

	// Re-sync with a changed URL for the same channel.
	ins, upd, seen, err := s.UpsertCatalog([]playlist.Channel{ch("tvg:a", "Alpha HD", "http://a/2")})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if ins != 0 || upd != 1 {
		t.Errorf("counts: ins=%d upd=%d, want 0/1", ins, upd)
	}
	if !seen["tvg:a"] {
		t.Error("seen set missing the channel")
	}

	chans, _ := s.ListChannels()
	if len(chans) != 1 {
		t.Fatalf("want 1 channel, got %d", len(chans))
	}
	got := chans[0]
	if got.Name != "Alpha HD" || got.Servers[0].URL != "http://a/2" {
		t.Errorf("feed fields not updated: %+v", got)
	}
	if !got.IsFavourite {
		t.Error("favourite not preserved across sync")
	}
	if got.IsWorking == nil || !*got.IsWorking {
		t.Error("working verdict not preserved across sync")
	}
}

func TestUpsertNeverTouchesManual(t *testing.T) {
	s := open(t)
	m, err := s.AddManual("My Stream", "http://m/1")
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	// A feed channel that happens to collide on the manual id must not clobber it.
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{ch(m.ID, "Hijacked", "http://x/1")}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.getChannel(m.ID)
	if err != nil {
		t.Fatalf("getChannel: %v", err)
	}
	if got.Name != "My Stream" || !got.IsFavourite {
		t.Errorf("manual row was modified by upsert: %+v", got)
	}
}

func TestSetFavouriteMissing(t *testing.T) {
	s := open(t)
	ok, err := s.SetFavourite("nope", true)
	if err != nil {
		t.Fatalf("SetFavourite: %v", err)
	}
	if ok {
		t.Error("expected ok=false for missing channel")
	}
}

func TestAddManualIdempotentAndNamespaced(t *testing.T) {
	s := open(t)
	a, _ := s.AddManual("Chan", "http://c/1")
	b, _ := s.AddManual("Chan", "http://c/1")
	if a.ID != b.ID {
		t.Errorf("re-add changed id: %q vs %q", a.ID, b.ID)
	}
	if a.ID[:7] != "manual:" {
		t.Errorf("manual id not namespaced: %q", a.ID)
	}
	if !a.IsFavourite || a.IsWorking == nil || !*a.IsWorking {
		t.Errorf("manual defaults wrong: %+v", a)
	}
	if n, _ := s.Count(); n != 1 {
		t.Errorf("idempotent add should leave 1 row, got %d", n)
	}

	// Different URL → distinct channel.
	c, _ := s.AddManual("Chan", "http://c/2")
	if c.ID == a.ID {
		t.Error("different url should yield a different id")
	}
}

func TestDeleteManual(t *testing.T) {
	s := open(t)
	m, _ := s.AddManual("Gone", "http://g/1")
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{ch("tvg:f", "Feed", "http://f/1")}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteManual("missing"); err != ErrNotFound {
		t.Errorf("delete missing: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteManual("tvg:f"); err != ErrNotManual {
		t.Errorf("delete feed channel: got %v, want ErrNotManual", err)
	}
	if err := s.DeleteManual(m.ID); err != nil {
		t.Errorf("delete manual: %v", err)
	}
	if n, _ := s.Count(); n != 1 {
		t.Errorf("after delete want 1 row, got %d", n)
	}
}

func TestSetHealthOnlyTouchesMapped(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{ch("a", "A", "http://a"), ch("b", "B", "http://b")})

	if err := s.SetHealth(map[string]bool{"a": true}, time.Unix(2000, 0)); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	chans, _ := s.ListChannels()
	byID := map[string]Channel{}
	for _, c := range chans {
		byID[c.ID] = c
	}
	if byID["a"].IsWorking == nil || !*byID["a"].IsWorking {
		t.Error("a should be working")
	}
	if byID["b"].IsWorking != nil {
		t.Error("b should remain unprobed (nil)")
	}
}

func TestStaleTargetsTTLBoundary(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{
		ch("fresh", "Fresh", "http://fresh"),
		ch("old", "Old", "http://old"),
		ch("never", "Never", "http://never"),
	})

	// Pin "now" so the boundary is deterministic.
	base := time.Unix(100000, 0)
	now = func() time.Time { return base }
	defer func() { now = time.Now }()

	s.SetHealth(map[string]bool{"fresh": true}, base.Add(-1*time.Hour)) // 1h old
	s.SetHealth(map[string]bool{"old": true}, base.Add(-10*time.Hour))  // 10h old
	// "never" left unprobed.

	stale, err := s.StaleTargets(6*time.Hour, false)
	if err != nil {
		t.Fatalf("StaleTargets: %v", err)
	}
	got := map[string]bool{}
	for _, tgt := range stale {
		got[tgt.ID] = true
	}
	if got["fresh"] {
		t.Error("fresh (1h < 6h ttl) should not be stale")
	}
	if !got["old"] {
		t.Error("old (10h > 6h ttl) should be stale")
	}
	if !got["never"] {
		t.Error("never-probed should be stale")
	}

	// force returns everything.
	all, _ := s.StaleTargets(6*time.Hour, true)
	if len(all) != 3 {
		t.Errorf("force should return all 3, got %d", len(all))
	}
	// URLs are carried through.
	for _, tgt := range all {
		if len(tgt.URLs) == 0 {
			t.Errorf("target %s has no URLs", tgt.ID)
		}
	}
}

func TestPruneOrphans(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{
		ch("keep", "Keep", "http://k"),
		ch("fav", "Fav", "http://f"),
		ch("orphan", "Orphan", "http://o"),
	})
	s.SetFavourite("fav", true)
	man, _ := s.AddManual("Man", "http://m")

	// New feed no longer contains "fav" or "orphan".
	_, _, seen, _ := s.UpsertCatalog([]playlist.Channel{ch("keep", "Keep", "http://k")})

	n, err := s.PruneOrphans(seen)
	if err != nil {
		t.Fatalf("PruneOrphans: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 orphan pruned, got %d", n)
	}

	ids := map[string]bool{}
	chans, _ := s.ListChannels()
	for _, c := range chans {
		ids[c.ID] = true
	}
	if ids["orphan"] {
		t.Error("orphan should be pruned")
	}
	if !ids["fav"] {
		t.Error("favourited-but-removed channel must be kept")
	}
	if !ids[man.ID] {
		t.Error("manual channel must be kept")
	}
}

func TestImportManual(t *testing.T) {
	s := open(t)
	added, err := s.ImportManual([]ImportEntry{
		{Name: "Alpha", URL: "http://a/1"},
		{Name: "Beta", URL: "https://b/1"},
		{Name: "", URL: "http://x/1"},        // skipped: no name
		{Name: "Bad", URL: "ftp://nope"},     // skipped: not http(s)
		{Name: "Alpha", URL: "http://a/1"},   // dup within batch: counted once
	})
	if err != nil {
		t.Fatalf("ImportManual: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	chans, _ := s.ListChannels()
	if len(chans) != 2 {
		t.Fatalf("want 2 channels, got %d", len(chans))
	}
	for _, c := range chans {
		if c.ID[:7] != "manual:" {
			t.Errorf("imported channel not namespaced manual: %q", c.ID)
		}
		if c.IsFavourite {
			t.Errorf("imported channels must NOT be auto-favourited: %+v", c)
		}
		if c.IsWorking == nil || !*c.IsWorking {
			t.Errorf("imported channels should default to working: %+v", c)
		}
	}

	// Re-import is idempotent (same name+url → same manual id).
	again, _ := s.ImportManual([]ImportEntry{{Name: "Alpha", URL: "http://a/1"}})
	if again != 1 {
		t.Errorf("re-import added = %d, want 1 (idempotent upsert)", again)
	}
	if n, _ := s.Count(); n != 2 {
		t.Errorf("re-import should not duplicate; count = %d, want 2", n)
	}
}

func TestMeta(t *testing.T) {
	s := open(t)
	if v, _ := s.GetMeta("k"); v != "" {
		t.Errorf("absent key should be empty, got %q", v)
	}
	if err := s.SetMeta("k", "v1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	s.SetMeta("k", "v2")
	if v, _ := s.GetMeta("k"); v != "v2" {
		t.Errorf("GetMeta = %q, want v2", v)
	}
}
