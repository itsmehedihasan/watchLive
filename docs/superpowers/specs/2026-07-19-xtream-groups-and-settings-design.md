# Design: Xtream category groups + per-playlist settings

Date: 2026-07-19

Follow-up to `2026-07-19-xtream-and-sidebar-design.md`. That spec shipped the
Xtream Codes tab (saved playlists, add-a-playlist sub-form, live-channel
import/refresh) but dumped every imported channel into one neutral group and
exposed no per-playlist settings. This spec adds real category grouping and two
per-playlist settings, staying within the "live channels only" scope.

## Scope

**In scope:**

1. **Category grouping** — imported Xtream live channels are grouped by their
   real panel category name ("US - Movies", "US - Sports", "Main Events / PPV",
   ...), preserving the panel's own category order, matching the TELEVIZO-style
   reference the user provided.
2. **Per-playlist "How often to update"** — `manual` / `daily` / `3days` /
   `weekly`. On server startup, any non-`manual` playlist older than its
   interval is auto-refreshed. No background timer.
3. **Per-playlist "Stream type"** — `ts` (MPEG-TS) or `m3u8` (HLS). Controls the
   extension when building each channel's stream URL on import/refresh.

**Dropped entirely** (per user): "Turn on playlist" (all playlists always on),
"Enable channels".

**Deferred — not built, not shown:** VOD movies, series, archive/catch-up
duration, EPG loading, "use all program guides". The "Movies"/"Sports" the user
saw in the mockup are just live-channel *category* groups (item 1), not the VOD
tab.

## 1. Category grouping (core change)

### Problem

The current import (`Store` Xtream import/refresh) writes every channel with
`typ = manualType` ("Entertainment") and `grp = manualGroup` ("Custom"), because
`get_live_streams` only returns an opaque numeric `category_id`. So all N
channels collapse into a single browse group instead of the per-category groups
the reference image shows.

The browse renderer (`web/static/channels.js` `renderChannelList`) *already*
groups by `ch.type` and draws the exact group-header / count / collapsible-list
UI the reference shows. The only gap is upstream: channels need their real
category name in `typ`.

### `internal/xtream`

Add:

- `Category` struct: `{ID string; Name string}` (from `category_id`,
  `category_name`), with the same field tolerance as the rest of the client.
- `LiveCategories(server, username, password) ([]Category, error)` — GET
  `player_api.php?...&action=get_live_categories`. Same rules as `LiveStreams`:
  verify auth first (bad creds → `ErrAuth`, not empty list), 15s timeout,
  non-2xx → error, non-JSON body → error, missing/extra fields tolerated. Order
  is preserved as returned by the panel (slice order = panel order).

`LiveStreams` already returns each `Stream`'s `CategoryID`; no change there.

### Store import path

The caller (import/refresh) now:

1. Fetches `LiveCategories` alongside `LiveStreams`.
2. Builds a `category_id -> {name, index}` map, where `index` is the category's
   position in the panel's returned slice (0-based).
3. For each stream, resolves its `CategoryID` to a category name. Writes that
   name into `typ` (the browse group). Unmatched / empty `CategoryID` →
   fall back group name `"Uncategorized"` (index = last).
4. `grp` (country) stays neutral (`manualGroup`) — categories are not
   countries, and must not pollute the country dropdown.

Panel category order is preserved via a new per-channel column:

- **New column on `channels`:** `cat_order INTEGER NOT NULL DEFAULT 0`, added via
  the existing `ALTER TABLE ... ADD COLUMN` migration pattern. Holds the
  category's panel index for Xtream channels; 0 for everything else.

The `Channel` struct gains `CatOrder int json:"cat_order"` and it is
selected/scanned in the read path and serialized to the frontend.

Upsert-by-id already updates `typ` on refresh, so renaming/reordering a category
on the panel is reflected on the next refresh. `cat_order` is added to the
upsert `SET` list too.

### Frontend renderer

`renderChannelList` currently orders groups as `CATEGORY_ORDER` (known
categories) then remaining categories alphabetically ("stragglers").

Change the straggler ordering so that groups whose channels carry a non-zero
`cat_order` sort by that index (ascending), *before* falling back to
alphabetical for any group with no ordering signal. Concretely:

- Known `CATEGORY_ORDER` groups still render first (unchanged) so the existing
  manual/M3U catalog behavior is untouched.
- Among the remaining ("straggler") groups: those that carry a non-zero
  `cat_order` (Xtream category groups) render next, ordered by the minimum
  `cat_order` among their channels (= the panel index). Any straggler group with
  no ordering signal (all channels `cat_order == 0`, i.e. non-Xtream) sorts
  after those, alphabetically — the current behavior, just pushed below the
  ordered Xtream groups.

The Favourites section stays on top (unchanged). "All channels" is the existing
full-list behavior — no new virtual group is added in this round (the reference
image's "All channels / Recently watched / Favorites" rows beyond Favourites are
out of scope here).

## 2. Per-playlist settings

### Schema — `xtream_playlists`

Add three columns via `ALTER TABLE ... ADD COLUMN`:

- `update_freq TEXT NOT NULL DEFAULT 'manual'` — `manual` | `daily` | `3days` |
  `weekly`.
- `stream_type TEXT NOT NULL DEFAULT 'ts'` — `ts` | `m3u8`.
- `last_refreshed_at INTEGER NOT NULL DEFAULT 0` — unix seconds; stamped by
  every refresh/import; drives the startup interval check.

### Stream type

The import/refresh path passes the playlist's `stream_type` as the `ext`
argument to `xtream.StreamURL` (already parameterized, defaults to `ts`).
Changing stream type takes effect on the next refresh (the UI offers to refresh
after the change — see §3).

### Update frequency (startup interval check)

On server startup, after the store opens, run a one-shot sweep:

- For each saved playlist with `update_freq != 'manual'`, compute
  `due = last_refreshed_at + interval(update_freq)` where `daily`=24h,
  `3days`=72h, `weekly`=168h.
- If `now >= due` (or `last_refreshed_at == 0`), run the same refresh code path
  the Refresh button uses. Refresh stamps `last_refreshed_at = now`.
- Failures are logged and skipped, never fatal (a dead panel must not block
  startup).

The selection logic is factored as a pure function
`playlistsDueForRefresh(playlists, now) []id` for unit testing; the startup
caller wires it to the real clock + refresh call. No long-running goroutine /
timer — a refresh only happens at startup or on an explicit click.

### Endpoint

`PATCH /api/xtream/playlists/{id}` — body `{update_freq?, stream_type?}`.
Validates each field against its allowed set, persists, returns the updated
playlist (password omitted, consistent with the existing GET). Unknown / invalid
values → 400 with an inline-friendly message.

`GET /api/xtream/playlists` gains `update_freq` and `stream_type` in its
response (still no password) so the UI can populate the dropdowns.

## 3. UI

In the Xtream Codes tab, below the saved-playlist dropdown and only when a
playlist is selected, add a settings block matching the reference rows:

- Row "How often to update" → `<select>`: Manual / Everyday / Every 3 days /
  Weekly (values `manual`/`daily`/`3days`/`weekly`).
- Row "Stream type" → `<select>`: MPEG-TS (ts) / HLS (m3u8) (values
  `ts`/`m3u8`).

Behavior:

- Selecting a saved playlist populates both selects from the cached
  `loadedPlaylists` entry.
- Changing "How often to update" fires the `PATCH` immediately; on success,
  updates the cached entry. Errors → `#xtreamError`.
- Changing "Stream type" fires the `PATCH`, then prompts inline that a refresh
  is needed to re-point existing channels' URLs, offering the existing Refresh
  action (does not auto-refresh).
- The block is hidden when no playlist is selected or none are saved
  (reuses the `#xtreamSavedWrap` visibility model).

Styled with the existing `.add-field` / row patterns and the country-select
styling already used by `#xtreamSaved`. No new modal or overlay.

## 4. Testing

- `internal/xtream`: `LiveCategories` against a `httptest.Server` — happy path
  (order preserved), `auth:0` → `ErrAuth`, malformed JSON → error, missing
  fields → zero values.
- `internal/store`:
  - import maps `category_id` → category name into `typ`, sets `cat_order`,
    unmatched → `"Uncategorized"`.
  - `PATCH` settings persistence (valid + invalid values).
  - `playlistsDueForRefresh(playlists, now)` pure-function cases: manual never
    due; each interval boundary just-before / just-after; `last_refreshed_at==0`
    always due.
- Frontend: manual in-browser verification (groups appear in panel order with
  counts; settings persist and reload; stream-type change + refresh re-points
  URLs).
