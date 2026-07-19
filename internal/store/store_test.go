package store

import (
	"errors"
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
	m, err := s.AddManual("My Stream", "http://m/1", nil, "", "")
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
	a, _ := s.AddManual("Chan", "http://c/1", nil, "", "")
	b, _ := s.AddManual("Chan", "http://c/1", nil, "", "")
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
	c, _ := s.AddManual("Chan", "http://c/2", nil, "", "")
	if c.ID == a.ID {
		t.Error("different url should yield a different id")
	}
}

func TestDeleteManual(t *testing.T) {
	s := open(t)
	m, _ := s.AddManual("Gone", "http://g/1", nil, "", "")
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
	m, _ := s.AddManual("Mine", "http://m/old", nil, "", "")
	s.SetFavourite(m.ID, true)
	s.SetHealth(map[string]bool{m.ID: true}, time.Unix(1000, 0))

	// Updating keeps the same id, so favourite/health state survives.
	got, err := s.UpdateManual(m.ID, "  Mine HD  ", "  http://m/new  ", "", "")
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
	if _, err := s.UpdateManual("tvg:f", "Feed", "http://f/2", "", ""); err != ErrNotManual {
		t.Errorf("update feed: got %v, want ErrNotManual", err)
	}
	if _, err := s.UpdateManual("manual:missing", "X", "http://x/1", "", ""); err != ErrNotFound {
		t.Errorf("update missing: got %v, want ErrNotFound", err)
	}
}

func TestManualHeaderHints(t *testing.T) {
	s := open(t)
	const ref, ua = "https://exposestrat.com/", "CustomUA/1.0"

	// AddManual persists the referer/user-agent the CDN gate needs.
	m, err := s.AddManual("Gated", "https://cdn9.zohanayaan.com/x.m3u8", nil, "  "+ref+"  ", "  "+ua+"  ")
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	if m.Referer != ref || m.UserAgent != ua {
		t.Errorf("add did not persist/trim headers: ref=%q ua=%q", m.Referer, m.UserAgent)
	}

	// UpdateManual replaces them (and can clear them).
	got, err := s.UpdateManual(m.ID, "Gated", "https://cdn9.zohanayaan.com/x.m3u8", "https://other.tld/", "")
	if err != nil {
		t.Fatalf("UpdateManual: %v", err)
	}
	if got.Referer != "https://other.tld/" || got.UserAgent != "" {
		t.Errorf("update did not replace headers: ref=%q ua=%q", got.Referer, got.UserAgent)
	}
}

func TestAddResolvable(t *testing.T) {
	s := open(t)
	ch, err := s.AddResolvable("Fox_5", "exposestrat", "nctvhd", "https://exposestrat.com/", "")
	if err != nil {
		t.Fatalf("AddResolvable: %v", err)
	}
	if ch.Resolver != "exposestrat" || ch.ResolverArg != "nctvhd" {
		t.Errorf("recipe not persisted: resolver=%q arg=%q", ch.Resolver, ch.ResolverArg)
	}
	if ch.Referer != "https://exposestrat.com/" || !ch.IsFavourite {
		t.Errorf("defaults wrong: ref=%q fav=%v", ch.Referer, ch.IsFavourite)
	}
	if len(ch.Servers) != 0 {
		t.Errorf("expected empty servers before first resolve, got %+v", ch.Servers)
	}

	// SetResolvedURL caches the fresh URL into servers[0] without disturbing the recipe.
	const fresh = "https://cdn13.zohanayaan.com:1686/hls/nctvhd.m3u8?md5=x&expires=1"
	if err := s.SetResolvedURL(ch.ID, fresh); err != nil {
		t.Fatalf("SetResolvedURL: %v", err)
	}
	got, _ := s.Get(ch.ID)
	if len(got.Servers) != 1 || got.Servers[0].URL != fresh {
		t.Errorf("servers not updated: %+v", got.Servers)
	}
	if got.Resolver != "exposestrat" || got.ResolverArg != "nctvhd" {
		t.Errorf("recipe lost after SetResolvedURL: %+v", got)
	}
	if err := s.SetResolvedURL("manual:missing", fresh); err != ErrNotFound {
		t.Errorf("SetResolvedURL missing: got %v, want ErrNotFound", err)
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
	man, _ := s.AddManual("Man", "http://m", nil, "", "")

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
		{Name: "Brand New", URL: "http://new/1"},    // unique → added
	})
	if n2 != 1 {
		t.Errorf("import vs feed link: added = %d, want 1", n2)
	}
}

func TestURLIndex(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{ch("tvg:x", "Multi", "http://x/1", "http://x/2")})
	s.AddManual("Mine", "http://m/1", nil, "", "")
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
	if _, err := s.AddManual("DRM Chan", "https://x/index.mpd", keys, "", ""); err != nil {
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
	if _, err := s.AddManual("Clear", "https://c/stream.m3u8", nil, "", ""); err != nil {
		t.Fatalf("AddManual clear: %v", err)
	}
	got, _ := s.getChannel("manual:" + manualHash("Clear", "https://c/stream.m3u8"))
	if got.ClearKeys != nil {
		t.Errorf("clear channel should have nil ClearKeys, got %+v", got.ClearKeys)
	}
}

func TestHeaderHintsRoundTrip(t *testing.T) {
	s := open(t)

	feed := ch("tvg:ua", "UA Feed", "https://z/index.m3u8")
	feed.UserAgent = "Mozilla/5.0 (Pixel 7)"
	feed.Referer = "https://site.example/?p=1"
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{feed}); err != nil {
		t.Fatalf("UpsertCatalog: %v", err)
	}
	got, _ := s.getChannel("tvg:ua")
	if got.UserAgent != "Mozilla/5.0 (Pixel 7)" || got.Referer != "https://site.example/?p=1" {
		t.Errorf("header hints not persisted: ua=%q ref=%q", got.UserAgent, got.Referer)
	}

	// A re-sync (upsert with changed/cleared headers) updates them — DB tracks source.
	feed.UserAgent = "Mozilla/5.0 (Changed)"
	feed.Referer = ""
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{feed}); err != nil {
		t.Fatalf("UpsertCatalog 2: %v", err)
	}
	got, _ = s.getChannel("tvg:ua")
	if got.UserAgent != "Mozilla/5.0 (Changed)" || got.Referer != "" {
		t.Errorf("re-sync did not update headers: ua=%q ref=%q", got.UserAgent, got.Referer)
	}

	// A channel with no hints round-trips as empty strings.
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{ch("tvg:plain", "Plain", "https://p/index.m3u8")}); err != nil {
		t.Fatalf("UpsertCatalog plain: %v", err)
	}
	got, _ = s.getChannel("tvg:plain")
	if got.UserAgent != "" || got.Referer != "" {
		t.Errorf("plain channel should have empty hints: %+v", got)
	}
}

func TestBackfillHeaders(t *testing.T) {
	s := open(t)
	// Catalog: three feed channels, no headers (as a pre-columns catalog would be).
	if _, _, _, err := s.UpsertCatalog([]playlist.Channel{
		ch("tvg:a", "A", "https://cdn.a/x.m3u8"),
		ch("tvg:b", "B", "https://cdn.b/y.m3u8"),
		ch("tvg:c", "C", "https://cdn.c/z.m3u8"),
	}); err != nil {
		t.Fatalf("UpsertCatalog: %v", err)
	}

	// Seed: A has UA+referer, B has UA only, Ghost's URL isn't in the catalog.
	seed := []playlist.Channel{
		{Name: "A", UserAgent: "UA-A", Referer: "https://site.a/p", Servers: []playlist.Server{{URL: "https://cdn.a/x.m3u8"}}},
		{Name: "B", UserAgent: "UA-B", Servers: []playlist.Server{{URL: "https://cdn.b/y.m3u8"}}},
		{Name: "Ghost", UserAgent: "UA-G", Servers: []playlist.Server{{URL: "https://not.in.catalog/q.m3u8"}}},
	}
	n, err := s.BackfillHeaders(seed)
	if err != nil {
		t.Fatalf("BackfillHeaders: %v", err)
	}
	if n != 2 {
		t.Errorf("updated = %d, want 2 (Ghost has no catalog match)", n)
	}

	if a, _ := s.getChannel("tvg:a"); a.UserAgent != "UA-A" || a.Referer != "https://site.a/p" {
		t.Errorf("A not filled: ua=%q ref=%q", a.UserAgent, a.Referer)
	}
	if b, _ := s.getChannel("tvg:b"); b.UserAgent != "UA-B" || b.Referer != "" {
		t.Errorf("B (UA-only) wrong: ua=%q ref=%q", b.UserAgent, b.Referer)
	}
	if c, _ := s.getChannel("tvg:c"); c.UserAgent != "" || c.Referer != "" {
		t.Errorf("C (no seed hint) should stay blank: ua=%q ref=%q", c.UserAgent, c.Referer)
	}
	if cnt, _ := s.Count(); cnt != 3 {
		t.Errorf("channel count = %d, want 3 (no adds/removes)", cnt)
	}

	// Fill-only: adding a referer to B must NOT clear its existing UA.
	if _, err := s.BackfillHeaders([]playlist.Channel{
		{Name: "B", Referer: "https://site.b/r", Servers: []playlist.Server{{URL: "https://cdn.b/y.m3u8"}}},
	}); err != nil {
		t.Fatalf("BackfillHeaders fill-only: %v", err)
	}
	if b, _ := s.getChannel("tvg:b"); b.UserAgent != "UA-B" || b.Referer != "https://site.b/r" {
		t.Errorf("fill-only failed: ua=%q ref=%q (want UA-B + referer)", b.UserAgent, b.Referer)
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

func TestPruneUnkept(t *testing.T) {
	s := open(t)
	s.UpsertCatalog([]playlist.Channel{
		ch("feed-a", "Feed A", "http://a"),
		ch("feed-b", "Feed B", "http://b"),
		ch("feed-fav", "Feed Fav", "http://c"),
	})
	s.SetFavourite("feed-fav", true)
	man, _ := s.AddManual("Man", "http://m", nil, "", "")

	// Unconditional: both non-fav, non-manual feed rows go, even though they are
	// still "in the feed" (PruneUnkept ignores any feed).
	n, err := s.PruneUnkept()
	if err != nil {
		t.Fatalf("PruneUnkept: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}

	ids := map[string]bool{}
	chans, _ := s.ListChannels()
	for _, c := range chans {
		ids[c.ID] = true
	}
	if ids["feed-a"] || ids["feed-b"] {
		t.Error("plain feed channels should be deleted")
	}
	if !ids["feed-fav"] {
		t.Error("favourited channel must be kept")
	}
	if !ids[man.ID] {
		t.Error("manual channel must be kept")
	}
}

func TestXtreamPlaylistCRUD(t *testing.T) {
	s := open(t)

	p, err := s.SaveXtreamPlaylist("My Panel", "http://p:8080", "user", "pass")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist: %v", err)
	}
	if p.ID == "" {
		t.Fatal("saved playlist should have an id")
	}
	if p.Name != "My Panel" || p.Server != "http://p:8080" || p.Username != "user" || p.Password != "pass" {
		t.Errorf("saved playlist = %+v", p)
	}

	got, err := s.GetXtreamPlaylist(p.ID)
	if err != nil {
		t.Fatalf("GetXtreamPlaylist: %v", err)
	}
	// Password must round-trip in-process (refresh needs it) even though it is
	// omitted from JSON.
	if got.Password != "pass" {
		t.Errorf("password not persisted: %q", got.Password)
	}

	if _, err := s.GetXtreamPlaylist("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing playlist err = %v, want ErrNotFound", err)
	}

	list, err := s.ListXtreamPlaylists()
	if err != nil {
		t.Fatalf("ListXtreamPlaylists: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 playlist, got %d", len(list))
	}
	// No channels reference it yet.
	if list[0].Imported {
		t.Error("playlist with no channels should report Imported=false")
	}
}

func TestUpsertXtreamChannels(t *testing.T) {
	s := open(t)
	p, _ := s.SaveXtreamPlaylist("Panel", "http://p", "u", "pw")

	streams := []XtreamStream{
		{StreamID: 101, Name: "Alpha", Logo: "http://l/a.png", URL: "http://p/live/u/pw/101.ts"},
		{StreamID: 202, Name: "Beta", URL: "http://p/live/u/pw/202.ts"},
		{StreamID: 303, Name: "", URL: "http://p/live/u/pw/303.ts"},   // skipped: no name
		{StreamID: 404, Name: "Bad", URL: "ftp://nope"},               // skipped: not http(s)
		{StreamID: 101, Name: "Alpha Dup", URL: "http://p/live/x.ts"}, // dup stream_id in batch
	}
	added, updated, err := s.UpsertXtreamChannels(p.ID, streams)
	if err != nil {
		t.Fatalf("UpsertXtreamChannels: %v", err)
	}
	if added != 2 || updated != 0 {
		t.Fatalf("first import added=%d updated=%d, want 2/0", added, updated)
	}

	chans, _ := s.ListChannels()
	if len(chans) != 2 {
		t.Fatalf("want 2 channels, got %d", len(chans))
	}
	var alpha Channel
	for _, c := range chans {
		if c.ID == "xtream:"+p.ID+":101" {
			alpha = c
		}
		// Imported rows survive pruning (is_manual) but are not favourited.
		if c.IsFavourite {
			t.Errorf("xtream channels must not be auto-favourited: %+v", c)
		}
	}
	if alpha.ID == "" {
		t.Fatal("expected stable id xtream:<pid>:101")
	}
	if alpha.Name != "Alpha" || alpha.Logo != "http://l/a.png" {
		t.Errorf("alpha = %+v", alpha)
	}

	// The playlist now reports Imported.
	list, _ := s.ListXtreamPlaylists()
	if !list[0].Imported {
		t.Error("playlist with channels should report Imported=true")
	}

	// User favourites Alpha and the prober marks it working; a refresh with a
	// changed name/URL must UPDATE in place, preserving that user state.
	s.SetFavourite(alpha.ID, true)
	s.SetHealth(map[string]bool{alpha.ID: true}, time.Unix(1000, 0))

	added2, updated2, err := s.UpsertXtreamChannels(p.ID, []XtreamStream{
		{StreamID: 101, Name: "Alpha HD", URL: "http://p/live/u/pw/101.m3u8"},
		{StreamID: 999, Name: "Gamma", URL: "http://p/live/u/pw/999.ts"},
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if added2 != 1 || updated2 != 1 {
		t.Fatalf("refresh added=%d updated=%d, want 1/1", added2, updated2)
	}

	got, _ := s.Get(alpha.ID)
	if got.Name != "Alpha HD" {
		t.Errorf("refresh should update name, got %q", got.Name)
	}
	if !got.IsFavourite {
		t.Error("refresh must preserve favourite")
	}
	if got.IsWorking == nil || !*got.IsWorking {
		t.Error("refresh must preserve health verdict")
	}
	if len(got.Servers) != 1 || got.Servers[0].URL != "http://p/live/u/pw/101.m3u8" {
		t.Errorf("refresh should update servers, got %+v", got.Servers)
	}
}

// An xtream-imported channel is is_manual=1, so it survives PruneOrphans even
// though its id never appears in the iptv-org feed.
func TestXtreamChannelsSurvivePrune(t *testing.T) {
	s := open(t)
	p, _ := s.SaveXtreamPlaylist("Panel", "http://p", "u", "pw")
	s.UpsertXtreamChannels(p.ID, []XtreamStream{{StreamID: 1, Name: "X", URL: "http://p/live/u/pw/1.ts"}})

	_, _, seen, _ := s.UpsertCatalog([]playlist.Channel{ch("tvg:feed", "Feed", "http://feed/1")})
	if _, err := s.PruneOrphans(seen); err != nil {
		t.Fatalf("PruneOrphans: %v", err)
	}
	if _, err := s.Get("xtream:" + p.ID + ":1"); err != nil {
		t.Errorf("xtream channel must survive prune, got %v", err)
	}
}
