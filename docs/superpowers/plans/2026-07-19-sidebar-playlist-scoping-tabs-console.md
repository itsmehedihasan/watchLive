# Sidebar Playlist Scoping, Tabs, and Xtream Console — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Scope the browse sidebar to a selected Xtream playlist, add Channels/Movies/Sports tabs, remove the viewers heartbeat, and log raw Xtream panel responses to the browser console on refresh/import.

**Architecture:** The `/api/channels` payload gains each channel's `xtream_playlist_id` so the frontend can filter client-side (no refetch). A playlist `<select>` and a tab bar drive `state.selectedPlaylist` / `state.activeTab`, both consumed by the existing `renderChannelList()` pipeline. The Xtream client gains raw-body variants of its three fetches; the two import/refresh handlers return those raw blobs under a `debug` key, which `xtream.js` logs verbatim.

**Tech Stack:** Go (net/http, database/sql via modernc.org/sqlite), vanilla ES modules frontend, no build step.

## Global Constraints

- Filter key is `xtream_playlist_id` (id), never playlist name. Dropdown displays name, option value = id.
- Default selected playlist = index [0] of `GET /api/xtream/playlists`. No persistence — resets on reload.
- Manual channels (`ch.id` starts with `manual:`) are ALWAYS visible regardless of selected playlist.
- ★ Favourites section is scoped by the same playlist-or-manual predicate.
- Tabs: `Channels` / `Movies` / `Sports`. Default `channels`. In-memory, no persistence.
- "All Countries" filter stays, capitalized. Country/health/search pipeline unchanged.
- Console logging = browser console, verbatim (credentials NOT masked), refresh/import only (NOT startup sweep).
- Existing `/api/viewers` server endpoint stays mounted; only the client call is removed.
- Frontend is ES5-style vanilla JS (var/function, no arrow-only). Match surrounding style.
- Run all Go tests with `go test ./...` from repo root (`c:/HDD/watchLive - main`).

---

## Task 1: Serialize `xtream_playlist_id` in the channels payload

**Files:**
- Modify: `internal/store/store.go` (Channel struct ~line 109-116; `ListChannels` SELECT ~line 201-203; `scanChannel` ~line 224-251)
- Test: `main_test.go` (extend an existing `/api/channels` assertion)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `Channel.XtreamPlaylistID string` serialized as JSON key `xtream_playlist_id` on every channel in `GET /api/channels`. Xtream channels carry their playlist id; manual/m3u channels carry `""`.

- [ ] **Step 1: Write the failing test**

Add to `main_test.go` inside `TestImportXtreamStreamsMapsCategories` (which already imports streams and reads back channels). After the existing assertions that channels were imported, fetch `/api/channels` and assert the imported channel's JSON has a non-empty `xtream_playlist_id`. If that test's structure makes this awkward, add a new test:

Helpers verified in `main_test.go`: `testMux(t)` returns `(mux *http.ServeMux, _, st *store.Store)`; `xtreamPanel(t, streamsJSON string)` returns an `*httptest.Server` stub panel; `do(t, mux, method, target, body)` returns `*httptest.ResponseRecorder`.

```go
func TestChannelsPayloadCarriesPlaylistID(t *testing.T) {
	mux, _, _ := testMux(t)
	panel := xtreamPanel(t, `[{"stream_id":10,"name":"Alpha"}]`)

	rec := do(t, mux, http.MethodPost, "/api/xtream/playlists",
		`{"name":"P","server":"`+panel.URL+`","username":"u","password":"p"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: got %d body %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Playlist store.XtreamPlaylist `json:"playlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}

	rec = do(t, mux, http.MethodGet, "/api/channels", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("channels: got %d", rec.Code)
	}
	var chans []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &chans); err != nil {
		t.Fatalf("decode channels: %v", err)
	}
	found := false
	for _, ch := range chans {
		id, _ := ch["id"].(string)
		if strings.HasPrefix(id, "xtream:"+saved.Playlist.ID+":") {
			if pid, _ := ch["xtream_playlist_id"].(string); pid == saved.Playlist.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no imported channel carried xtream_playlist_id=%q", saved.Playlist.ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestChannelsPayloadCarriesPlaylistID -v`
Expected: FAIL — the `xtream_playlist_id` key is absent (nil), `found` stays false.

- [ ] **Step 3: Add the struct field**

In `internal/store/store.go`, in the `Channel` struct, after the `CatOrder` field (line ~115):

```go
	// XtreamPlaylistID ties an Xtream-imported channel to its saved playlist so
	// the browse sidebar can scope the list to one playlist. Empty for manual
	// and .m3u-imported channels.
	XtreamPlaylistID string `json:"xtream_playlist_id"`
```

- [ ] **Step 4: Add the column to the SELECT**

In `ListChannels`, add `xtream_playlist_id` to the column list (keep it last to match the scan order):

```go
	rows, err := s.db.Query(`
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, resolver, resolver_arg, is_favourite, is_working, cat_order, xtream_playlist_id
		FROM channels ORDER BY sort_name, name`)
```

- [ ] **Step 5: Scan the column**

In `scanChannel`, add `&ch.XtreamPlaylistID` to the end of the `row.Scan(...)` argument list (matching the new SELECT position):

```go
	if err := row.Scan(&ch.ID, &ch.Name, &ch.Logo, &ch.Group, &ch.Type, &serversJS, &clearJS, &ch.UserAgent, &ch.Referer, &ch.Resolver, &ch.ResolverArg, &fav, &working, &ch.CatOrder, &ch.XtreamPlaylistID); err != nil {
		return Channel{}, err
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./... -run TestChannelsPayloadCarriesPlaylistID -v`
Expected: PASS.

- [ ] **Step 7: Run the full suite (no regressions)**

Run: `go test ./...`
Expected: all PASS. `scanChannel` is also used by any single-row reads — verify none broke.

- [ ] **Step 8: Commit**

```bash
git add internal/store/store.go main_test.go
git commit -m "feat(store): serialize xtream_playlist_id on channels payload"
```

---

## Task 2: Raw-body variants in the Xtream client

**Files:**
- Modify: `internal/xtream/xtream.go` (`Login` ~166, `LiveStreams` ~187, `LiveCategories` ~220)
- Test: `internal/xtream/xtream_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func LoginRaw(server, username, password string) (UserInfo, ServerInfo, json.RawMessage, error)`
  - `func LiveStreamsRaw(server, username, password string) ([]Stream, json.RawMessage, error)`
  - `func LiveCategoriesRaw(server, username, password string) ([]Category, json.RawMessage, error)`
  - Existing `Login`/`LiveStreams`/`LiveCategories` keep their current signatures and now delegate to the `*Raw` versions, discarding the raw. `json.RawMessage` is the exact bytes the panel returned (login envelope, streams array, categories array).

- [ ] **Step 1: Write the failing test**

In `internal/xtream/xtream_test.go`, add a test that the raw body round-trips unmodified. Reuse the existing `stubPanel` helper (it serves login + `get_live_streams` / `get_live_categories`). Match the helper's real signature from the top of the test file.

Helper verified in `xtream_test.go`: `stubPanel(t, login, streams, cats string)` (serves `login` for a bare request, `streams` for `action=get_live_streams`, `cats` for `action=get_live_categories`). Const `okLogin` is available in the package test file.

```go
func TestLiveStreamsRawReturnsExactBody(t *testing.T) {
	const streamsBody = `[{"stream_id":7,"name":"Chan","category_id":"1","container_extension":"ts"}]`
	srv := stubPanel(t, okLogin, streamsBody, "[]")

	streams, raw, err := LiveStreamsRaw(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("LiveStreamsRaw: %v", err)
	}
	if len(streams) != 1 || streams[0].StreamID != 7 {
		t.Fatalf("parsed streams wrong: %+v", streams)
	}
	if string(raw) != streamsBody {
		t.Fatalf("raw body not verbatim:\n got %s\nwant %s", raw, streamsBody)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xtream/ -run TestLiveStreamsRawReturnsExactBody -v`
Expected: FAIL — `LiveStreamsRaw` undefined.

- [ ] **Step 3: Add `LoginRaw` and delegate `Login`**

Replace the `Login` function body so the raw body is captured, and add `LoginRaw`:

```go
// LoginRaw is Login but also returns the exact response body bytes for debug
// logging. Login delegates to it and discards the raw.
func LoginRaw(server, username, password string) (UserInfo, ServerInfo, json.RawMessage, error) {
	body, status, err := get(playerAPI(server, username, password, nil))
	if err != nil {
		return UserInfo{}, ServerInfo{}, nil, fmt.Errorf("xtream: login: %w", err)
	}
	if status < 200 || status >= 300 {
		return UserInfo{}, ServerInfo{}, json.RawMessage(body), fmt.Errorf("%w: panel returned status %d", ErrAuth, status)
	}
	var lr loginResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return UserInfo{}, ServerInfo{}, json.RawMessage(body), fmt.Errorf("%w: unexpected response body", ErrAuth)
	}
	if lr.UserInfo.Auth != 1 {
		return UserInfo{}, ServerInfo{}, json.RawMessage(body), ErrAuth
	}
	return lr.UserInfo, lr.ServerInfo, json.RawMessage(body), nil
}

func Login(server, username, password string) (UserInfo, ServerInfo, error) {
	ui, si, _, err := LoginRaw(server, username, password)
	return ui, si, err
}
```

- [ ] **Step 4: Add `LiveStreamsRaw` and delegate `LiveStreams`**

Replace `LiveStreams` with a delegating wrapper and a `LiveStreamsRaw` that returns the streams body:

```go
func LiveStreamsRaw(server, username, password string) ([]Stream, json.RawMessage, error) {
	if _, _, _, err := LoginRaw(server, username, password); err != nil {
		return nil, nil, err
	}
	u := playerAPI(server, username, password, url.Values{"action": {"get_live_streams"}})
	body, status, err := get(u)
	if err != nil {
		return nil, nil, fmt.Errorf("xtream: live streams: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, json.RawMessage(body), fmt.Errorf("xtream: live streams: panel returned status %d", status)
	}
	var raw []rawStream
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, json.RawMessage(body), fmt.Errorf("xtream: live streams: decode: %w", err)
	}
	out := make([]Stream, 0, len(raw))
	for _, r := range raw {
		out = append(out, Stream{
			StreamID:   int(r.StreamID),
			Name:       strings.TrimSpace(r.Name),
			Icon:       r.Icon,
			CategoryID: r.CategoryID,
			Extension:  r.Extension,
		})
	}
	return out, json.RawMessage(body), nil
}

func LiveStreams(server, username, password string) ([]Stream, error) {
	s, _, err := LiveStreamsRaw(server, username, password)
	return s, err
}
```

- [ ] **Step 5: Add `LiveCategoriesRaw` and delegate `LiveCategories`**

```go
func LiveCategoriesRaw(server, username, password string) ([]Category, json.RawMessage, error) {
	if _, _, _, err := LoginRaw(server, username, password); err != nil {
		return nil, nil, err
	}
	u := playerAPI(server, username, password, url.Values{"action": {"get_live_categories"}})
	body, status, err := get(u)
	if err != nil {
		return nil, nil, fmt.Errorf("xtream: live categories: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, json.RawMessage(body), fmt.Errorf("xtream: live categories: panel returned status %d", status)
	}
	var raw []rawCategory
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, json.RawMessage(body), fmt.Errorf("xtream: live categories: decode: %w", err)
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		out = append(out, Category{ID: string(r.ID), Name: strings.TrimSpace(r.Name)})
	}
	return out, json.RawMessage(body), nil
}

func LiveCategories(server, username, password string) ([]Category, error) {
	c, _, err := LiveCategoriesRaw(server, username, password)
	return c, err
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/xtream/ -run TestLiveStreamsRawReturnsExactBody -v`
Expected: PASS.

- [ ] **Step 7: Run the xtream package tests (no regressions)**

Run: `go test ./internal/xtream/`
Expected: all PASS (existing `Login`/`LiveStreams`/`LiveCategories` tests still green — they now delegate).

- [ ] **Step 8: Commit**

```bash
git add internal/xtream/xtream.go internal/xtream/xtream_test.go
git commit -m "feat(xtream): raw-body variants of login/streams/categories"
```

---

## Task 3: Emit `debug` raw payloads from import/refresh handlers

**Files:**
- Modify: `main.go` (`POST /api/xtream/playlists` ~1043-1086; `POST /api/xtream/playlists/{id}/refresh` ~1104-1137)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `xtream.LoginRaw`, `xtream.LiveStreamsRaw`, `xtream.LiveCategoriesRaw` from Task 2.
- Produces: both endpoints' JSON responses gain a `debug` object:
  `"debug": {"login": <raw|null>, "categories": <raw|null>, "streams": <raw|null>}`.
  Values are the verbatim panel bodies (`json.RawMessage`), or `null` when that fetch failed. The startup auto-refresh sweep does NOT go through these handlers, so it emits nothing (constraint satisfied structurally).

- [ ] **Step 1: Write the failing test**

In `main_test.go`, the refresh test already exists (`main_test.go:399` posts to `/refresh`). Add an assertion — or a new focused test — that the refresh response body contains a `debug` object with a non-null `streams` key:

Setup mirrors `TestXtreamPlaylistHandlers` (main_test.go:354): save via `POST /api/xtream/playlists`, read the id from the `playlist` object, then refresh.

```go
func TestRefreshResponseIncludesDebugRaw(t *testing.T) {
	mux, _, _ := testMux(t)
	panel := xtreamPanel(t, `[{"stream_id":10,"name":"Alpha"}]`)

	rec := do(t, mux, http.MethodPost, "/api/xtream/playlists",
		`{"name":"P","server":"`+panel.URL+`","username":"u","password":"p"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: got %d body %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Playlist store.XtreamPlaylist `json:"playlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}

	rec = do(t, mux, http.MethodPost, "/api/xtream/playlists/"+saved.Playlist.ID+"/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Added   int `json:"added"`
		Updated int `json:"updated"`
		Debug   struct {
			Login      json.RawMessage `json:"login"`
			Categories json.RawMessage `json:"categories"`
			Streams    json.RawMessage `json:"streams"`
		} `json:"debug"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Debug.Streams) == 0 || string(resp.Debug.Streams) == "null" {
		t.Fatalf("expected non-null debug.streams, got %q", resp.Debug.Streams)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestRefreshResponseIncludesDebugRaw -v`
Expected: FAIL — no `debug` key, `resp.Debug.Streams` empty.

- [ ] **Step 3: Update the refresh handler to use `*Raw` and emit debug**

In `POST /api/xtream/playlists/{id}/refresh`, swap the two fetches for their raw variants and thread the raw blobs into the response. Replace the streams/cats fetch + final `writeJSON`:

```go
		streams, streamsRaw, err := xtream.LiveStreamsRaw(p.Server, p.Username, p.Password)
		if err != nil {
			log.Printf("xtream: refresh %q: %v", p.Name, err)
			http.Error(w, "could not reach the panel or the credentials were rejected", http.StatusBadGateway)
			return
		}
		cats, catsRaw, err := xtream.LiveCategoriesRaw(p.Server, p.Username, p.Password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (refreshing without groups)", p.Name, err)
			cats = nil
			catsRaw = nil
		}
		added, updated, err := importXtreamStreams(st, p, streams, cats)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if err := st.StampXtreamRefreshed(p.ID); err != nil {
			log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
		}
		channels.rebuild()
		log.Printf("xtream: refreshed %q (%s): +%d new, %d updated", p.Name, p.ID, added, updated)
		writeJSON(w, r, map[string]any{
			"added":   added,
			"updated": updated,
			"debug": map[string]any{
				"login":      nil, // refresh doesn't call LoginRaw separately; streams call logs in already
				"categories": catsRaw,
				"streams":    streamsRaw,
			},
		})
```

> Note: `LiveStreamsRaw` performs the login internally but discards its raw. For the refresh handler, `login` is intentionally `null` — the streams+categories raw is what matters. Keep the key present (value `null`) so the client shape is stable.

- [ ] **Step 4: Update the save/import handler the same way**

In `POST /api/xtream/playlists`, mirror the change. This handler already calls `LiveStreams` before persisting; switch to `LiveStreamsRaw` and `LiveCategoriesRaw`, and include `debug` in the final `writeJSON`:

```go
		streams, streamsRaw, err := xtream.LiveStreamsRaw(server, username, password)
		if err != nil {
			log.Printf("xtream: import %q: %v", name, err)
			http.Error(w, "could not reach the panel or the credentials were rejected", http.StatusBadGateway)
			return
		}
		p, err := st.SaveXtreamPlaylist(name, server, username, password)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		cats, catsRaw, err := xtream.LiveCategoriesRaw(server, username, password)
		if err != nil {
			log.Printf("xtream: categories %q: %v (importing without groups)", name, err)
			cats = nil
			catsRaw = nil
		}
		added, _, err := importXtreamStreams(st, p, streams, cats)
		if err != nil {
			serverError(w, "api", err)
			return
		}
		if err := st.StampXtreamRefreshed(p.ID); err != nil {
			log.Printf("xtream: stamp refresh %q: %v", p.Name, err)
		}
		channels.rebuild()
		log.Printf("xtream: saved playlist %q (%s), imported %d channel(s)", name, p.ID, added)
		writeJSON(w, r, map[string]any{
			"playlist": p,
			"imported": added,
			"debug": map[string]any{
				"login":      nil,
				"categories": catsRaw,
				"streams":    streamsRaw,
			},
		})
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./... -run TestRefreshResponseIncludesDebugRaw -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: all PASS. The existing `TestXtreamPlaylistHandlers` refresh assertion (main_test.go:403) decodes into `struct{ Added, Updated int }`, which ignores the new `debug` key — no change needed there. Likewise the save assertion decodes into a struct with only `Playlist`/`Imported`. Confirm both still pass; if any test instead used `map[string]int` it would break — none do, but verify.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(xtream): return raw panel payloads under debug on import/refresh"
```

---

## Task 4: Frontend state + els for playlist and tabs

**Files:**
- Modify: `web/static/state.js` (state object ~1-37; `els` ~46-86)

**Interfaces:**
- Consumes: nothing.
- Produces: `state.selectedPlaylist` (string, `''` default), `state.activeTab` (string, `'channels'` default), `els.playlistFilter`, `els.playlistFilterWrap`, `els.sidebarTabs`, `els.tabChannels`, `els.tabMovies`, `els.tabSports`, `els.tabPlaceholder`. These DOM ids are created in Task 5.

- [ ] **Step 1: Add state fields**

In `web/static/state.js`, add to the `state` object (near `country`/`search`):

```js
  selectedPlaylist: '',
  activeTab: 'channels',
```

- [ ] **Step 2: Add els entries**

In the `els` object, after the `search` / `countryFilter` lines:

```js
  playlistFilterWrap: $('playlistFilterWrap'), playlistFilter: $('playlistFilter'),
  sidebarTabs: $('sidebarTabs'),
  tabChannels: $('tabChannels'), tabMovies: $('tabMovies'), tabSports: $('tabSports'),
  tabPlaceholder: $('tabPlaceholder'),
```

- [ ] **Step 3: Sanity check (no test — pure declaration)**

There is no unit test for state wiring. Verify by `node --check`:

Run: `node --check web/static/state.js`
Expected: no output (syntax OK). If `node` is unavailable, skip — Task 8 verifies in-browser.

- [ ] **Step 4: Commit**

```bash
git add web/static/state.js
git commit -m "feat(ui): state + els for playlist filter and sidebar tabs"
```

---

## Task 5: Sidebar markup — playlist dropdown + tab bar + placeholder

**Files:**
- Modify: `web/templates/index.html` (sidebar header region — find the existing `#countryFilter` and `#search` block)
- Modify: `web/static/style.css` (append styles)

**Interfaces:**
- Consumes: nothing (markup only). The ids match Task 4's `els`.
- Produces: DOM elements `#playlistFilterWrap > #playlistFilter`, `#sidebarTabs > #tabChannels/#tabMovies/#tabSports`, `#tabPlaceholder`. Layout order in the sidebar header: **playlist dropdown → tab bar → search → country filter → channel list**.

- [ ] **Step 1: Locate the sidebar header**

Open `web/templates/index.html` and find the block containing `id="search"` and `id="countryFilter"` (the sidebar top). Copy the existing wrapper element/class names used around them so new markup matches.

- [ ] **Step 2: Add the playlist dropdown above search**

Immediately above the search input's wrapper, add:

```html
<div id="playlistFilterWrap" class="playlist-filter-wrap" hidden>
  <select id="playlistFilter" class="playlist-filter" aria-label="Playlist"></select>
</div>
```

- [ ] **Step 3: Add the tab bar below the dropdown, above search**

```html
<div id="sidebarTabs" class="sidebar-tabs" role="tablist">
  <button id="tabChannels" class="sidebar-tab active" role="tab" aria-selected="true" data-tab="channels">Channels</button>
  <button id="tabMovies"   class="sidebar-tab"        role="tab" aria-selected="false" data-tab="movies">Movies</button>
  <button id="tabSports"   class="sidebar-tab"        role="tab" aria-selected="false" data-tab="sports">Sports</button>
</div>
```

- [ ] **Step 4: Add the placeholder panel near the channel list**

Adjacent to `#channelList` (as a sibling, so it can be shown while the list is hidden):

```html
<div id="tabPlaceholder" class="tab-placeholder" hidden>Coming soon.</div>
```

- [ ] **Step 5: Style the dropdown and tabs**

Append to `web/static/style.css` (match existing color variables — reuse the same custom properties the sidebar already uses; the values below are fallbacks if none exist):

```css
.playlist-filter-wrap { padding: 8px 10px 0; }
.playlist-filter {
  width: 100%; padding: 8px 10px; border-radius: 8px;
  background: #1b1f2a; color: #e6e8ee; border: 1px solid #2a2f3d;
  font-size: 14px;
}
.sidebar-tabs {
  display: flex; gap: 4px; padding: 8px 10px 0;
  border-bottom: 1px solid #2a2f3d;
}
.sidebar-tab {
  flex: 1; padding: 10px 8px; background: transparent; border: none;
  color: #9aa0ad; font-size: 14px; font-weight: 600; cursor: pointer;
  border-bottom: 2px solid transparent;
}
.sidebar-tab.active { color: #e7c14b; border-bottom-color: #e7c14b; }
.tab-placeholder { padding: 24px 16px; color: #9aa0ad; text-align: center; }
```

> If the sidebar already defines CSS custom properties (e.g. `--panel`, `--accent`), use those instead of the literals above so light/dark themes stay consistent.

- [ ] **Step 6: Verify markup loads**

There is no unit test for templates. Verify structurally: Task 8 loads the app. For now confirm the file has no unclosed tags by eye and that the three ids and the wrap/tablist/placeholder ids exist.

- [ ] **Step 7: Commit**

```bash
git add web/templates/index.html web/static/style.css
git commit -m "feat(ui): sidebar playlist dropdown, tab bar, placeholder markup"
```

---

## Task 6: Playlist dropdown + tab behavior module

**Files:**
- Create: `web/static/playlists.js`
- Modify: `web/static/init.js` (import + init the module; remove `beat()` — but beat removal is Task 7, keep it here untouched)

**Interfaces:**
- Consumes: `state.selectedPlaylist`, `state.activeTab`, `els.playlistFilter*`, `els.sidebarTabs`, tab buttons (Task 4); `renderChannelList` from `channels.js`.
- Produces:
  - `export function initPlaylistFilter()` — fetches `GET /api/xtream/playlists`, populates `#playlistFilter`, sets `state.selectedPlaylist` to `list[0].id` (or `''` if empty), shows/hides `#playlistFilterWrap`, wires the `change` handler to set `state.selectedPlaylist` + call `renderChannelList()`.
  - `export function initSidebarTabs()` — wires the three tab buttons to set `state.activeTab`, toggle `.active`/`aria-selected`, and call `renderChannelList()`.

- [ ] **Step 1: Write the module**

Create `web/static/playlists.js`:

```js
import { state, els } from './state.js';
import { renderChannelList } from './channels.js';

// initPlaylistFilter loads saved Xtream playlists into the sidebar dropdown and
// selects the first one (index [0]) by default. Selection is in-memory only —
// it resets to [0] on every page load. Manual channels stay visible regardless.
export function initPlaylistFilter() {
  const sel = els.playlistFilter;
  const wrap = els.playlistFilterWrap;
  if (!sel || !wrap) return;
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) {
      list = Array.isArray(list) ? list : [];
      sel.innerHTML = '';
      if (list.length === 0) {
        wrap.hidden = true;
        state.selectedPlaylist = '';
        renderChannelList();
        return;
      }
      wrap.hidden = false;
      list.forEach(function (p) {
        const opt = document.createElement('option');
        opt.value = p.id;
        opt.textContent = p.name;
        sel.appendChild(opt);
      });
      state.selectedPlaylist = list[0].id; // index [0] default
      sel.value = state.selectedPlaylist;
      renderChannelList();
    })
    .catch(function () {
      wrap.hidden = true;
      state.selectedPlaylist = '';
      renderChannelList();
    });

  sel.addEventListener('change', function () {
    state.selectedPlaylist = sel.value;
    renderChannelList();
  });
}

// initSidebarTabs wires the Channels/Movies/Sports tab bar. Only Channels shows
// the list today; Movies/Sports render a placeholder (handled in channels.js).
export function initSidebarTabs() {
  const tabs = els.sidebarTabs;
  if (!tabs) return;
  const buttons = [els.tabChannels, els.tabMovies, els.tabSports];
  buttons.forEach(function (btn) {
    if (!btn) return;
    btn.addEventListener('click', function () {
      const tab = btn.dataset.tab || 'channels';
      if (state.activeTab === tab) return;
      state.activeTab = tab;
      buttons.forEach(function (b) {
        if (!b) return;
        const on = b === btn;
        b.classList.toggle('active', on);
        b.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      renderChannelList();
    });
  });
}
```

- [ ] **Step 2: Syntax check**

Run: `node --check web/static/playlists.js`
Expected: no output. (Skip if `node` unavailable.)

- [ ] **Step 3: Wire into init**

In `web/static/init.js`, add the import at the top (with the other imports):

```js
import { initPlaylistFilter, initSidebarTabs } from './playlists.js';
```

And in `init()`, after `loadChannels();` (line ~65), add:

```js
  initPlaylistFilter();
  initSidebarTabs();
```

- [ ] **Step 4: Syntax check init**

Run: `node --check web/static/init.js`
Expected: no output. (Skip if `node` unavailable.)

- [ ] **Step 5: Commit**

```bash
git add web/static/playlists.js web/static/init.js
git commit -m "feat(ui): playlist dropdown + tab bar behavior module"
```

---

## Task 7: Filter pipeline (playlist facet + tabs) and remove viewers heartbeat

**Files:**
- Modify: `web/static/channels.js` (`workingSet` ~111-125; `buildFavSection` ~157-184; `renderChannelList` ~190-277; `beat` ~289-303)
- Modify: `web/static/init.js` (remove `beat()` call + interval ~67-68; drop `beat` from the import ~line 3)

**Interfaces:**
- Consumes: `state.selectedPlaylist`, `state.activeTab` (Task 4); `isManual` (already in channels.js).
- Produces: sidebar scoped to selected playlist (+ manual channels always), ★ Favourites scoped identically, Movies/Sports tabs show the placeholder, `/api/viewers` no longer called.

- [ ] **Step 1: Add the playlist facet to `workingSet`**

In `channels.js`, at the START of `workingSet()` (before the `state.country` block), add:

```js
  if (state.selectedPlaylist) {
    const pid = state.selectedPlaylist;
    base = base.filter(function (ch) {
      return ch.xtream_playlist_id === pid || isManual(ch);
    });
  }
```

(`base` is already `state.channels` at that point — insert right after `let base = state.channels;`.)

- [ ] **Step 2: Scope the Favourites section**

In `buildFavSection`, change the first line from:

```js
  let favList = state.channels.filter(isFav);
```

to also apply the playlist-or-manual predicate:

```js
  let favList = state.channels.filter(isFav);
  if (state.selectedPlaylist) {
    const pid = state.selectedPlaylist;
    favList = favList.filter(function (ch) {
      return ch.xtream_playlist_id === pid || isManual(ch);
    });
  }
```

- [ ] **Step 3: Make `renderChannelList` tab-aware**

At the very top of `renderChannelList()`, after `populateCountryFilter();`, add an early branch for non-Channels tabs. `populateCountryFilter()` stays first so the country dropdown remains populated on all tabs.

```js
  populateCountryFilter();
  if (state.activeTab !== 'channels') {
    // Movies/Sports: hide the list + counts, show the placeholder.
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });
    els.listLoading.hidden = true;
    els.emptyState.hidden = true;
    els.channelCount.textContent = '';
    if (els.tabPlaceholder) els.tabPlaceholder.hidden = false;
    return;
  }
  if (els.tabPlaceholder) els.tabPlaceholder.hidden = true;
```

- [ ] **Step 4: Neutralize `beat()` and remove the heartbeat scheduling**

Replace the body of `beat()` in `channels.js` with a no-op (keeps all call sites in audio.js/cell.js/grid.js valid without editing each):

```js
// beat is retained as a no-op: the live-viewers heartbeat (/api/viewers) was
// removed from the client. Call sites remain harmless.
export function beat() {}
```

Also remove the now-dead `watchedChannelId` helper ONLY if it is unused elsewhere — check with a search first; if `cell.js`/`audio.js` reference it, leave it. (It is defined in channels.js right after `beat`; it was only used by `beat`. Remove it if no other file imports it.)

In `web/static/init.js`, remove lines:

```js
  beat();
  setInterval(beat, 30000);
```

and drop `beat` from the import on line 3:

```js
import { renderChannelList } from './channels.js';
```

- [ ] **Step 5: Verify `/api/viewers` is no longer called**

Run: `grep -rn "api/viewers" web/static/`
Expected: no matches in `web/static/` (the only reference was in `channels.js` `beat`, now removed).

- [ ] **Step 6: Syntax check**

Run: `node --check web/static/channels.js && node --check web/static/init.js`
Expected: no output. (Skip if `node` unavailable.)

- [ ] **Step 7: Commit**

```bash
git add web/static/channels.js web/static/init.js
git commit -m "feat(ui): playlist-scoped sidebar + tabs; remove viewers heartbeat"
```

---

## Task 8: Console logging in xtream.js + end-to-end verification

**Files:**
- Modify: `web/static/xtream.js` (`importPlaylist` `.then` ~106-121; save `.then` ~139-160)

**Interfaces:**
- Consumes: the `debug` object from Task 3's endpoint responses.
- Produces: on refresh/import, the raw panel payloads are `console.log`ed verbatim in the browser console.

- [ ] **Step 1: Log debug on refresh/import**

In `web/static/xtream.js`, in `importPlaylist`, change the `.then` that currently does `setBusy(...); loadChannels(); initXtreamTab();` to first log the debug payload. The refresh fetch currently discards the JSON (`.then(function () {...})`); capture it:

```js
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ('refresh failed: ' + r.status)); });
      return r.json();
    })
    .then(function (d) {
      logXtreamDebug(d);
      setBusy(busyBtn, false);
      loadChannels();
      initXtreamTab();
    })
```

- [ ] **Step 2: Log debug on save**

In the save handler's `.then(function (d) {...})` (the one that calls `resetXtreamTab(); loadChannels();`), add `logXtreamDebug(d);` as the first line inside that callback.

- [ ] **Step 3: Add the logger helper**

Add near the bottom of `xtream.js` (before `friendly`):

```js
// logXtreamDebug prints the raw, unmodified panel responses the server relayed
// (login/categories/streams) to the browser console. Fires only on manual
// refresh/import — the startup sweep never returns a debug block.
function logXtreamDebug(d) {
  if (!d || !d.debug) return;
  console.log('[xtream] raw login', d.debug.login);
  console.log('[xtream] raw categories', d.debug.categories);
  console.log('[xtream] raw streams', d.debug.streams);
}
```

- [ ] **Step 4: Syntax check**

Run: `node --check web/static/xtream.js`
Expected: no output. (Skip if `node` unavailable.)

- [ ] **Step 5: Commit**

```bash
git add web/static/xtream.js
git commit -m "feat(xtream): log raw panel payloads to browser console on refresh"
```

- [ ] **Step 6: Full Go suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 7: End-to-end manual verification (use the `run` skill)**

Launch the app and confirm, in order:
1. Sidebar shows the playlist dropdown (if ≥1 Xtream playlist) with the first playlist selected.
2. Left list shows only that playlist's channels PLUS any manual channels; ★ Favourites shows only favourites within that playlist + manual favourites.
3. Switching the dropdown re-scopes the list instantly (no network call in the Network tab).
4. Tabs `Channels`/`Movies`/`Sports` render; Movies/Sports show "Coming soon"; Channels shows the list.
5. "All Countries" filter still present and functional.
6. Refresh a playlist (add-modal → Xtream → Refresh) → browser console prints `[xtream] raw login/categories/streams` with the verbatim payloads.
7. Network tab shows NO `/api/viewers` requests.

- [ ] **Step 8: Final commit (if verification required tweaks)**

```bash
git add -A && git commit -m "fix(ui): address end-to-end verification findings"
```

---

## Self-Review

**Spec coverage:**
- Playlist tag in `/api/channels` → Task 1. ✓
- Playlist dropdown, default [0], no persistence → Tasks 4,5,6. ✓
- Sidebar scoping (playlist + manual; favourites scoped) → Task 7. ✓
- Tabs Channels/Movies/Sports, placeholders → Tasks 4,5,6,7. ✓
- Remove `/api/viewers` client call → Task 7. ✓
- Xtream raw console logging, refresh/import only, verbatim → Tasks 2,3,8. ✓
- "All Countries" kept → untouched (Task 7 keeps `populateCountryFilter` first). ✓

**Type consistency:** `xtream_playlist_id` (JSON) / `XtreamPlaylistID` (Go) consistent across Tasks 1,3,7. `state.selectedPlaylist` / `state.activeTab` consistent across Tasks 4,6,7. `debug.{login,categories,streams}` consistent across Tasks 3,8. `LoginRaw`/`LiveStreamsRaw`/`LiveCategoriesRaw` signatures consistent across Tasks 2,3.

**Placeholder scan:** No TBD/TODO. Test helper names are concrete and verified against the source: `testMux(t) → (mux, _, st)`, `do(t, mux, method, target, body)`, `xtreamPanel(t, streams)`, `findChannel(t, st, id)` (main_test.go); `stubPanel(t, login, streams, cats)` and const `okLogin` (xtream_test.go). Existing refresh/save assertions decode into narrow structs that ignore the new `debug` key, so Task 3 adds no regressions.

**Known stub behavior:** `xtreamPanel` serves the login object for `get_live_categories` (not an array), so `LiveCategoriesRaw` returns a decode error and the handlers set `catsRaw = nil` → `debug.categories` is `null` in tests. Tests assert on `debug.streams` only, which is consistent.
