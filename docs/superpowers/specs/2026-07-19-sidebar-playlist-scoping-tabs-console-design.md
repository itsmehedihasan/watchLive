# Design: Right-sidebar playlist scoping, tabs, and Xtream console

Date: 2026-07-19
Branch: feat/xtream-groups-settings (or a new branch off it)

## Summary

Four changes to the right-hand browse sidebar and the Xtream refresh path:

1. **Playlist dropdown** above the search box that scopes the sidebar to one
   Xtream playlist's channels (default = first playlist in the array).
2. **Playlist tagging** in the `/api/channels` payload so the frontend can
   filter by playlist without refetching.
3. **Tab bar** (`Channels` / `Movies` / `Sports`) at the top of the sidebar;
   `Channels` holds the current view, `Movies`/`Sports` are placeholders.
4. **Remove the `/api/viewers` heartbeat** client call.
5. **Xtream console logging**: the refresh/import endpoint returns the raw,
   unmodified panel responses, which `xtream.js` logs to the browser console.

"All Countries" filter stays (capitalized), unchanged.

## Decisions (locked)

- Playlist dropdown options: saved **Xtream playlists** only (m3u-with-name is a
  later feature, out of scope here).
- Dropdown displays playlist **name**; option value and filter key = playlist
  **id** (`xtream_playlist_id`). Matching on id, not name (rename-safe,
  collision-safe).
- Default selected playlist = **index [0]** of the `GET /api/xtream/playlists`
  array. **No persistence** — resets to [0] on every page load.
- When a playlist is selected:
  - **Left channel list (groups):** channels with
    `xtream_playlist_id === selectedId`, **plus** all `manual:` channels
    (single hand-added channels always stay visible).
  - **★ Favourites section:** favourited channels within the selected playlist,
    plus favourited `manual:` channels (they always stay).
- Third tab label = **Sports** (`Channels` / `Movies` / `Sports`). Default tab =
  `Channels`. Tab state is in-memory (`state.activeTab`), no persistence.
- Console logging fires **only on refresh/import** (the two endpoints below),
  **not** the startup auto-refresh sweep.
- Raw data logged **verbatim** — credentials in URLs are **not** masked; payload
  is unmodified.
- Console output goes to the **browser console** (data is piped from the server
  to the client in the refresh/import response, then `console.log`ed).

## Architecture

### Component 1 — Playlist tag in `/api/channels`

The `channels` table already has an `xtream_playlist_id TEXT` column and Xtream
channels use `xtream:<playlist_id>:<stream_id>` ids. The column is simply not
serialized today.

**Backend changes** (`internal/store/store.go`):

- `Channel` struct: add
  `XtreamPlaylistID string \`json:"xtream_playlist_id"\`` (after `CatOrder` or
  grouped with the other persisted-state fields). Emit always (not `omitempty`)
  so the frontend can rely on the key existing; manual channels serialize `""`.
- `ListChannels` SELECT: add `xtream_playlist_id` to the column list.
- `scanChannel`: add `&ch.XtreamPlaylistID` to the `Scan` call in the matching
  position.

No new endpoint. The precomputed gzip/ETag payload in `main.go` rebuilds from
`ListChannels` on change, so the new field flows through automatically.

**Test:** extend an existing `/api/channels` JSON test (or
`TestImportXtreamStreamsMapsCategories`) to assert an imported Xtream channel's
JSON carries the expected `xtream_playlist_id`, and a manual channel carries
`""`.

### Component 2 — Playlist dropdown (frontend)

**Markup** (`web/templates/index.html`): a `<select id="playlistFilter">` above
the search input in the sidebar header. Wrapper hidden when there are zero
playlists.

**State** (`web/static/state.js`): add `selectedPlaylist: ''` and register
`playlistFilter` in `els`. Add `activeTab: 'channels'`.

**Population**: on load, `GET /api/xtream/playlists`. Build one `<option>` per
playlist (`textContent = p.name`, `value = p.id`). Set
`state.selectedPlaylist = list[0].id` (index [0]) and `sel.value` to match. If
the list is empty, hide the dropdown and treat `selectedPlaylist` as `''`
(no playlist facet → behaves like today: all channels).

- New module `web/static/playlists.js` (or fold into `channels.js`) owns fetch +
  render + the `change` handler. On `change`: set `state.selectedPlaylist`,
  call `renderChannelList()`. No network call.
- Reuse the existing `GET /api/xtream/playlists` endpoint; no backend work.

### Component 3 — Sidebar filter semantics

Extend `workingSet()` in `web/static/channels.js`. Current pipeline:
country → health → search. New **first** step, the playlist facet:

```
if (state.selectedPlaylist) {
  base = base.filter(ch =>
    ch.xtream_playlist_id === state.selectedPlaylist || isManual(ch));
}
```

`isManual(ch)` already exists (`ch.id` starts with `manual:`). This keeps all
hand-added channels visible in every playlist, per the locked decision.

When `state.selectedPlaylist === ''` (no playlists exist, or the list is empty)
the playlist facet is skipped entirely and the sidebar behaves exactly as today
(all channels, all groups).

`buildFavSection()` uses the same facet: favourites are drawn from
`state.channels.filter(isFav)` then filtered by the same playlist-or-manual
predicate, so the ★ Favourites count/list is dynamic per playlist.

Country + health + search continue to apply on top, unchanged.

### Component 4 — Tab bar (`Channels` / `Movies` / `Sports`)

**Markup**: a tab bar at the top of the sidebar (below the playlist dropdown).
Three buttons styled per the reference screenshot (dark bar, active tab = yellow
text + underline). `role="tab"`, active reflects `state.activeTab`.

**Behavior**:

- `state.activeTab === 'channels'` → render the full current view (Favourites +
  all groups) via `renderChannelList()`. This is everything today.
- `state.activeTab === 'movies'` / `'sports'` → render a placeholder panel
  ("Coming soon"); the channel list, count, and empty-state are hidden/replaced.
  Structurally wired so we can fill them later.
- Clicking a tab sets `state.activeTab` and re-renders. No persistence.

`renderChannelList()` early-returns to the placeholder when `activeTab` is not
`channels`, so the country/health/search pipeline only runs for the Channels tab.
The early-return happens **after** `populateCountryFilter()` (or that call moves
out of `renderChannelList` into init) so the country dropdown stays populated and
is not stranded when the user is on the Movies/Sports tab.

### Component 5 — Remove `/api/viewers` heartbeat

`beat()` in `channels.js` POSTs `/api/viewers` and is called from `init.js`
(`setInterval(beat, 30000)` + initial call), `audio.js`, `cell.js`, `grid.js`.

**Approach:** make `beat()` a no-op and remove the interval + call sites, or
delete `beat()` and its callers outright. Chosen: **remove the interval and the
scheduled/initial calls, and reduce `beat()` to a no-op** to avoid touching every
call site's control flow — simplest, lowest-risk. Also remove
`state.topChannelIds` reads that only existed to consume the response.

The **server** `/api/viewers` endpoint stays mounted (harmless, no client uses
it). No backend change.

**Test:** none required (removal of a fire-and-forget call). Existing tests must
still pass.

### Component 6 — Xtream raw-response console logging

Fetches happen server-side (Go, `internal/xtream`). To surface raw data in the
**browser** console, the refresh/import endpoints attach the raw responses to
their JSON, and `xtream.js` logs them.

**Endpoints affected** (both, since both fetch from the panel):

- `POST /api/xtream/playlists` (Save & Import)
- `POST /api/xtream/playlists/{id}/refresh` (Refresh / first import)

**Backend**: the Xtream client currently returns typed structs. To log *raw,
unmodified* data we need the raw bytes. Add a debug channel:

- In `internal/xtream`, capture the raw response body for the login,
  `get_live_categories`, and `get_live_streams` calls (as `json.RawMessage` or
  `string`) alongside the parsed result — without altering existing parsing.
- The two `main.go` handlers include these raw blobs in their JSON response under
  a `debug` key, e.g.:
  `{"playlist": {...}, "added": N, "updated": M, "debug": {"login": <raw>, "categories": <raw>, "streams": <raw>}}`.
- The startup auto-refresh sweep does **not** populate/emit debug (locked: only
  on manual refresh/import).

**Frontend** (`xtream.js`): in the `.then` of the refresh and save fetches, if
`d.debug` is present, `console.log` each raw blob verbatim, e.g.
`console.log('[xtream] login', d.debug.login)` etc.

**Caveat (documented, accepted):** streams payload for 8K+ channels is large and
travels the wire on every manual refresh; credentials appear unmasked in any
logged request URLs. Accepted per locked decisions. To bound risk slightly, the
raw streams blob is only included in the response, logged once, and not stored.

**Test:** assert the refresh endpoint response includes a `debug` object with the
three keys when a playlist is refreshed (using the existing `stubPanel` test
server in `internal/xtream` / `main_test.go`).

## Files touched

- `internal/store/store.go` — `Channel` struct field, `ListChannels`,
  `scanChannel`.
- `internal/xtream/xtream.go` — capture raw response bytes for login/categories/
  streams.
- `main.go` — include `debug` raw blobs in the two refresh/import handler
  responses.
- `web/templates/index.html` — playlist dropdown, tab bar markup.
- `web/static/state.js` — `selectedPlaylist`, `activeTab`, new `els`.
- `web/static/channels.js` — playlist facet in `workingSet()` + `buildFavSection`,
  tab-aware `renderChannelList()`, neutralize `beat()`.
- `web/static/playlists.js` (new) — dropdown fetch/render/handler; tab handlers
  (or fold into `channels.js`).
- `web/static/init.js` — remove `beat()` interval + initial call; init playlist
  dropdown + tabs.
- `web/static/xtream.js` — `console.log` raw `debug` payloads on refresh/import.
- `web/static/style.css` — dropdown + tab-bar styling.

## Out of scope

- Uploading `.m3u` files with a mandatory playlist name (future; the dropdown is
  built to accommodate additional option sources later).
- Filling in the `Movies` / `Sports` tab content.
- Removing the server-side `/api/viewers` endpoint.
- Credential masking in logs.

## Testing strategy

- Go: unit test the new `xtream_playlist_id` serialization; unit test the
  `debug` payload on refresh. Run `go test ./...`.
- Frontend: manual verification per the run skill — select each playlist and
  confirm the list + Favourites scope correctly, manual channels persist across
  playlists, tabs switch, and raw data appears in the browser console on refresh.
