# Xtream Category Groups + Per-Playlist Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group imported Xtream live channels by their real panel category (in panel order), and add per-playlist "update frequency" and "stream type" settings.

**Architecture:** The `internal/xtream` client gains a `LiveCategories` call. The store import path maps each stream's `category_id` to its category name (written into the channel's `typ`/group) and records the category's panel position in a new `cat_order` column. Three new columns on `xtream_playlists` hold the per-playlist settings; a pure `playlistsDueForRefresh` function drives a one-shot startup auto-refresh sweep. A `PATCH` endpoint persists setting changes. The browse renderer already groups by `ch.type`; it is adjusted to order Xtream groups by `cat_order`.

**Tech Stack:** Go 1.x (net/http, modernc.org/sqlite via database/sql), vanilla ES modules frontend, `httptest` + standard `testing` for Go tests.

## Global Constraints

- Go module path is `watchlive` (imports like `watchlive/internal/xtream`).
- SQLite has no `ADD COLUMN IF NOT EXISTS`; migrations tolerate a `"duplicate column"` error as benign (see `store.Open`).
- Password/credentials are plaintext by design (local single-user); never add crypto. Passwords are omitted from JSON via the `json:"-"` tag.
- Xtream client decoding is tolerant: missing fields → zero values; non-JSON body or `auth != 1` → `ErrAuth`; 15s HTTP timeout.
- The store uses a single DB connection (`SetMaxOpenConns(1)`); no cross-goroutine concurrency assumptions.
- `now` in `internal/store` is the package var `now = time.Now` (test seam). Do NOT call `time.Now()` directly in store code.
- Frequent commits: one commit per task minimum.

---

### Task 1: `xtream.LiveCategories` — fetch category id→name in panel order

**Files:**
- Modify: `internal/xtream/xtream.go`
- Test: `internal/xtream/xtream_test.go`

**Interfaces:**
- Consumes: existing `Login`, `playerAPI`, `get`, `flexInt`, `ErrAuth` in the same package.
- Produces:
  - `type Category struct { ID string; Name string }`
  - `func LiveCategories(server, username, password string) ([]Category, error)` — slice preserves the panel's returned order.

- [ ] **Step 1: Extend the test stub to serve categories, then write the failing test**

In `internal/xtream/xtream_test.go`, replace the `stubPanel` helper so it can also answer `action=get_live_categories`:

```go
// stubPanel returns a test server standing in for player_api.php. login is the
// body served for a bare login request; streams for action=get_live_streams;
// cats for action=get_live_categories. An empty body is served verbatim (used
// to simulate malformed/HTML responses).
func stubPanel(t *testing.T, login, streams, cats string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player_api.php" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("action") {
		case "get_live_streams":
			w.Write([]byte(streams))
		case "get_live_categories":
			w.Write([]byte(cats))
		default:
			w.Write([]byte(login))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
```

Update every existing `stubPanel(t, X, Y)` call in the file to `stubPanel(t, X, Y, "[]")` (there are several: in `TestLoginHappyPath`, `TestLoginAuthZero`, `TestLoginAuthStringOne`, `TestLoginMalformedJSON`, `TestLiveStreamsHappyPath`, `TestLiveStreamsAuthFails`, `TestLiveStreamsMalformed`).

Then add the new tests:

```go
func TestLiveCategoriesHappyPath(t *testing.T) {
	cats := `[
		{"category_id":"1","category_name":"Main Events / PPV"},
		{"category_id":"2","category_name":"US - Entertainment"},
		{"category_id":3,"category_name":"US - Movies"}
	]`
	srv := stubPanel(t, okLogin, "[]", cats)
	got, err := LiveCategories(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("LiveCategories: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d categories, want 3", len(got))
	}
	// Order must match the panel's returned order.
	if got[0].Name != "Main Events / PPV" || got[0].ID != "1" {
		t.Errorf("category[0] = %+v", got[0])
	}
	// category_id sent as a number must decode to its string form.
	if got[2].ID != "3" || got[2].Name != "US - Movies" {
		t.Errorf("category[2] = %+v", got[2])
	}
}

func TestLiveCategoriesAuthFails(t *testing.T) {
	srv := stubPanel(t, `{"user_info":{"auth":0}}`, "[]", `[{"category_id":"1","category_name":"X"}]`)
	if _, err := LiveCategories(srv.URL, "u", "bad"); err == nil {
		t.Fatal("LiveCategories should surface auth failure before listing")
	}
}

func TestLiveCategoriesMalformed(t *testing.T) {
	srv := stubPanel(t, okLogin, "[]", `not json`)
	if _, err := LiveCategories(srv.URL, "u", "p"); err == nil {
		t.Fatal("LiveCategories with malformed body should error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/xtream/ -run TestLiveCategories -v`
Expected: FAIL — `undefined: LiveCategories` (compile error).

- [ ] **Step 3: Implement `Category` and `LiveCategories`**

In `internal/xtream/xtream.go`, add after the `Stream` type block:

```go
// Category is one live-stream category as returned by get_live_categories.
// ID is decoded via flexString so a numeric or quoted id both land as a string
// (it is only ever matched against Stream.CategoryID, itself a string).
type Category struct {
	ID   string `json:"category_id"`
	Name string `json:"category_name"`
}

// rawCategory is the wire shape; category_id may arrive as a number or a quoted
// string, so it is decoded tolerantly then copied into the exported Category.
type rawCategory struct {
	ID   flexString `json:"category_id"`
	Name string     `json:"category_name"`
}

// flexString decodes a JSON value that a panel might send as either a number or
// a quoted string into its string form.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	*f = flexString(strings.Trim(string(b), `"`))
	return nil
}
```

Add the function after `LiveStreams`:

```go
// LiveCategories authenticates and returns the panel's live-stream categories in
// the order the panel lists them (used to preserve group ordering on import).
// Auth is verified first so bad credentials surface as ErrAuth, not an empty
// list. Missing/extra fields are tolerated.
func LiveCategories(server, username, password string) ([]Category, error) {
	if _, _, err := Login(server, username, password); err != nil {
		return nil, err
	}
	u := playerAPI(server, username, password, url.Values{"action": {"get_live_categories"}})
	body, status, err := get(u)
	if err != nil {
		return nil, fmt.Errorf("xtream: live categories: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("xtream: live categories: panel returned status %d", status)
	}
	var raw []rawCategory
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("xtream: live categories: decode: %w", err)
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		out = append(out, Category{ID: string(r.ID), Name: strings.TrimSpace(r.Name)})
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/xtream/ -v`
Expected: PASS (all existing tests still pass with the new `stubPanel` signature, plus the three new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/xtream/xtream.go internal/xtream/xtream_test.go
git commit -m "feat(xtream): add LiveCategories to fetch category id→name in panel order"
```

---

### Task 2: Store — `cat_order` column, category-name grouping on import

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: existing `XtreamStream`, `UpsertXtreamChannels`, `Channel`, `idSet`, `now`.
- Produces:
  - `XtreamStream` gains fields `Group string` (category name → channel `typ`) and `CatOrder int` (panel index).
  - `Channel` gains `CatOrder int json:"cat_order"`.
  - New channels column `cat_order INTEGER NOT NULL DEFAULT 0`.
  - `UpsertXtreamChannels` writes `typ = st.Group` (falling back to `"Uncategorized"` when empty) and `cat_order = st.CatOrder`.

- [ ] **Step 1: Write the failing test**

In `internal/store/store_test.go`, add:

```go
func TestUpsertXtreamChannelsGrouping(t *testing.T) {
	s := open(t)
	_, err := s.SaveXtreamPlaylist("KS", "http://p:8080", "u", "pw")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist: %v", err)
	}
	added, _, err := s.UpsertXtreamChannels("pl1", []XtreamStream{
		{StreamID: 1, Name: "Movie One", URL: "http://p/live/u/pw/1.ts", Group: "US - Movies", CatOrder: 5},
		{StreamID: 2, Name: "Sport One", URL: "http://p/live/u/pw/2.ts", Group: "US - Sports", CatOrder: 6},
		{StreamID: 3, Name: "Loose", URL: "http://p/live/u/pw/3.ts", Group: "", CatOrder: 0},
	})
	if err != nil {
		t.Fatalf("UpsertXtreamChannels: %v", err)
	}
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}
	chans, err := s.ListChannels()
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	byID := map[string]Channel{}
	for _, c := range chans {
		byID[c.ID] = c
	}
	if got := byID["xtream:pl1:1"]; got.Type != "US - Movies" || got.CatOrder != 5 {
		t.Errorf("channel 1 = {Type:%q CatOrder:%d}, want {US - Movies 5}", got.Type, got.CatOrder)
	}
	// Empty category name falls back to "Uncategorized".
	if got := byID["xtream:pl1:3"]; got.Type != "Uncategorized" {
		t.Errorf("channel 3 Type = %q, want Uncategorized", got.Type)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestUpsertXtreamChannelsGrouping -v`
Expected: FAIL — `unknown field 'Group' in struct literal of type store.XtreamStream` (compile error).

- [ ] **Step 3: Add the column, struct fields, and wire the upsert**

In `internal/store/store.go`, add `cat_order` to the migration loop in `Open` (it is INTEGER, not TEXT, so it needs its own `ALTER`, separate from the string-column loop). Immediately after the existing string-column `for _, col := range []string{...}` loop, add:

```go
	// cat_order is INTEGER (category position from the Xtream panel), so it
	// can't ride the string-column loop above. Same duplicate-column tolerance.
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN cat_order INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("store: migrate cat_order: %w", err)
	}
```

Add `CatOrder` to the `Channel` struct (after `IsWorking`):

```go
	// CatOrder is the category's position in its source Xtream panel, used to
	// render Xtream groups in panel order. 0 for non-Xtream channels.
	CatOrder int `json:"cat_order"`
```

Add fields to `XtreamStream`:

```go
	// Group is the channel's category name (from the panel), stored as the
	// channel's typ so the browse UI groups by it. Empty → "Uncategorized".
	Group string
	// CatOrder is the category's index in the panel's category list, used to
	// order groups in the browse UI.
	CatOrder int
```

Both channel-read queries (`ListChannels` at ~line 173 and `getChannel` at ~line 608) feed the SINGLE shared `scanChannel` helper (~line 203), so the scan target list is edited in exactly ONE place while BOTH SELECT column lists must gain `cat_order` (column order must match the scan order).

Update the `ListChannels` SELECT (~line 173):

```go
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, resolver, resolver_arg, is_favourite, is_working, cat_order
		FROM channels ORDER BY sort_name, name`)
```

Update the `getChannel` SELECT (~line 608) the same way — add `cat_order` as the final column so it stays aligned with `scanChannel`:

```go
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, resolver, resolver_arg, is_favourite, is_working, cat_order
		FROM channels WHERE id=?`, id)
```

Then, in `scanChannel` (~line 203), append `&ch.CatOrder` as the final scan target (edit this ONCE — both SELECTs share it):

```go
	if err := row.Scan(&ch.ID, &ch.Name, &ch.Logo, &ch.Group, &ch.Type, &serversJS, &clearJS, &ch.UserAgent, &ch.Referer, &ch.Resolver, &ch.ResolverArg, &fav, &working, &ch.CatOrder); err != nil {
```

In `UpsertXtreamChannels`, add `cat_order` to the INSERT column list, the VALUES placeholders, and the `ON CONFLICT ... DO UPDATE SET`:

```go
	stmt, err := tx.Prepare(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, is_working, last_checked_at, is_favourite, is_manual, sort_name, xtream_playlist_id, cat_order)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, 0, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, logo=excluded.logo, grp=excluded.grp, typ=excluded.typ,
			servers=excluded.servers, sort_name=excluded.sort_name,
			xtream_playlist_id=excluded.xtream_playlist_id, cat_order=excluded.cat_order`)
```

In the loop body, compute the group and pass the new args. Replace the existing `stmt.Exec(...)` call (currently passing `manualGroup, manualType, ...`) with:

```go
		grp := strings.TrimSpace(st.Group)
		if grp == "" {
			grp = "Uncategorized"
		}
		// grp column (country) stays neutral — categories are not countries and
		// must not pollute the country dropdown. The category name is the typ.
		if _, err = stmt.Exec(id, name, st.Logo, manualGroup, grp, string(serversJS), strings.ToLower(name), playlistID, st.CatOrder); err != nil {
			return 0, 0, err
		}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestUpsertXtreamChannelsGrouping -v`
Expected: PASS.

- [ ] **Step 5: Run the full store + xtream suites (regression)**

Run: `go test ./internal/store/ ./internal/xtream/`
Expected: PASS (existing tests unaffected — the SELECT/scan changes stay aligned).

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): group Xtream channels by category name + cat_order column"
```

---

### Task 3: main — map categories on import, store category name + order

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `xtream.LiveCategories`, `xtream.LiveStreams`, `store.XtreamStream{Group, CatOrder}`.
- Produces: `importXtreamStreams` now fetches categories itself and maps each stream's `CategoryID` → `{name, index}`.

- [ ] **Step 1: Write the failing test**

In `main_test.go`, add a test that drives the mapping through `importXtreamStreams`. First check the existing imports/helpers in `main_test.go`; it is in `package main`, so it can call `importXtreamStreams` directly. Add:

```go
func TestImportXtreamStreamsMapsCategories(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	p := store.XtreamPlaylist{ID: "pl1", Server: "http://p:8080", Username: "u", Password: "pw"}

	// Categories in panel order; stream 2 references an unknown category id.
	cats := []xtream.Category{
		{ID: "10", Name: "US - Movies"},
		{ID: "11", Name: "US - Sports"},
	}
	streams := []xtream.Stream{
		{StreamID: 1, Name: "Film", CategoryID: "10", Extension: "ts"},
		{StreamID: 2, Name: "Orphan", CategoryID: "999", Extension: "ts"},
		{StreamID: 3, Name: "Match", CategoryID: "11", Extension: "ts"},
	}
	added, _, err := importXtreamStreams(st, p, streams, cats)
	if err != nil {
		t.Fatalf("importXtreamStreams: %v", err)
	}
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}
	chans, _ := st.ListChannels()
	byID := map[string]store.Channel{}
	for _, c := range chans {
		byID[c.ID] = c
	}
	if got := byID["xtream:pl1:1"]; got.Type != "US - Movies" || got.CatOrder != 0 {
		t.Errorf("stream 1 = {Type:%q CatOrder:%d}, want {US - Movies 0}", got.Type, got.CatOrder)
	}
	if got := byID["xtream:pl1:3"]; got.Type != "US - Sports" || got.CatOrder != 1 {
		t.Errorf("stream 3 = {Type:%q CatOrder:%d}, want {US - Sports 1}", got.Type, got.CatOrder)
	}
	// Unknown category id falls back to Uncategorized (store applies the default).
	if got := byID["xtream:pl1:2"]; got.Type != "Uncategorized" {
		t.Errorf("stream 2 Type = %q, want Uncategorized", got.Type)
	}
}
```

`main_test.go` is `package main` and already imports `watchlive/internal/store`. It does NOT import `watchlive/internal/xtream` — add `"watchlive/internal/xtream"` to its import block.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestImportXtreamStreamsMapsCategories -v`
Expected: FAIL — `too many arguments in call to importXtreamStreams` (signature change needed).

- [ ] **Step 3: Change `importXtreamStreams` to take categories and map them**

In `main.go`, replace the `importXtreamStreams` function with:

```go
// importXtreamStreams turns a panel's live-stream list into catalog rows for the
// given saved playlist: it builds each stream's playable URL from the playlist
// credentials, resolves each stream's category_id to its panel category name and
// order (cats, in panel order), then upserts by the stable
// xtream:<playlist>:<stream> id. Returns added/updated counts.
func importXtreamStreams(st *store.Store, p store.XtreamPlaylist, streams []xtream.Stream, cats []xtream.Category) (added, updated int, err error) {
	type catInfo struct {
		name  string
		order int
	}
	byID := make(map[string]catInfo, len(cats))
	for i, c := range cats {
		byID[c.ID] = catInfo{name: c.Name, order: i}
	}
	rows := make([]store.XtreamStream, 0, len(streams))
	for _, s := range streams {
		ci := byID[s.CategoryID] // zero value (empty name, order 0) when unknown
		rows = append(rows, store.XtreamStream{
			StreamID: s.StreamID,
			Name:     s.Name,
			Logo:     s.Icon,
			URL:      xtream.StreamURL(p.Server, p.Username, p.Password, s.StreamID, s.Extension),
			Group:    ci.name,
			CatOrder: ci.order,
		})
	}
	return st.UpsertXtreamChannels(p.ID, rows)
}
```

- [ ] **Step 4: Update the two callers to fetch categories**

In the `POST /api/xtream/playlists` handler, after the successful `xtream.LiveStreams(...)` call and before `importXtreamStreams`, fetch categories (non-fatal — a panel that lists streams but errors on categories should still import, just ungrouped):

```go
		cats, err := xtream.LiveCategories(server, username, password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (importing without groups)", name, err)
			cats = nil
		}
```

Change the call to `added, _, err := importXtreamStreams(st, p, streams, cats)`.

In the `POST /api/xtream/playlists/{id}/refresh` handler, after its `xtream.LiveStreams(...)` call, add the same category fetch keyed on `p`:

```go
		cats, err := xtream.LiveCategories(p.Server, p.Username, p.Password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (refreshing without groups)", p.Name, err)
			cats = nil
		}
```

Change the call to `added, updated, err := importXtreamStreams(st, p, streams, cats)`.

- [ ] **Step 5: Run the test + build to verify it passes**

Run: `go build ./... && go test . -run TestImportXtreamStreamsMapsCategories -v`
Expected: build OK, test PASS.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(xtream): map category names + order into imported channels"
```

---

### Task 4: Frontend — render Xtream groups in panel order

**Files:**
- Modify: `web/static/channels.js`
- Test: manual (no JS test harness in repo — verified in Task 8).

**Interfaces:**
- Consumes: `ch.type` (group name), `ch.cat_order` (panel index, added to the API payload in Task 2/3), `CATEGORY_ORDER`.
- Produces: group ordering where known categories render first, then Xtream groups by ascending `cat_order`, then remaining groups alphabetically.

- [ ] **Step 1: Update the straggler ordering in `renderChannelList`**

In `web/static/channels.js`, the block that computes `cats` (around lines 211–216) currently is:

```js
  // Known categories first (in CATEGORY_ORDER), then any stragglers alphabetically.
  const known = {};
  CATEGORY_ORDER.forEach(function (c) { known[c] = true; });
  const extra = Object.keys(byCat).filter(function (c) { return !known[c]; })
    .sort(function (a, b) { a = a.toLowerCase(); b = b.toLowerCase(); return a < b ? -1 : a > b ? 1 : 0; });
  const cats = CATEGORY_ORDER.concat(extra);
```

Replace it with ordering that puts Xtream groups (those whose channels carry a non-zero `cat_order`) in panel order, before the alphabetical remainder:

```js
  // Known categories first (in CATEGORY_ORDER). Then "straggler" groups: Xtream
  // category groups (channels carry a panel index in cat_order) sorted by that
  // index to preserve the panel's order, followed by any remaining groups
  // (cat_order 0 — non-Xtream) alphabetically.
  const known = {};
  CATEGORY_ORDER.forEach(function (c) { known[c] = true; });
  // Group's ordering key = the smallest cat_order among its channels.
  const groupOrder = {};
  Object.keys(byCat).forEach(function (c) {
    let min = 0;
    byCat[c].forEach(function (ch) {
      const o = ch.cat_order || 0;
      if (o > 0 && (min === 0 || o < min)) min = o;
    });
    groupOrder[c] = min; // 0 = no panel ordering signal
  });
  const extra = Object.keys(byCat).filter(function (c) { return !known[c]; })
    .sort(function (a, b) {
      const oa = groupOrder[a], ob = groupOrder[b];
      if (oa !== ob) {
        // Ordered (non-zero) groups come before unordered (zero) ones; among
        // ordered groups, ascending panel index.
        if (oa === 0) return 1;
        if (ob === 0) return -1;
        return oa - ob;
      }
      a = a.toLowerCase(); b = b.toLowerCase();
      return a < b ? -1 : a > b ? 1 : 0;
    });
  const cats = CATEGORY_ORDER.concat(extra);
```

- [ ] **Step 2: Verify the file parses (no build step; lint by loading)**

Run: `node --check web/static/channels.js`
Expected: no output (exit 0) — syntax valid.

- [ ] **Step 3: Commit**

```bash
git add web/static/channels.js
git commit -m "feat(browse): order Xtream category groups by panel index"
```

---

### Task 5: Store — per-playlist settings columns + PATCH persistence

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: existing `XtreamPlaylist`, `SaveXtreamPlaylist`, `ListXtreamPlaylists`, `GetXtreamPlaylist`, `now`, `ErrNotFound`.
- Produces:
  - `XtreamPlaylist` gains `UpdateFreq string json:"update_freq"`, `StreamType string json:"stream_type"`, `LastRefreshedAt int64 json:"-"`.
  - Columns `update_freq TEXT NOT NULL DEFAULT 'manual'`, `stream_type TEXT NOT NULL DEFAULT 'ts'`, `last_refreshed_at INTEGER NOT NULL DEFAULT 0` on `xtream_playlists`.
  - `func (s *Store) UpdateXtreamSettings(id, updateFreq, streamType string) (XtreamPlaylist, error)` — validates values, persists, returns the updated playlist (`ErrNotFound` if absent, `ErrInvalidSetting` on a bad value).
  - `func (s *Store) StampXtreamRefreshed(id string) error` — sets `last_refreshed_at = now()`.
  - Exported `var ErrInvalidSetting = errors.New("invalid xtream setting")`.
  - `SaveXtreamPlaylist` returns rows with defaults `UpdateFreq:"manual"`, `StreamType:"ts"`.

- [ ] **Step 1: Write the failing tests**

In `internal/store/store_test.go`, add:

```go
func TestUpdateXtreamSettings(t *testing.T) {
	s := open(t)
	p, err := s.SaveXtreamPlaylist("KS", "http://p:8080", "u", "pw")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist: %v", err)
	}
	// Defaults on a fresh row.
	if p.UpdateFreq != "manual" || p.StreamType != "ts" {
		t.Fatalf("defaults = {%q %q}, want {manual ts}", p.UpdateFreq, p.StreamType)
	}
	got, err := s.UpdateXtreamSettings(p.ID, "weekly", "m3u8")
	if err != nil {
		t.Fatalf("UpdateXtreamSettings: %v", err)
	}
	if got.UpdateFreq != "weekly" || got.StreamType != "m3u8" {
		t.Errorf("updated = {%q %q}, want {weekly m3u8}", got.UpdateFreq, got.StreamType)
	}
	// Persisted across a re-read.
	list, _ := s.ListXtreamPlaylists()
	if len(list) != 1 || list[0].UpdateFreq != "weekly" || list[0].StreamType != "m3u8" {
		t.Errorf("reloaded = %+v", list)
	}
}

func TestUpdateXtreamSettingsInvalid(t *testing.T) {
	s := open(t)
	p, _ := s.SaveXtreamPlaylist("KS", "http://p:8080", "u", "pw")
	if _, err := s.UpdateXtreamSettings(p.ID, "hourly", "ts"); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("bad freq err = %v, want ErrInvalidSetting", err)
	}
	if _, err := s.UpdateXtreamSettings(p.ID, "manual", "rtmp"); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("bad stream type err = %v, want ErrInvalidSetting", err)
	}
	if _, err := s.UpdateXtreamSettings("nope", "manual", "ts"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run TestUpdateXtreamSettings -v`
Expected: FAIL — `p.UpdateFreq undefined` / `UpdateXtreamSettings undefined` (compile error).

- [ ] **Step 3: Add columns, struct fields, error, and methods**

In the `schema` const, extend the `xtream_playlists` table definition with the three columns (so a fresh DB has them):

```sql
CREATE TABLE IF NOT EXISTS xtream_playlists (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    server            TEXT NOT NULL,
    username          TEXT NOT NULL,
    password          TEXT NOT NULL,
    created_at        INTEGER NOT NULL,
    update_freq       TEXT NOT NULL DEFAULT 'manual',
    stream_type       TEXT NOT NULL DEFAULT 'ts',
    last_refreshed_at INTEGER NOT NULL DEFAULT 0
);
```

In `Open`, after the `cat_order` migration from Task 2, add migrations for existing DBs (the schema block only runs `CREATE TABLE IF NOT EXISTS`, so an already-created table won't gain columns without an ALTER):

```go
	for _, col := range []struct{ name, def string }{
		{"update_freq", `TEXT NOT NULL DEFAULT 'manual'`},
		{"stream_type", `TEXT NOT NULL DEFAULT 'ts'`},
		{"last_refreshed_at", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if _, err := db.Exec(`ALTER TABLE xtream_playlists ADD COLUMN ` + col.name + ` ` + col.def); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("store: migrate %s: %w", col.name, err)
		}
	}
```

Add the error near the other package-level errors (around line 40):

```go
	ErrInvalidSetting = errors.New("invalid xtream setting")
```

Add fields to `XtreamPlaylist`:

```go
	// UpdateFreq is the auto-refresh cadence: "manual" | "daily" | "3days" |
	// "weekly". StreamType is the extension used to build stream URLs: "ts" |
	// "m3u8". LastRefreshedAt (unix seconds) drives the startup interval sweep.
	UpdateFreq      string `json:"update_freq"`
	StreamType      string `json:"stream_type"`
	LastRefreshedAt int64  `json:"-"`
```

Update `SaveXtreamPlaylist` to set the defaults on the returned struct (the columns default in SQL, but the returned value must match): after building `p`, set `p.UpdateFreq = "manual"` and `p.StreamType = "ts"` before returning.

Update `ListXtreamPlaylists` and `GetXtreamPlaylist` SELECTs to include the new columns and scan them. For `ListXtreamPlaylists`:

```go
	rows, err := s.db.Query(`
		SELECT id, name, server, username, password, created_at, update_freq, stream_type, last_refreshed_at
		FROM xtream_playlists ORDER BY created_at DESC, name`)
```
and scan: `rows.Scan(&p.ID, &p.Name, &p.Server, &p.Username, &p.Password, &p.CreatedAt, &p.UpdateFreq, &p.StreamType, &p.LastRefreshedAt)`.

For `GetXtreamPlaylist`, same columns:
```go
	err := s.db.QueryRow(`
		SELECT id, name, server, username, password, created_at, update_freq, stream_type, last_refreshed_at
		FROM xtream_playlists WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Server, &p.Username, &p.Password, &p.CreatedAt, &p.UpdateFreq, &p.StreamType, &p.LastRefreshedAt)
```

Add the two methods (place them after `GetXtreamPlaylist`):

```go
// validUpdateFreq / validStreamType are the accepted setting values.
var validUpdateFreq = map[string]bool{"manual": true, "daily": true, "3days": true, "weekly": true}
var validStreamType = map[string]bool{"ts": true, "m3u8": true}

// UpdateXtreamSettings validates and persists a playlist's auto-refresh cadence
// and stream type, returning the updated playlist. ErrInvalidSetting on a bad
// value; ErrNotFound if no such playlist.
func (s *Store) UpdateXtreamSettings(id, updateFreq, streamType string) (XtreamPlaylist, error) {
	if !validUpdateFreq[updateFreq] || !validStreamType[streamType] {
		return XtreamPlaylist{}, ErrInvalidSetting
	}
	res, err := s.db.Exec(`UPDATE xtream_playlists SET update_freq=?, stream_type=? WHERE id=?`,
		updateFreq, streamType, id)
	if err != nil {
		return XtreamPlaylist{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return XtreamPlaylist{}, ErrNotFound
	}
	return s.GetXtreamPlaylist(id)
}

// StampXtreamRefreshed records that a playlist was just refreshed, so the
// startup interval sweep can tell when it is next due.
func (s *Store) StampXtreamRefreshed(id string) error {
	_, err := s.db.Exec(`UPDATE xtream_playlists SET last_refreshed_at=? WHERE id=?`, now().Unix(), id)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run TestUpdateXtreamSettings -v`
Expected: PASS.

- [ ] **Step 5: Run the full store suite (regression)**

Run: `go test ./internal/store/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): per-playlist update_freq/stream_type settings + refresh stamp"
```

---

### Task 6: Store — `playlistsDueForRefresh` pure function

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `XtreamPlaylist{UpdateFreq, LastRefreshedAt}`.
- Produces: `func playlistsDueForRefresh(playlists []XtreamPlaylist, nowUnix int64) []string` — returns the IDs of playlists due for auto-refresh. Package-private (tested from within `package store`).

- [ ] **Step 1: Write the failing test**

In `internal/store/store_test.go`, add:

```go
func TestPlaylistsDueForRefresh(t *testing.T) {
	const day = int64(24 * 3600)
	nowUnix := int64(1_000_000)
	pls := []XtreamPlaylist{
		{ID: "manual", UpdateFreq: "manual", LastRefreshedAt: 0},          // never due
		{ID: "never", UpdateFreq: "daily", LastRefreshedAt: 0},            // due (0 → always)
		{ID: "fresh", UpdateFreq: "daily", LastRefreshedAt: nowUnix - 1},  // not due
		{ID: "stale", UpdateFreq: "daily", LastRefreshedAt: nowUnix - 2*day}, // due
		{ID: "wk-fresh", UpdateFreq: "weekly", LastRefreshedAt: nowUnix - 3*day}, // not due
		{ID: "wk-due", UpdateFreq: "weekly", LastRefreshedAt: nowUnix - 8*day},   // due
		{ID: "3d-due", UpdateFreq: "3days", LastRefreshedAt: nowUnix - 4*day},    // due
	}
	got := playlistsDueForRefresh(pls, nowUnix)
	want := map[string]bool{"never": true, "stale": true, "wk-due": true, "3d-due": true}
	if len(got) != len(want) {
		t.Fatalf("due = %v, want keys %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected due id %q", id)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestPlaylistsDueForRefresh -v`
Expected: FAIL — `undefined: playlistsDueForRefresh`.

- [ ] **Step 3: Implement the function**

In `internal/store/store.go`, add:

```go
// refreshInterval maps an update_freq to its cadence in seconds. "manual" (and
// any unknown value) returns 0, meaning "never auto-refresh".
func refreshInterval(freq string) int64 {
	switch freq {
	case "daily":
		return 24 * 3600
	case "3days":
		return 3 * 24 * 3600
	case "weekly":
		return 7 * 24 * 3600
	default:
		return 0
	}
}

// playlistsDueForRefresh returns the ids of playlists whose auto-refresh cadence
// has elapsed as of nowUnix. "manual" playlists are never due; a playlist that
// has never been refreshed (LastRefreshedAt == 0) with a non-manual cadence is
// always due.
func playlistsDueForRefresh(playlists []XtreamPlaylist, nowUnix int64) []string {
	var due []string
	for _, p := range playlists {
		interval := refreshInterval(p.UpdateFreq)
		if interval == 0 {
			continue
		}
		if nowUnix >= p.LastRefreshedAt+interval {
			due = append(due, p.ID)
		}
	}
	return due
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestPlaylistsDueForRefresh -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): playlistsDueForRefresh interval selection"
```

---

### Task 7: main — PATCH endpoint + startup auto-refresh sweep

**Files:**
- Modify: `main.go`
- Test: manual + build (endpoint exercised in Task 8; sweep logic unit-tested in Task 6).

**Interfaces:**
- Consumes: `store.UpdateXtreamSettings`, `store.StampXtreamRefreshed`, `store.ListXtreamPlaylists`, `store.ErrInvalidSetting`, `store.ErrNotFound`, `importXtreamStreams`, `xtream.LiveStreams`, `xtream.LiveCategories`, `channels.rebuild`.
- Produces: `PATCH /api/xtream/playlists/{id}` endpoint; a startup goroutine that refreshes due playlists once.

- [ ] **Step 1: Stamp last_refreshed_at on every refresh/import**

In `main.go`, in both the `POST /api/xtream/playlists` and `POST /api/xtream/playlists/{id}/refresh` handlers, after the successful `importXtreamStreams(...)` call and before `channels.rebuild()`, add:

```go
		if err := st.StampXtreamRefreshed(p.ID); err != nil {
			log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
		}
```

(`p` is in scope in both handlers — the saved playlist / the fetched playlist.)

- [ ] **Step 2: Add the PATCH endpoint**

In `main.go`, after the `POST /api/xtream/playlists/{id}/refresh` handler block, add:

```go
	// Update a saved playlist's per-playlist settings (auto-refresh cadence and
	// stream type). Applying a new stream type takes effect on the next refresh;
	// this endpoint only persists the choice.
	mux.HandleFunc("PATCH /api/xtream/playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			UpdateFreq string `json:"update_freq"`
			StreamType string `json:"stream_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p, err := st.UpdateXtreamSettings(id, body.UpdateFreq, body.StreamType)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, store.ErrInvalidSetting) {
			http.Error(w, "update_freq must be manual/daily/3days/weekly and stream_type ts/m3u8", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		writeJSON(w, r, p)
	})
```

- [ ] **Step 3: Add the startup auto-refresh sweep**

`playlistsDueForRefresh` is package-private to `store`, so expose the sweep as a store method that main calls. In `internal/store/store.go`, add:

```go
// DueXtreamPlaylists returns saved playlists whose auto-refresh cadence has
// elapsed as of now, for the startup sweep. Each returned playlist carries its
// credentials so the caller can refresh it.
func (s *Store) DueXtreamPlaylists() ([]XtreamPlaylist, error) {
	all, err := s.ListXtreamPlaylists()
	if err != nil {
		return nil, err
	}
	dueIDs := playlistsDueForRefresh(all, now().Unix())
	dueSet := make(map[string]bool, len(dueIDs))
	for _, id := range dueIDs {
		dueSet[id] = true
	}
	var due []XtreamPlaylist
	for _, p := range all {
		if dueSet[p.ID] {
			due = append(due, p)
		}
	}
	return due, nil
}
```

Commit this store addition together with the handler work in this task's final commit.

In `main.go`, after the store is opened and the HTTP handlers are registered but before/around the existing startup goroutines (near the other `go func()` blocks around lines 493/514), add a sweep goroutine:

```go
	// One-shot startup sweep: auto-refresh any saved Xtream playlist whose
	// update cadence has elapsed. Runs in the background so a slow/dead panel
	// never delays startup; failures are logged and skipped.
	go func() {
		due, err := st.DueXtreamPlaylists()
		if err != nil {
			log.Printf("xtream: startup sweep: %v", err)
			return
		}
		refreshed := false
		for _, p := range due {
			streams, err := xtream.LiveStreams(p.Server, p.Username, p.Password)
			if err != nil {
				log.Printf("xtream: auto-refresh %q: %v", p.Name, err)
				continue
			}
			cats, err := xtream.LiveCategories(p.Server, p.Username, p.Password)
			if err != nil {
				log.Printf("xtream: auto-refresh categories %q: %v", p.Name, err)
				cats = nil
			}
			added, updated, err := importXtreamStreams(st, p, streams, cats)
			if err != nil {
				log.Printf("xtream: auto-refresh import %q: %v", p.Name, err)
				continue
			}
			if err := st.StampXtreamRefreshed(p.ID); err != nil {
				log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
			}
			log.Printf("xtream: auto-refreshed %q: +%d new, %d updated", p.Name, added, updated)
			refreshed = true
		}
		if refreshed {
			channels.rebuild()
		}
	}()
```

Confirm `channels` and `st` are in scope at that point (they are — `channels` is built earlier and used by the handlers; the goroutine closes over both).

- [ ] **Step 4: Build to verify it compiles**

Run: `go build ./...`
Expected: build OK, no errors.

- [ ] **Step 5: Run the full Go test suite (regression)**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add main.go internal/store/store.go
git commit -m "feat(xtream): PATCH settings endpoint + startup auto-refresh sweep"
```

---

### Task 8: Frontend — per-playlist settings UI in the Xtream tab

**Files:**
- Modify: `web/templates/index.html`, `web/static/state.js`, `web/static/xtream.js`
- Test: manual in-browser (Step 6).

**Interfaces:**
- Consumes: `GET /api/xtream/playlists` (now includes `update_freq`, `stream_type`), `PATCH /api/xtream/playlists/{id}`, existing `els`, `loadedPlaylists`, `selectedPlaylist`, `showError`, `initXtreamTab`.
- Produces: two `<select>` controls whose changes PATCH the selected playlist.

- [ ] **Step 1: Add the settings block to the template**

In `web/templates/index.html`, inside `#xtreamSavedWrap` (the block that holds `#xtreamSaved` + `#xtreamRefresh`), add a settings sub-block right after the dropdown row so it shows only when a saved playlist exists. Insert after the `#xtreamRefresh` button's row and before `#xtreamSavedWrap` closes:

```html
        <div id="xtreamSettings" class="add-field xtream-settings" hidden>
          <label class="xtream-setting-row">
            <span>How often to update</span>
            <select id="xtreamUpdateFreq" class="country-select">
              <option value="manual">Manual</option>
              <option value="daily">Everyday</option>
              <option value="3days">Every 3 days</option>
              <option value="weekly">Weekly</option>
            </select>
          </label>
          <label class="xtream-setting-row">
            <span>Stream type</span>
            <select id="xtreamStreamType" class="country-select">
              <option value="ts">MPEG-TS (ts)</option>
              <option value="m3u8">HLS (m3u8)</option>
            </select>
          </label>
        </div>
```

- [ ] **Step 2: Register the new elements in state.js**

In `web/static/state.js`, add to the `els` object (next to the other `xtream*` entries around line 76):

```js
  xtreamSettings: $('xtreamSettings'), xtreamUpdateFreq: $('xtreamUpdateFreq'), xtreamStreamType: $('xtreamStreamType'),
```

- [ ] **Step 3: Wire the settings block in xtream.js**

In `web/static/xtream.js`, update `renderSaved` to hide the settings block when the list is empty (add to the existing `if (list.length === 0) {` branch, before `return;`):

```js
    els.xtreamSettings.hidden = true;
```

Add a helper that fills the selects from the selected playlist, and show/hide the block. Add after the `selectedPlaylist` function:

```js
// syncSettings reflects the selected playlist's settings into the two selects,
// and shows the settings block only when a playlist is selected.
function syncSettings() {
  const p = selectedPlaylist();
  if (!p) {
    els.xtreamSettings.hidden = true;
    return;
  }
  els.xtreamSettings.hidden = false;
  els.xtreamUpdateFreq.value = p.update_freq || 'manual';
  els.xtreamStreamType.value = p.stream_type || 'ts';
}
```

Call `syncSettings()` at the end of `renderSaved` (after the loop, in the non-empty path) and inside the `els.xtreamSaved` `change` handler (after the existing import logic). The change handler becomes:

```js
els.xtreamSaved.addEventListener('change', function () {
  const p = selectedPlaylist();
  syncSettings();
  if (p && !p.imported) importPlaylist(p.id, els.xtreamSaved);
});
```

Add a PATCH helper and change listeners on the two selects:

```js
// patchSettings persists the selected playlist's current setting values.
function patchSettings(notify) {
  const p = selectedPlaylist();
  if (!p) return;
  const body = {
    update_freq: els.xtreamUpdateFreq.value,
    stream_type: els.xtreamStreamType.value,
  };
  showError('');
  fetch('/api/xtream/playlists/' + encodeURIComponent(p.id), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ('save failed: ' + r.status)); });
      return r.json();
    })
    .then(function (updated) {
      // Keep the cache in sync so re-selecting shows the saved values.
      p.update_freq = updated.update_freq;
      p.stream_type = updated.stream_type;
      if (notify) showError('Stream type saved — press Refresh to re-import channels with the new type.');
    })
    .catch(function (err) { showError(friendly(err)); });
}

els.xtreamUpdateFreq.addEventListener('change', function () { patchSettings(false); });
els.xtreamStreamType.addEventListener('change', function () { patchSettings(true); });
```

Note: `#xtreamError` doubles as the notice area; the stream-type message is informational (not a hard error) but reuses the same element, consistent with the existing inline pattern.

- [ ] **Step 4: Verify the JS parses**

Run: `node --check web/static/xtream.js && node --check web/static/state.js`
Expected: no output (exit 0).

- [ ] **Step 5: Build and run the app**

Run: `go build ./... && go run . ` (or the project's usual run command — check for a run skill/README). Then open the app in a browser.
Expected: server starts, no template parse error.

- [ ] **Step 6: Manual verification checklist**

Perform in-browser and confirm each:

1. Open the "+" add modal → Xtream Codes tab. With no saved playlists, the settings block is hidden.
2. Add a playlist (Save & Import). After import, the browse sidebar shows category groups (e.g. "US - Movies", "US - Sports") with per-group counts, in the panel's order — not one giant group.
3. Select the saved playlist → the settings block appears; "How often to update" and "Stream type" show the saved values (defaults Manual / MPEG-TS on first import).
4. Change "How often to update" → reload the page, reopen the tab, reselect → the value persisted.
5. Change "Stream type" to HLS (m3u8) → inline notice tells you to Refresh → press Refresh → channels re-import; spot-check a channel plays (URL now ends in `.m3u8`).
6. (Optional) Set a playlist to "Everyday", edit its `last_refreshed_at` far in the past via the DB, restart the app → logs show an auto-refresh on startup.

- [ ] **Step 7: Commit**

```bash
git add web/templates/index.html web/static/state.js web/static/xtream.js
git commit -m "feat(ui): per-playlist update-frequency and stream-type settings"
```

---

## Self-Review Notes

- **Spec coverage:** §1 category grouping → Tasks 1–4; §2 settings columns + stream type + startup sweep + PATCH → Tasks 5–7; §3 UI → Task 8; §4 testing → tests embedded in Tasks 1, 2, 3, 5, 6 + manual checklist in Task 8. Dropped/deferred items are not implemented, as specified.
- **Type consistency:** `XtreamStream.Group`/`CatOrder` (Task 2) match their use in `importXtreamStreams` (Task 3); `Channel.CatOrder`/`cat_order` JSON matches `ch.cat_order` in the renderer (Task 4); `UpdateXtreamSettings`/`StampXtreamRefreshed`/`playlistsDueForRefresh`/`DueXtreamPlaylists` signatures are defined in Tasks 5–7 and consumed only where defined.
- **Ordering nuance:** groups with `cat_order == 0` (non-Xtream) sort after ordered Xtream groups, matching the spec's clarified ordering.
