# Design: DB cleanup, sidebar merge, Xtream Codes playlists

Date: 2026-07-19

## 1. One-time DB cleanup

Run a one-off cleanup against `store/catalog.db`: delete every channel row
where `is_favourite=0 AND is_manual=0`. This is the same predicate
`Store.PruneOrphans` already uses to decide "the user has no stake in this
channel," just applied unconditionally instead of restricted to channels
absent from the latest feed.

- Favourited channels are kept regardless of source.
- Manually-added channels (`is_manual=1`) are kept — this includes
  hand-added channels, imported `.m3u` entries, and (after this feature)
  Xtream-imported channels.
- This is a one-time operation, not a new permanent feature/button. No new
  endpoint or UI is added for it.

## 2. Sidebar merge (left drawer removed, right drawer is the single browse UI)

**Removed:** `#categorySidebar` (left drawer), `#leftDrawerToggle`, and the
category-only grouping/rendering path that fed it.

**`#sidebar` (right drawer)** becomes the only browse drawer. Its contents,
top to bottom:

1. Header: "Browse" + close button (was "Countries").
2. Search box (unchanged behavior).
3. **Country dropdown**, default option "All countries". Selecting a country
   narrows the working channel set to that country before grouping.
4. Channel list, grouped by **category** (the grouping logic currently used
   by the left drawer), built from whatever subset the country dropdown and
   search box leave.
5. Footer: existing "Working only" health toggle + channel count (unchanged).

**Filter pipeline** (applied in this order): country facet → category
grouping (section headers within the list) → text search (further narrows
within each group, same as today's search behavior).

`channels.js` and `grid.js` currently hold separate rendering paths for the
category drawer and the country drawer; these merge into one list-builder
function parameterized by the selected country and the search term, emitting
category-grouped sections.

## 3. "+" Add popup: Manual / Xtream Codes tabs

The rail's "+" button (`#addChannelBtn`) opens a modal (same overlay
technique as the existing `.picker` / `.add-modal` components — an
absolutely-positioned panel, not a native OS window) with two tabs:

### Tab 1 — "Manual"

Exactly today's "Add a channel" form (name, stream link, optional
referer/user-agent/license-key, Save/Cancel), relocated under this tab
without behavior changes. Existing `/api/channels/add` endpoint, unchanged.

### Tab 2 — "Xtream Codes"

**Layout:**

- **Saved playlists dropdown** — lists playlists previously saved via this
  tab. Empty/hidden if none exist yet.
  - Selecting a playlist **never imported before**: immediately fetches its
    live channel list from the Xtream server and imports it (spinner shown
    during the fetch; errors shown inline).
  - Selecting a playlist **already imported**: just selects it in the UI, no
    network call.
  - **"Refresh"** button next to the dropdown: re-fetches the currently
    selected playlist's live channels from the server and upserts them
    (updates existing rows, adds new ones — see upsert rule below).
- **Add new playlist sub-form** (always visible below the dropdown):
  - Playlist name (text, required)
  - Username (text, required)
  - Password (password input, required)
  - Server address (text, required, must start with `http://`, `https://`,
    matching the existing hint pattern from `addChannelUrl`)
  - "Save & Import" button: saves the playlist row, immediately fetches and
    imports its live channels (no review/edit step — matches the "save
    immediately" decision), then selects it in the dropdown.
  - Inline error area, same visual pattern as `#addChannelError`.

**Out of scope for this version** (explicitly deferred, per the "core only"
scope decision): VOD movies, series, EPG loading, scheduled auto-update
frequency, archive/catch-up duration, stream-type selector, "use default
User-Agent" toggle. Only the live-channel list is fetched and imported.

### Backend

**New package `internal/xtream`** — a thin client for the Xtream Codes
`player_api.php` protocol:

- `Login(server, username, password) (UserInfo, ServerInfo, error)` — GET
  `player_api.php?username=...&password=...`; treats a decodable JSON body
  with `user_info.auth == 0` (or non-2xx, or non-JSON body) as an
  authentication/connectivity error rather than panicking.
- `LiveStreams(server, username, password) ([]Stream, error)` — GET
  `...&action=get_live_streams`; tolerant of missing/extra fields (panels
  vary), 15s client timeout.
- `StreamURL(server, username, password string, streamID int, ext string) string`
  — builds `{server}/live/{username}/{password}/{streamID}.{ext}` (default
  ext `ts`).

Server address normalization: trim trailing slash; require the user to
include the scheme (`http://`/`https://`) and port if non-default — no
guessing/probing of ports.

**New table `xtream_playlists`:**

```sql
CREATE TABLE xtream_playlists (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    server     TEXT NOT NULL,
    username   TEXT NOT NULL,
    password   TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
```

Password is stored in plaintext, consistent with the rest of the app's
local-only, single-user storage model (channel URLs, DRM ClearKeys, etc. are
all plaintext today — see `internal/keystore`). No new crypto dependency.

**New column on `channels`:** `xtream_playlist_id TEXT NOT NULL DEFAULT ''`
(nullable-in-spirit, empty string = not from Xtream), added via the same
`ALTER TABLE ... ADD COLUMN` migration pattern already used for
`clear_keys`/`resolver`/etc.

**Channel ID scheme:** `xtream:<playlist_id>:<stream_id>` — stable across
refreshes, so re-importing the same playlist upserts existing rows (updates
name/logo/servers) instead of duplicating. Imported rows get `is_manual=1`
(survive pruning/re-sync, matches "manual channel" semantics already used by
`.m3u` import) and `is_favourite=0` by default (same reasoning as
`ImportManual`: bulk-adding shouldn't flood Favourites).

**New endpoints:**

- `POST /api/xtream/playlists` — body `{name, server, username, password}`;
  saves the playlist, then synchronously fetches + imports its live streams;
  returns the saved playlist + import count.
- `GET /api/xtream/playlists` — lists saved playlists (id, name, server,
  username — password omitted from the response) with an `imported: bool`
  computed from whether any channel row references that playlist id.
- `POST /api/xtream/playlists/{id}/refresh` — re-fetches live streams for an
  existing saved playlist and upserts channel rows by the
  `xtream:<playlist_id>:<stream_id>` scheme; returns added/updated counts.

## Testing

- `internal/xtream`: unit tests against a `httptest.Server` stubbing
  `player_api.php` responses (happy path, `auth:0`, malformed JSON, missing
  fields).
- `internal/store`: unit tests for the new playlist table CRUD and the
  channel upsert-by-`xtream_playlist_id` path, following existing
  `store_test.go` conventions.
- Frontend: manual verification in-browser (add playlist, refresh, sidebar
  merge, search/country/category interplay) — no existing JS test harness in
  this repo to extend.
