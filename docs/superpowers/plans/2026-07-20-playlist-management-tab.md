# Playlist Management Tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third "Playlist" tab to the add-channel modal that lists every
saved Xtream playlist with inline rename, stream-type change, and delete.

**Architecture:** A new store method `UpdatePlaylistFields` replaces
`UpdateXtreamSettings`, supporting partial updates (name/update_freq/stream_type
independently) via nilable args. A new store method `DeleteXtreamPlaylist`
removes a playlist and its channels in one transaction. `main.go` gains a
relaxed `PATCH /api/xtream/playlists/{id}` body and a new `DELETE
/api/xtream/playlists/{id}` handler. The frontend gets a third modal tab
backed by a new `web/static/playlist-tab.js` module, wired into the existing
3-way tab switcher in `modals.js`.

**Tech Stack:** Go (`database/sql`, `modernc.org/sqlite`), vanilla JS modules,
Go's `net/http` `ServeMux` pattern routing.

## Global Constraints

- Reuse the existing `validUpdateFreq`/`validStreamType` maps in
  `internal/store/store.go` — do not redefine them.
- `ErrNotFound` and `ErrInvalidSetting` (already defined in
  `internal/store/store.go`) are the only sentinel errors used; no new
  sentinel errors are introduced.
- Delete confirmation uses plain `confirm("Remove playlist \"<name>\"?")` —
  no typed-confirmation ceremony (matches the existing destructive-action
  style in this app).
- No update-frequency control on the new tab — stream type only.
- No imported-channel-count display on rows.

---

### Task 1: Store — `UpdatePlaylistFields` (replaces `UpdateXtreamSettings`)

**Files:**
- Modify: `internal/store/store.go:797-813` (replace `UpdateXtreamSettings`)
- Modify: `internal/store/store_test.go:526-562` (replace
  `TestUpdateXtreamSettings` / `TestUpdateXtreamSettingsInvalid`)

**Interfaces:**
- Consumes: `s.db *sql.DB`, `s.GetXtreamPlaylist(id string) (XtreamPlaylist,
  error)`, `validUpdateFreq map[string]bool`, `validStreamType
  map[string]bool`, `ErrNotFound`, `ErrInvalidSetting` — all already defined
  in `internal/store/store.go`.
- Produces: `func (s *Store) UpdatePlaylistFields(id string, name,
  updateFreq, streamType *string) (XtreamPlaylist, error)` — used by Task 3
  (handler).

- [ ] **Step 1: Write the failing tests**

Replace the existing `TestUpdateXtreamSettings` and
`TestUpdateXtreamSettingsInvalid` functions (`internal/store/store_test.go:526-562`)
with:

```go
func strp(s string) *string { return &s }

func TestUpdatePlaylistFields(t *testing.T) {
	s := open(t)
	p, err := s.SaveXtreamPlaylist("KS", "http://p:8080", "u", "pw")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist: %v", err)
	}
	// Defaults on a fresh row.
	if p.UpdateFreq != "manual" || p.StreamType != "ts" {
		t.Fatalf("defaults = {%q %q}, want {manual ts}", p.UpdateFreq, p.StreamType)
	}

	// Update only stream_type; name and update_freq must be untouched.
	got, err := s.UpdatePlaylistFields(p.ID, nil, nil, strp("m3u8"))
	if err != nil {
		t.Fatalf("UpdatePlaylistFields (stream_type only): %v", err)
	}
	if got.Name != "KS" || got.UpdateFreq != "manual" || got.StreamType != "m3u8" {
		t.Errorf("after stream_type-only update = %+v, want name=KS update_freq=manual stream_type=m3u8", got)
	}

	// Update only update_freq; name and stream_type must be untouched.
	got, err = s.UpdatePlaylistFields(p.ID, nil, strp("weekly"), nil)
	if err != nil {
		t.Fatalf("UpdatePlaylistFields (update_freq only): %v", err)
	}
	if got.Name != "KS" || got.UpdateFreq != "weekly" || got.StreamType != "m3u8" {
		t.Errorf("after update_freq-only update = %+v, want name=KS update_freq=weekly stream_type=m3u8", got)
	}

	// Update only name; settings must be untouched.
	got, err = s.UpdatePlaylistFields(p.ID, strp("Renamed"), nil, nil)
	if err != nil {
		t.Fatalf("UpdatePlaylistFields (name only): %v", err)
	}
	if got.Name != "Renamed" || got.UpdateFreq != "weekly" || got.StreamType != "m3u8" {
		t.Errorf("after name-only update = %+v, want name=Renamed update_freq=weekly stream_type=m3u8", got)
	}

	// Persisted across a re-read.
	list, _ := s.ListXtreamPlaylists()
	if len(list) != 1 || list[0].Name != "Renamed" || list[0].UpdateFreq != "weekly" || list[0].StreamType != "m3u8" {
		t.Errorf("reloaded = %+v", list)
	}

	// All-nil is a no-op read, not an error.
	got, err = s.UpdatePlaylistFields(p.ID, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdatePlaylistFields (no-op): %v", err)
	}
	if got.Name != "Renamed" || got.UpdateFreq != "weekly" || got.StreamType != "m3u8" {
		t.Errorf("no-op read = %+v, want unchanged Renamed/weekly/m3u8", got)
	}
}

func TestUpdatePlaylistFieldsInvalid(t *testing.T) {
	s := open(t)
	p, _ := s.SaveXtreamPlaylist("KS", "http://p:8080", "u", "pw")

	if _, err := s.UpdatePlaylistFields(p.ID, nil, strp("hourly"), nil); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("bad freq err = %v, want ErrInvalidSetting", err)
	}
	if _, err := s.UpdatePlaylistFields(p.ID, nil, nil, strp("rtmp")); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("bad stream type err = %v, want ErrInvalidSetting", err)
	}
	if _, err := s.UpdatePlaylistFields(p.ID, strp("  "), nil, nil); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("blank name err = %v, want ErrInvalidSetting", err)
	}
	if _, err := s.UpdatePlaylistFields("nope", nil, strp("manual"), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestUpdatePlaylistFields -v`
Expected: FAIL — `s.UpdatePlaylistFields undefined` (compile error), since
the method doesn't exist yet and `UpdateXtreamSettings` still exists
unreferenced by these new tests.

- [ ] **Step 3: Replace `UpdateXtreamSettings` with `UpdatePlaylistFields`**

In `internal/store/store.go`, replace the entire `UpdateXtreamSettings`
function (lines 797-813) with:

```go
// UpdatePlaylistFields partially updates a saved playlist: name, update_freq
// and stream_type are each optional (nil leaves that column untouched),
// letting the Playlist management tab change one field at a time without
// resending the others. A nil name is "don't touch"; a non-nil name must be
// non-blank after trimming. updateFreq/streamType, if provided, are validated
// against the existing enums. Passing all three as nil is a no-op read of the
// current row. ErrInvalidSetting on a bad or blank value; ErrNotFound if no
// such playlist.
func (s *Store) UpdatePlaylistFields(id string, name, updateFreq, streamType *string) (XtreamPlaylist, error) {
	var sets []string
	var args []any

	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "name=?")
		args = append(args, trimmed)
	}
	if updateFreq != nil {
		if !validUpdateFreq[*updateFreq] {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "update_freq=?")
		args = append(args, *updateFreq)
	}
	if streamType != nil {
		if !validStreamType[*streamType] {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "stream_type=?")
		args = append(args, *streamType)
	}

	if len(sets) == 0 {
		return s.GetXtreamPlaylist(id)
	}

	args = append(args, id)
	res, err := s.db.Exec(`UPDATE xtream_playlists SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	if err != nil {
		return XtreamPlaylist{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return XtreamPlaylist{}, ErrNotFound
	}
	return s.GetXtreamPlaylist(id)
}
```

Also update the `ErrInvalidSetting` doc comment (`internal/store/store.go:40-42`)
since it currently says "flags an UpdateXtreamSettings call":

```go
	// ErrInvalidSetting flags an UpdatePlaylistFields call with a blank name or
	// an out-of-range update_freq or stream_type value.
	ErrInvalidSetting = errors.New("invalid xtream setting")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestUpdatePlaylistFields -v`
Expected: PASS (both `TestUpdatePlaylistFields` and
`TestUpdatePlaylistFieldsInvalid`)

- [ ] **Step 5: Fix the now-broken call site (compile error) — main.go**

`main.go:1081` still calls the removed `UpdateXtreamSettings`. This will be
fully rewritten in Task 3, but to keep the build green after this task,
update the call minimally now:

In `main.go`, find:

```go
		p, err := st.UpdateXtreamSettings(id, body.UpdateFreq, body.StreamType)
```

Replace with:

```go
		p, err := st.UpdatePlaylistFields(id, nil, &body.UpdateFreq, &body.StreamType)
```

(This is a temporary shim — Task 3 replaces the whole handler body to make
all three fields optional.)

- [ ] **Step 6: Run the full test suite and build**

Run: `go build ./... && go test ./...`
Expected: PASS, no compile errors.

- [ ] **Step 7: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go main.go
git commit -m "refactor(store): replace UpdateXtreamSettings with partial UpdatePlaylistFields"
```

---

### Task 2: Store — `DeleteXtreamPlaylist`

**Files:**
- Modify: `internal/store/store.go` (add new method after `UpdatePlaylistFields`)
- Modify: `internal/store/store_test.go` (add new test after
  `TestUpdatePlaylistFieldsInvalid`)

**Interfaces:**
- Consumes: `s.db *sql.DB`, `ErrNotFound` (already defined).
- Produces: `func (s *Store) DeleteXtreamPlaylist(id string) (channelsDeleted
  int, err error)` — used by Task 3 (handler).

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go` (after `TestUpdatePlaylistFieldsInvalid`):

```go
func TestDeleteXtreamPlaylist(t *testing.T) {
	s := open(t)
	p, err := s.SaveXtreamPlaylist("Panel", "http://p", "u", "pw")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist: %v", err)
	}
	other, err := s.SaveXtreamPlaylist("Other Panel", "http://q", "u2", "pw2")
	if err != nil {
		t.Fatalf("SaveXtreamPlaylist (other): %v", err)
	}

	streams := []XtreamStream{
		{StreamID: 1, Name: "A", URL: "http://p/live/u/pw/1.ts"},
		{StreamID: 2, Name: "B", URL: "http://p/live/u/pw/2.ts"},
	}
	if _, _, err := s.UpsertXtreamChannels(p.ID, streams); err != nil {
		t.Fatalf("UpsertXtreamChannels: %v", err)
	}
	otherStreams := []XtreamStream{
		{StreamID: 9, Name: "C", URL: "http://q/live/u2/pw2/9.ts"},
	}
	if _, _, err := s.UpsertXtreamChannels(other.ID, otherStreams); err != nil {
		t.Fatalf("UpsertXtreamChannels (other): %v", err)
	}

	// Favourite one of p's channels — DeleteXtreamPlaylist must still remove it.
	if ok, _ := s.SetFavourite("xtream:"+p.ID+":1", true); !ok {
		t.Fatal("SetFavourite should report ok")
	}

	n, err := s.DeleteXtreamPlaylist(p.ID)
	if err != nil {
		t.Fatalf("DeleteXtreamPlaylist: %v", err)
	}
	if n != 2 {
		t.Errorf("channelsDeleted = %d, want 2", n)
	}

	if _, err := s.GetXtreamPlaylist(p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("playlist should be gone, err = %v", err)
	}

	chans, _ := s.ListChannels()
	ids := map[string]bool{}
	for _, c := range chans {
		ids[c.ID] = true
	}
	if ids["xtream:"+p.ID+":1"] || ids["xtream:"+p.ID+":2"] {
		t.Error("deleted playlist's channels (incl. favourited) must be gone")
	}
	if !ids["xtream:"+other.ID+":9"] {
		t.Error("other playlist's channel must survive")
	}

	if _, err := s.DeleteXtreamPlaylist("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestDeleteXtreamPlaylist -v`
Expected: FAIL — `s.DeleteXtreamPlaylist undefined` (compile error).

- [ ] **Step 3: Implement `DeleteXtreamPlaylist`**

Add to `internal/store/store.go`, immediately after `UpdatePlaylistFields`:

```go
// DeleteXtreamPlaylist removes one saved Xtream playlist together with every
// channel it imported — favourited or not, since the playlist is going away
// and its channels can no longer be refreshed. Both deletes run in one
// transaction. ErrNotFound when the id matches no saved playlist. Returns the
// channel-delete count (the playlist itself is always exactly one row).
func (s *Store) DeleteXtreamPlaylist(id string) (channelsDeleted int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	res, err := tx.Exec(`DELETE FROM channels WHERE xtream_playlist_id = ?`, id)
	if err != nil {
		return 0, err
	}
	cn, _ := res.RowsAffected()

	res, err = tx.Exec(`DELETE FROM xtream_playlists WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		err = ErrNotFound
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(cn), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestDeleteXtreamPlaylist -v`
Expected: PASS

- [ ] **Step 5: Run the full store test suite**

Run: `go test ./internal/store/...`
Expected: PASS, all tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): add DeleteXtreamPlaylist"
```

---

### Task 3: HTTP handlers — partial PATCH + new DELETE

**Files:**
- Modify: `main.go:1071-1095` (PATCH handler — make body fully optional)
- Modify: `main.go` (add new DELETE handler after the PATCH handler)
- Test: `main_test.go` (add handler tests)

**Interfaces:**
- Consumes: `st.UpdatePlaylistFields(id string, name, updateFreq, streamType
  *string) (store.XtreamPlaylist, error)` (Task 1), `st.DeleteXtreamPlaylist(id
  string) (int, error)` (Task 2), `store.ErrNotFound`, `store.ErrInvalidSetting`,
  `channels.rebuild()`, `serverError(w, op string, err error)`, `writeJSON(w,
  r, v any)` — all pre-existing except the two store methods just added.
- Produces: `PATCH /api/xtream/playlists/{id}` (body `{name?, update_freq?,
  stream_type?}` → returns updated `store.XtreamPlaylist` JSON), `DELETE
  /api/xtream/playlists/{id}` (no body → returns `{"deleted": <int>}`) — both
  consumed by Task 4 (frontend).

- [ ] **Step 1: Write the failing tests**

`main_test.go` already has a handler-test harness for this route
(`testMux`, `do`, `xtreamPanel` helpers, and `TestXtreamPlaylistHandlers`
around line 352). Add a new test after `TestXtreamPlaylistHandlers`
(after line 411):

```go
func TestXtreamPlaylistPatchAndDelete(t *testing.T) {
	mux, _, st := testMux(t)
	panel := xtreamPanel(t, `[{"stream_id":10,"name":"Alpha"}]`)

	rec := do(t, mux, http.MethodPost, "/api/xtream/playlists",
		`{"name":"Panel","server":"`+panel.URL+`","username":"u","password":"p"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: got %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Playlist store.XtreamPlaylist `json:"playlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id := created.Playlist.ID

	// PATCH with only stream_type: name must be untouched.
	rec = do(t, mux, http.MethodPatch, "/api/xtream/playlists/"+id, `{"stream_type":"m3u8"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch stream_type: got %d body %s", rec.Code, rec.Body.String())
	}
	var p1 store.XtreamPlaylist
	if err := json.Unmarshal(rec.Body.Bytes(), &p1); err != nil {
		t.Fatalf("decode patch1: %v", err)
	}
	if p1.Name != "Panel" || p1.StreamType != "m3u8" || p1.UpdateFreq != "manual" {
		t.Errorf("after stream_type-only patch = %+v, want name=Panel stream_type=m3u8 update_freq=manual", p1)
	}

	// PATCH with only name: stream_type must stay from the previous patch.
	rec = do(t, mux, http.MethodPatch, "/api/xtream/playlists/"+id, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch name: got %d body %s", rec.Code, rec.Body.String())
	}
	var p2 store.XtreamPlaylist
	if err := json.Unmarshal(rec.Body.Bytes(), &p2); err != nil {
		t.Fatalf("decode patch2: %v", err)
	}
	if p2.Name != "Renamed" || p2.StreamType != "m3u8" {
		t.Errorf("after name-only patch = %+v, want name=Renamed stream_type=m3u8", p2)
	}

	// PATCH with a blank name is rejected.
	rec = do(t, mux, http.MethodPatch, "/api/xtream/playlists/"+id, `{"name":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch blank name: got %d, want 400", rec.Code)
	}

	// PATCH of an unknown id is 404.
	if rec := do(t, mux, http.MethodPatch, "/api/xtream/playlists/nope", `{"name":"X"}`); rec.Code != http.StatusNotFound {
		t.Errorf("patch unknown: got %d, want 404", rec.Code)
	}

	// The imported channel is present before delete.
	findChannel(t, st, "xtream:"+id+":10")

	// DELETE removes the playlist and its channel.
	rec = do(t, mux, http.MethodDelete, "/api/xtream/playlists/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d body %s", rec.Code, rec.Body.String())
	}
	var delResp struct{ Deleted int }
	if err := json.Unmarshal(rec.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if delResp.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", delResp.Deleted)
	}
	chans, _ := st.ListChannels()
	for _, c := range chans {
		if c.ID == "xtream:"+id+":10" {
			t.Error("channel should have been deleted along with its playlist")
		}
	}

	// DELETE of an unknown id is 404.
	if rec := do(t, mux, http.MethodDelete, "/api/xtream/playlists/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete already-removed: got %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run TestXtreamPlaylistPatchAndDelete -v`
Expected: FAIL — `st.UpdatePlaylistFields` already exists from Task 1 so the
PATCH half may compile; the DELETE half fails with a 404/`http: no matching
handler` (or similar `unregistered method "DELETE"` panic from `ServeMux`)
since the route doesn't exist yet.

- [ ] **Step 3: Rewrite the PATCH handler and add the DELETE handler**

In `main.go`, replace the entire PATCH handler block (the one added/modified
in Task 1 Step 5) — currently:

```go
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
		p, err := st.UpdatePlaylistFields(id, nil, &body.UpdateFreq, &body.StreamType)
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

with:

```go
	// Update a saved playlist's fields. Each of name/update_freq/stream_type is
	// optional — only fields present in the JSON body are validated and
	// changed, letting the Playlist tab update one field (e.g. just the name)
	// without resending the others. Applying a new stream type takes effect on
	// the next refresh; this endpoint only persists the choice.
	mux.HandleFunc("PATCH /api/xtream/playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Name       *string `json:"name"`
			UpdateFreq *string `json:"update_freq"`
			StreamType *string `json:"stream_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p, err := st.UpdatePlaylistFields(id, body.Name, body.UpdateFreq, body.StreamType)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, store.ErrInvalidSetting) {
			http.Error(w, "name must be non-blank, update_freq must be manual/daily/3days/weekly, and stream_type ts/m3u8", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		writeJSON(w, r, p)
	})

	// Remove a saved playlist along with every channel it imported. Local
	// single-user app, DB restorable from backup: a plain browser confirm() is
	// the only guard, no typed-confirmation ceremony.
	mux.HandleFunc("DELETE /api/xtream/playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		deleted, err := st.DeleteXtreamPlaylist(id)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, "api", err)
			return
		}
		channels.rebuild()
		log.Printf("xtream: removed playlist %s, deleted %d channel(s)", id, deleted)
		writeJSON(w, r, map[string]int{"deleted": deleted})
	})
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./ -run TestXtreamPlaylistPatchAndDelete -v`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS, no regressions in `TestXtreamPlaylistHandlers` or any other
existing test.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(api): partial playlist PATCH + DELETE /api/xtream/playlists/{id}"
```

---

### Task 4: Frontend — Playlist tab UI

**Files:**
- Modify: `web/templates/index.html:216-220` (tab bar — add third button)
- Modify: `web/templates/index.html:306-307` (add new panel after the Xtream
  Codes panel, before the closing `</div>` at line 307)
- Modify: `web/static/state.js:71-76` (register new element ids)
- Modify: `web/static/modals.js:37-49` (generalize `setAddTab` to 3-way)
- Create: `web/static/playlist-tab.js`
- Modify: `web/static/style.css` (row styles)

**Interfaces:**
- Consumes: `state`, `els` (`web/static/state.js`), `loadChannels` (`web/static/init.js`),
  `GET /api/xtream/playlists`, `PATCH /api/xtream/playlists/{id}` (body
  `{name?, stream_type?}`), `DELETE /api/xtream/playlists/{id}` — all from
  Task 3.
- Produces: `export function initPlaylistTab()` from `web/static/playlist-tab.js`,
  imported by `web/static/modals.js`.

- [ ] **Step 1: Add the third tab button and panel markup**

In `web/templates/index.html`, replace the tab bar (lines 217-220):

```html
      <!-- Tab bar: hidden while editing an existing channel (Manual only). -->
      <div id="addTabs" class="add-tabs">
        <button type="button" id="addTabManual" class="add-tab active" data-tab="manual">Manual</button>
        <button type="button" id="addTabXtream" class="add-tab" data-tab="xtream">Xtream Codes</button>
        <button type="button" id="addTabPlaylist" class="add-tab" data-tab="playlist">Playlist</button>
      </div>
```

Then, after the closing `</div>` of `#xtreamPanel` (currently line 306,
right before the outer `</div>` at line 307), insert the new panel:

```html
      <!-- Tab 3 — Playlist: manage already-saved Xtream playlists (rename,
           stream type, delete). Adding a playlist stays on the Xtream Codes
           tab; this tab is management-only. -->
      <div id="playlistPanel" class="add-form add-tab-panel" data-tab="playlist" hidden>
        <div id="playlistList" class="playlist-list"></div>
        <p id="playlistEmpty" class="add-hint" hidden>No saved playlists yet — add one from the Xtream Codes tab.</p>
      </div>
```

- [ ] **Step 2: Register new element ids in state.js**

In `web/static/state.js`, replace line 71:

```js
  addTabs: $('addTabs'), addTabManual: $('addTabManual'), addTabXtream: $('addTabXtream'),
```

with:

```js
  addTabs: $('addTabs'), addTabManual: $('addTabManual'), addTabXtream: $('addTabXtream'),
  addTabPlaylist: $('addTabPlaylist'),
  playlistPanel: $('playlistPanel'), playlistList: $('playlistList'), playlistEmpty: $('playlistEmpty'),
```

- [ ] **Step 3: Write `playlist-tab.js`**

Create `web/static/playlist-tab.js`:

```js
import { els } from './state.js';
import { loadChannels } from './init.js';

// The Playlist tab of the add modal: lists every saved Xtream playlist for
// management (rename, change stream type, delete). Adding a playlist is still
// done from the Xtream Codes tab — this tab never creates one.

// initPlaylistTab loads and renders the saved-playlist list; called each time
// the tab is shown so a playlist added/removed elsewhere in this session is
// reflected without a full page reload.
export function initPlaylistTab() {
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) { render(Array.isArray(list) ? list : []); })
    .catch(function () { render([]); });
}

function render(list) {
  els.playlistList.innerHTML = '';
  els.playlistEmpty.hidden = list.length > 0;
  list.forEach(function (p) { els.playlistList.appendChild(buildRow(p)); });
}

function buildRow(p) {
  const row = document.createElement('div');
  row.className = 'playlist-row';

  const nameSpan = document.createElement('span');
  nameSpan.className = 'playlist-row-name';
  nameSpan.textContent = p.name;
  nameSpan.tabIndex = 0;
  nameSpan.title = 'Click to rename';
  nameSpan.addEventListener('click', function () { startRename(row, nameSpan, p); });

  const typeSelect = document.createElement('select');
  typeSelect.className = 'country-select playlist-row-type';
  ['ts', 'm3u8'].forEach(function (v) {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v === 'ts' ? 'MPEG-TS (ts)' : 'HLS (m3u8)';
    if (v === (p.stream_type || 'ts')) opt.selected = true;
    typeSelect.appendChild(opt);
  });
  typeSelect.addEventListener('change', function () {
    patchPlaylist(p.id, { stream_type: typeSelect.value });
  });

  const removeBtn = document.createElement('button');
  removeBtn.type = 'button';
  removeBtn.className = 'add-btn add-btn-ghost playlist-row-remove';
  removeBtn.textContent = 'Remove';
  removeBtn.addEventListener('click', function () {
    if (!confirm('Remove playlist "' + p.name + '"?')) return;
    removePlaylist(p.id);
  });

  row.appendChild(nameSpan);
  row.appendChild(typeSelect);
  row.appendChild(removeBtn);
  return row;
}

// startRename swaps the name span for a text input in place; Enter/blur
// saves (unless blank, which reverts with no request), Escape cancels.
function startRename(row, nameSpan, p) {
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'playlist-row-name-input';
  input.value = p.name;
  row.replaceChild(input, nameSpan);
  input.focus();
  input.select();

  let done = false;
  function finish(save) {
    if (done) return;
    done = true;
    const next = input.value.trim();
    if (save && next && next !== p.name) {
      patchPlaylist(p.id, { name: next }, function () { p.name = next; nameSpan.textContent = next; });
    }
    row.replaceChild(nameSpan, input);
  }
  input.addEventListener('blur', function () { finish(true); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
}

function patchPlaylist(id, body, onSuccess) {
  fetch('/api/xtream/playlists/' + encodeURIComponent(id), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    })
    .then(function () { if (onSuccess) onSuccess(); })
    .catch(function () { initPlaylistTab(); }); // reload to discard the failed edit
}

function removePlaylist(id) {
  fetch('/api/xtream/playlists/' + encodeURIComponent(id), { method: 'DELETE' })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    })
    .then(function () {
      loadChannels();
      initPlaylistTab();
    })
    .catch(function () { initPlaylistTab(); });
}
```

- [ ] **Step 4: Wire the third tab into `modals.js`**

In `web/static/modals.js`, replace the import (line 3):

```js
import { initXtreamTab, resetXtreamTab } from './xtream.js';
```

with:

```js
import { initXtreamTab, resetXtreamTab } from './xtream.js';
import { initPlaylistTab } from './playlist-tab.js';
```

Replace `setAddTab` (lines 37-46):

```js
// setAddTab switches between the Manual and Xtream Codes tabs of the add modal.
function setAddTab(tab) {
  const xtream = tab === 'xtream';
  els.addTabManual.classList.toggle('active', !xtream);
  els.addTabXtream.classList.toggle('active', xtream);
  els.addChannelForm.hidden = xtream;
  els.xtreamPanel.hidden = !xtream;
  els.addChannelTitle.textContent = xtream ? 'Add from Xtream Codes' : 'Add a channel';
  if (xtream) initXtreamTab();
}
```

with:

```js
// setAddTab switches between the Manual, Xtream Codes, and Playlist tabs of
// the add modal.
function setAddTab(tab) {
  els.addTabManual.classList.toggle('active', tab === 'manual');
  els.addTabXtream.classList.toggle('active', tab === 'xtream');
  els.addTabPlaylist.classList.toggle('active', tab === 'playlist');
  els.addChannelForm.hidden = tab !== 'manual';
  els.xtreamPanel.hidden = tab !== 'xtream';
  els.playlistPanel.hidden = tab !== 'playlist';
  els.addChannelTitle.textContent =
    tab === 'xtream' ? 'Add from Xtream Codes' :
    tab === 'playlist' ? 'Manage playlists' : 'Add a channel';
  if (tab === 'xtream') initXtreamTab();
  if (tab === 'playlist') initPlaylistTab();
}
```

Replace the two listener lines (lines 48-49):

```js
els.addTabManual.addEventListener('click', function () { setAddTab('manual'); });
els.addTabXtream.addEventListener('click', function () { setAddTab('xtream'); });
```

with:

```js
els.addTabManual.addEventListener('click', function () { setAddTab('manual'); });
els.addTabXtream.addEventListener('click', function () { setAddTab('xtream'); });
els.addTabPlaylist.addEventListener('click', function () { setAddTab('playlist'); });
```

- [ ] **Step 5: Add row styles to `style.css`**

Append to `web/static/style.css`:

```css
.playlist-list { display: flex; flex-direction: column; gap: 8px; }
.playlist-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
}
.playlist-row-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: text;
  font-weight: 600;
}
.playlist-row-name-input {
  flex: 1 1 auto;
  min-width: 0;
  font: inherit;
  font-weight: 600;
  background: var(--input-bg);
  color: #fff;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 2px 6px;
}
.playlist-row-type { flex: 0 0 auto; }
.playlist-row-remove { flex: 0 0 auto; }
```

- [ ] **Step 6: Manual UI verification**

Run the app (`go run .`), open it in a browser, click "Add a channel" →
"Playlist" tab. Verify:
- Saved playlists list with name, stream-type select, Remove button.
- Clicking a name turns it into an editable input; typing a new name and
  pressing Enter renames it (verify via reload — GET
  `/api/xtream/playlists` reflects the new name).
- Escape while editing cancels without a request (check Network tab: no
  PATCH fired).
- Changing the stream-type select fires a PATCH and persists across tab
  switches.
- Remove prompts a `confirm()`, and on accept, the row disappears, the
  channel list refreshes, and the playlist and its channels are gone from a
  reload.

Expected: all behaviors work as described, no console errors.

- [ ] **Step 7: Commit**

```bash
git add web/templates/index.html web/static/state.js web/static/modals.js web/static/playlist-tab.js web/static/style.css
git commit -m "feat(ui): add Playlist management tab (rename, stream type, delete)"
```

---

## Self-Review Notes

- **Spec coverage:** UI tab/rows (Task 4), partial PATCH (Task 1 + 3),
  DELETE endpoint (Task 2 + 3), out-of-scope items (no update-freq control,
  no imported-count) respected in Task 4's row markup — all covered.
- **Placeholder scan:** none found; every step has literal code or exact
  commands.
- **Type consistency:** `UpdatePlaylistFields(id string, name, updateFreq,
  streamType *string) (XtreamPlaylist, error)` is identical across Tasks 1, 3;
  `DeleteXtreamPlaylist(id string) (int, error)` identical across Tasks 2, 3;
  frontend `patchPlaylist`/`removePlaylist` bodies match the handler's
  expected JSON shape from Task 3.
