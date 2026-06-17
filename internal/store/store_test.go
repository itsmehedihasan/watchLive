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
	m, err := s.AddManual("My Stream", "http://m/1", nil)
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
	a, _ := s.AddManual("Chan", "http://c/1", nil)
	b, _ := s.AddManual("Chan", "http://c/1", nil)
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
	c, _ := s.AddManual("Chan", "http://c/2", nil)
	if c.ID == a.ID {
		t.Error("different url should yield a different id")
	}
}

func TestDeleteManual(t *testing.T) {
	s := open(t)
	m, _ := s.AddManual("Gone", "http://g/1", nil)
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

func TestUpdateManual(t *testing.T) {
	s := open(t)
	m, _ := s.AddManual("Mine", "http://m/old", nil)
	s.SetFavourite(m.ID, true)
	s.SetHealth(map[string]bool{m.ID: true}, time.Unix(1000, 0))

	// Updating keeps the same id, so favourite/health state survives.
	got, err := s.UpdateManual(m.ID, "  Mine HD  ", "  http://m/new  ")
	if err != nil {
		t.Fatalf("UpdateManual: %v", err)
	}
	if got.ID != m.ID {
		t.Errorf("id changed on update: %q -> %q", m.ID, got.ID)
	}
	if got.Name != "Mine HD" {
		t.Errorf("name not updated/trimmed: %q", got.Name)
	}
	if len(got.Servers) != 1 || got.Servers[0].URL != "http://m/new" {
		t.Errorf("link not updated/trimmed: %+v", got.Servers)
	}
	if !got.IsFavourite {
		t.Error("favourite lost on update")
	}
	if got.IsWorking == nil || !*got.IsWorking {
		t.Error("health verdict lost on update")
	}

	// The new URL is indexed; the old one is gone.
	idx, _ := s.URLIndex()
	if _, ok := idx["http://m/new"]; !ok {
		t.Error("updated URL not indexed")
	}
	if _, ok := idx["http://m/old"]; ok {
		t.Error("old URL still indexed after update")
	}

	// Feed channels and unknown ids are refused.
	s.UpsertCatalog([]playlist.Channel{ch("tvg:f", "Feed", "http://f/1")})
	if _, err := s.UpdateManual("tvg:f", "Feed", "http://f/2"); err != ErrNotManual {
		t.Errorf("update feed: got %v, want ErrNotManual", err)
	}
	if _, err := s.UpdateManual("manual:missing", "X", "http://x/1"); err != ErrNotFound {
		t.Errorf("update missing: got %v, want ErrNotFound", err)
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
	man, _ := s.AddManual("Man", "http://m", nil)

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
		{Name: "Alpha 2", URL: "http://a/1"}, // dup LINK within batch (diff name): skipped
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

	// Re-import of an existing LINK is skipped (dedupe by URL against the DB),
	// even under a different name.
	again, _ := s.ImportManual([]ImportEntry{{Name: "Alpha Renamed", URL: "http://a/1"}})
	if again != 0 {
		t.Errorf("re-import of existing link added = %d, want 0", again)
	}
	if n, _ := s.Count(); n != 2 {
		t.Errorf("re-import should not duplicate; count = %d, want 2", n)
	}

	// A link that collides with a FEED channel's server URL is also skipped.
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{ch("tvg:feed", "Feed Chan", "http://feed/1")}); err != nil {
		t.Fatalf("seed feed channel: %v", err)
	}
	n2, _ := s.ImportManual([]ImportEntry{
		{Name: "Dup Of Feed", URL: "http://feed/1"}, // collides with feed → skipped
		{Name: "Brand New", URL: "http://new/1"},     // unique → added
	})
	if n2 != 1 {
		t.Errorf("import vs feed link: added = %d, want 1", n2)
	}
}

func TestURLIndex(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{ch("tvg:x", "Multi", "http://x/1", "http://x/2")})
	s.AddManual("Mine", "http://m/1", nil)
	idx, err := s.URLIndex()
	if err != nil {
		t.Fatalf("URLIndex: %v", err)
	}
	if idx["http://x/1"].ID != "tvg:x" || idx["http://x/2"].Name != "Multi" {
		t.Errorf("feed server URLs not indexed: %+v", idx)
	}
	if _, ok := idx["http://m/1"]; !ok {
		t.Error("manual channel URL not indexed")
	}
	if _, ok := idx["http://absent"]; ok {
		t.Error("unexpected URL in index")
	}
}

func TestClearKeysRoundTrip(t *testing.T) {
	s := open(t)

	keys := map[string]string{"549ab7cd35a64bb6bb479ecead04d69d": "829799ed534d11fcadeb4b192467e050"}

	// Manual add carries the key through to ListChannels.
	if _, err := s.AddManual("DRM Chan", "https://x/index.mpd", keys); err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	// Import carries it too.
	if _, err := s.ImportManual([]ImportEntry{{Name: "DRM Import", URL: "https://y/index.mpd", ClearKeys: keys}}); err != nil {
		t.Fatalf("ImportManual: %v", err)
	}
	// A feed channel with a key survives upsert.
	feed := ch("tvg:drm", "DRM Feed", "https://z/index.mpd")
	feed.ClearKeys = keys
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{feed}); err != nil {
		t.Fatalf("UpsertCatalog: %v", err)
	}

	chans, _ := s.ListChannels()
	if len(chans) != 3 {
		t.Fatalf("want 3 channels, got %d", len(chans))
	}
	for _, c := range chans {
		if c.ClearKeys["549ab7cd35a64bb6bb479ecead04d69d"] != "829799ed534d11fcadeb4b192467e050" {
			t.Errorf("%s lost its clear key: %+v", c.Name, c.ClearKeys)
		}
	}

	// A clear channel has no keys (nil, not an empty map artefact).
	if _, err := s.AddManual("Clear", "https://c/stream.m3u8", nil); err != nil {
		t.Fatalf("AddManual clear: %v", err)
	}
	got, _ := s.getChannel("manual:" + manualHash("Clear", "https://c/stream.m3u8"))
	if got.ClearKeys != nil {
		t.Errorf("clear channel should have nil ClearKeys, got %+v", got.ClearKeys)
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
