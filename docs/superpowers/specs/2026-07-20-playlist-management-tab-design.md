# Playlist management tab

Date: 2026-07-20

A third tab, "Playlist", in the existing "Add from Xtream Codes" modal
(`Manual / Xtream Codes / Playlist`). Manages already-saved Xtream playlists:
rename, change stream type, delete. The Xtream Codes tab's add-a-playlist form
and saved-playlist dropdown are unchanged — this tab is purely for managing
what's already saved.

## UI

`web/templates/index.html`: new tab button `#addTabPlaylist` (`data-tab:
playlist`) next to the existing two, and a new panel `#playlistPanel`
(`add-form add-tab-panel`, `data-tab="playlist"`, `hidden`) rendering one row
per saved playlist:

- Name — click-to-edit inline: click turns it into a text input, saves on
  blur/Enter (empty → reverts, no request), Escape cancels without saving.
- Stream type — a `<select>` (`ts` / `m3u8`), auto-saves on change.
- Remove button — `confirm("Remove playlist \"<name>\"?")` → `DELETE
  /api/xtream/playlists/{id}` → reload the row list + `loadChannels()`.

New `web/static/playlist-tab.js` module: `initPlaylistTab()` fetches `GET
/api/xtream/playlists` and renders the rows (called each time the tab is
shown, mirroring `initXtreamTab()`'s freshness pattern). No caching beyond the
current render.

## Backend

### Partial PATCH

`PATCH /api/xtream/playlists/{id}` body becomes fully optional:
`{name?, update_freq?, stream_type?}`. Only keys present in the JSON are
validated and applied; absent keys leave that column untouched. Decode into
`struct { Name, UpdateFreq, StreamType *string }` so "absent" and "empty
string" are distinguishable.

Store: replace `UpdateXtreamSettings` with

```go
func (s *Store) UpdatePlaylistFields(id string, name, updateFreq, streamType *string) (XtreamPlaylist, error)
```

- Builds the `SET` clause dynamically from the non-nil args.
- `updateFreq`/`streamType`, if provided, are validated against the existing
  `validUpdateFreq`/`validStreamType` maps → `ErrInvalidSetting` on a bad
  value.
- `name`, if provided, is trimmed and must be non-empty → `ErrInvalidSetting`
  if it trims to "".
- If all three args are nil, it's a no-op read: skip the UPDATE and just
  return the current row.
- `ErrNotFound` if the id doesn't exist (checked via `RowsAffected` when an
  UPDATE runs, or a existence check on the no-op path).

Handler updates the body struct and the call site accordingly; error mapping
(`ErrNotFound`→404, `ErrInvalidSetting`→400) unchanged.

### Delete

`DELETE /api/xtream/playlists/{id}`: new handler. Store:

```go
func (s *Store) DeleteXtreamPlaylist(id string) (channelsDeleted int, err error)
```

One transaction:
- `DELETE FROM channels WHERE xtream_playlist_id = ?`
- `DELETE FROM xtream_playlists WHERE id = ?`

`ErrNotFound` when the id matches no playlist (zero rows affected on the
playlist delete). Returns the channel-delete count.

Handler: call it, map `ErrNotFound`→404, `channels.rebuild()`, return
`{deleted}`.

## Out of scope

- No imported-channel-count display on rows.
- No update-frequency control on this tab (still only on the Xtream Codes
  tab's dropdown-select settings block).
- No drag-reorder, search, or pagination — the saved-playlist list is small
  (single local user).

## Tests

Store-level:
- `UpdatePlaylistFields`: each field updatable independently; providing none
  is a no-op read; invalid enum values and empty name rejected;
  unknown id → `ErrNotFound`.
- `DeleteXtreamPlaylist`: removes the playlist and its channels (including
  favourited ones from it); unknown id → `ErrNotFound`.
