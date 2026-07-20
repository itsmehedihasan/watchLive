# Channel List tab + add-modal sizing — design

Date: 2026-07-20

## Goal

Add a fourth tab, **Channel List**, to the Add/Manage modal (`#addChannel`). The tab
lets the user pick a saved Xtream playlist from a dropdown and see a read-only list of
that playlist's channels, each with its stream address(es), which can be copied by
clicking. Also resize the modal: fixed width 500px, height clamped to 350–700px with
vertical scrolling.

## Scope

- **In:** new tab button + panel, playlist dropdown, read-only channel/address list,
  click-to-copy, modal sizing.
- **Out:** editing channels or URLs, any new backend endpoint, changes to the sidebar
  playlist filter, pagination/virtualization.

## Data model (existing, reused)

- `state.channels` — already loaded once via `GET /api/channels` on startup
  ([init.js:16](../../../web/static/init.js#L16)). Each channel:
  - `name` — display name.
  - `servers` — array of `{ url, ... }`; a channel usually has one server but may have
    several. We render **every** `servers[].url`.
  - `xtream_playlist_id` — ties the channel to its saved playlist; empty for manual /
    `.m3u`-imported channels.
- Saved playlists — `GET /api/xtream/playlists` (already cached elsewhere). Each has
  `{ id, name, ... }`. Used to populate the dropdown by name.

No new API calls are required for the list itself; it filters the in-memory channel set.
The dropdown fetches `/api/xtream/playlists` the same way [playlist-tab.js:12](../../../web/static/playlist-tab.js#L12) does.

## UI

### Tab bar ([index.html:220-224](../../../web/templates/index.html#L220))

Add a fourth button after Playlist:

```html
<button type="button" id="addTabChannels" class="add-tab" data-tab="channels">Channel List</button>
```

### Panel ([index.html](../../../web/templates/index.html), after `#playlistPanel`)

```html
<!-- Tab 4 — Channel List: pick a playlist, view its channels' addresses (read-only). -->
<div id="channelListPanel" class="add-form add-tab-panel" data-tab="channels" hidden>
  <label class="add-field">
    <span>Playlist</span>
    <select id="channelListFilter" class="country-select"></select>
  </label>
  <div id="channelListItems" class="channel-addr-list"></div>
  <p id="channelListEmpty" class="add-hint" hidden>No channels in this playlist.</p>
</div>
```

### Row layout

Each channel renders as a card:

- **Line 1:** channel name (bold).
- **Below:** one element per `servers[].url`, read-only. Clicking copies the URL to the
  clipboard and briefly shows "Copied!" (revert after ~1.2s). Long URLs **wrap** to
  multiple lines (`word-break: break-all`) so the full address is always visible; rows
  grow as needed.

### Dropdown behavior

- Options: a disabled `Select Playlist` placeholder (value `""`) followed by one option
  per saved playlist, labeled by name, valued by playlist id.
- On `change`, re-render the list filtered to the selected playlist id.
- Default selection: the placeholder — nothing is listed until the user picks a
  playlist.

### Empty state

If the filtered set is empty (no channels for that playlist, or none loaded yet), show
`#channelListEmpty` and hide the list.

## Module: `web/static/channel-list-tab.js`

Mirrors `playlist-tab.js`. Exports `initChannelListTab()`:

1. Fetch `/api/xtream/playlists`, (re)build `#channelListFilter` options, preserving the
   current selection if it still exists.
2. Render the list for the current selection from `state.channels`.
3. Bind the dropdown `change` handler once to re-render.

Click-to-copy uses `navigator.clipboard.writeText(url)`; on resolve, swap the element's
text to "Copied!" and restore after a timeout. If the clipboard API is unavailable or
rejects, fall back to leaving the text unchanged (no error dialog).

`initChannelListTab()` is called each time the tab is shown (like `initPlaylistTab`), so
it always reflects the latest `state.channels`.

## Wiring: `web/static/modals.js`

- Import `initChannelListTab`.
- `setAddTab('channels')`: toggle `addTabChannels.active`, hide the other panels, show
  `#channelListPanel`, set the modal title (e.g. "Channel list"), and call
  `initChannelListTab()`.
- Add the `channels` cases to the panel-visibility toggles in `setAddTab`
  ([modals.js:40-52](../../../web/static/modals.js#L40)).
- Add a click listener on `addTabChannels`.
- Register `addTabChannels` and the new element ids in
  [state.js](../../../web/static/state.js) `els`.

## Modal sizing ([style.css](../../../web/static/style.css))

On the add modal's panel (`.add-panel`):

- `width: 500px;` (and `max-width: 500px` so it never exceeds it on wide screens).
- `min-height: 350px;`
- `max-height: 700px;`
- `overflow-y: auto;` so long channel lists scroll within the panel rather than growing
  past the viewport.

The channel address list (`.channel-addr-list`) itself does not need its own scrollbar —
the panel scrolls. URLs wrap rather than scroll horizontally, so the panel body never
overflows horizontally.

## Testing

Manual verification (frontend-only, no test harness for the JS UI in this repo):

1. Open the modal → **Channel List** tab appears fourth.
2. Dropdown lists All + each saved playlist by name.
3. Selecting a playlist shows only its channels; each shows name + all server URLs.
4. Clicking a URL copies it (paste elsewhere to confirm) and shows "Copied!" briefly.
5. A playlist with no channels shows the empty hint.
6. Modal is 500px wide; a long channel list scrolls vertically within 350–700px height;
   long URLs wrap, no horizontal scrollbar.
7. `go build ./...` still compiles (new static file is picked up by the embed).
