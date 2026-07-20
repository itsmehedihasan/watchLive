# watchlive (Go)

Single-binary live TV streaming server: web UI, HLS/DASH stream proxy, and screen recording in one executable with near-zero runtime dependencies. Web assets are embedded, so it runs with no external files.

Channels live in a local **SQLite catalog** (`store/catalog.db`, created next to the binary). Your favourites and manually-added channels persist in that DB and survive re-syncs. (The upstream [iptv-org](https://github.com/iptv-org/iptv) refresh and Sync are wired but currently disabled.)

The UI is a multi-cell **video wall**: each cell plays its own channel. Browse via a **category** panel (News / Sports / Movies / Music / Kids / Religious / Entertainment, joined from iptv-org's database on `tvg-id`) and a **country** panel with search. Plus: a **Favourites** section, manual channel add, **.m3u import** with duplicate detection, optional **screen recording** to MP4, and **ClearKey** decryption for CENC-protected DASH streams.

---

## Download & Run (Windows)

No install, no Go, no config:

1. Download **`watchlive-windows-amd64.zip`** from the [Releases](../../releases) page.
2. Unzip it anywhere.
3. Double-click **`WatchLive.bat`**.

The server starts, your browser opens to `http://localhost:3000`, and channels are served
from the local `store/catalog.db`. Close the console window to stop the server.

> Building from source instead? See [Build](#build) below.

---

## Prerequisites

- **Go 1.25.0 or newer** — [go.dev/dl](https://go.dev/dl/)
- Verify: `go version`
- *(Optional)* **ffmpeg** on `PATH` (or `internal/ffmpeg/bin/ffmpeg.exe`) to enable screen recording. Without it, the app runs fine and just hides the record button.

No other runtime dependencies (SQLite is the pure-Go `modernc.org/sqlite`, no cgo).

---

## Build

```sh
go build -ldflags "-s -w" -o watchlive.exe .
```

Omit `-ldflags "-s -w"` if you need a debuggable build.

---

## Run

```sh
./watchlive.exe              # http://localhost:3000
```

### Flags

| Flag                        | Default                    | Description                                                                                  |
|-----------------------------|----------------------------|----------------------------------------------------------------------------------------------|
| `-addr`                     | `:3000`                    | Listen address                                                                               |
| `-db`                       | `store/catalog.db`         | Path to the SQLite catalog                                                                    |
| `-playlist`                 | *(off)*                    | Run a throwaway session from a custom `.m3u` (see [Custom playlist mode](#custom-playlist-mode)) |
| `-source-url`               | iptv-org index URL         | Upstream playlist fetched at startup and by Sync                                             |
| `-no-refresh`               | `false`                    | Skip the startup fetch from `-source-url`; use the catalog as-is                             |
| `-prune`                    | `false`                    | On sync, delete channels no longer upstream (keeps favourited and manual ones)               |
| `-cache-mb`                 | `200`                      | In-memory HLS segment cache size (MB)                                                         |
| `-rec-dir`                  | `recordings`               | Directory for saved screen recordings                                                        |
| `-open`                     | `true`                     | Open the web UI in the default browser once the server is listening                          |
| `-allow-private-upstreams`  | `false`                    | Allow the proxy to fetch loopback/private/link-local addresses (off by default to block SSRF) |

```sh
./watchlive.exe -addr :8080
./watchlive.exe -no-refresh            # work offline from the existing catalog
./watchlive.exe -cache-mb 50
```

---

## Channel catalog

The catalog is a SQLite database; the M3U feed is only the transport format it is
populated from. Channels carry stable IDs (derived from `tvg-id`, else a content hash),
so favourites re-attach to the right rows across re-syncs.

- **Sync** (`POST /api/sync`) and the background iptv-org refresh are wired but **currently disabled** — the server serves the catalog as-is. `refresh()` / `fetchAndEnrich()` and the `-source-url` / `-no-refresh` / `-prune` flags remain in place for a future sync rework. When re-enabled, Sync **upserts** by stable ID — new channels added, existing ones updated, favourites and manual rows preserved.

### Custom playlist mode

Run a self-contained session from your own `.m3u`, fully isolated from the main catalog:

```sh
go run . --playlist list.m3u
# or, built:
./watchlive.exe --playlist list.m3u
```

In this mode the app:

- loads **only** the channels in your file;
- uses a **separate** database (`store/playlist.db`) — your real `catalog.db` is never read or modified;
- **resets that DB on every start**, so each run begins fresh "like a new server" (favourites/adds from a prior `--playlist` run are discarded);
- **disables the iptv-org refresh**, and **refuses Sync** (returns `409`) so the upstream catalogue can never leak into your custom session.

The playlist file is validated up front — a missing, empty, or non-M3U file fails fast with a clear error. Standard M3U format:

```m3u
#EXTM3U
#EXTINF:-1 tvg-logo="https://example.com/logo.png" group-title="News",BBC World News
https://stream.example.com/bbc.m3u8
```

---

## Adding channels

Three ways, all via the running app — no file editing or restart:

- **+ Add** (rail button): name + stream link, with an optional `KID:KEY` ClearKey for DRM streams.
- **Import**: pick a `.m3u`, review/edit the parsed entries, and save. Imports are de-duplicated by stream **link** against the whole catalog, and conflicts are reported before you commit.
- **`--playlist`**: bring your own playlist as the entire catalog (see above).

Manually-added and imported channels are marked manual and are never overwritten or pruned by a Sync.

---

## Import from iptv-org (CLI)

Standalone tool that fetches free streams from [iptv-org/iptv](https://github.com/iptv-org/iptv), de-duplicates them, and writes an M3U (stamping `tvg-genre` from iptv-org's category DB).

```sh
go build -o import.exe ./cmd/import

./import.exe                          # all countries
./import.exe -country bd,us,gb        # specific countries only
./import.exe -out channels.m3u -country bd -concurrency 20
./import.exe -enrich -out channels.m3u   # only stamp tvg-genre on an existing file
```

| Flag            | Default          | Description                                            |
|-----------------|------------------|--------------------------------------------------------|
| `-out`          | `list.sync.m3u`  | Output file (appended, deduplicated)                   |
| `-country`      | *(all)*          | Comma-separated ISO country codes (e.g. `bd,us`)       |
| `-concurrency`  | `12`             | Parallel HTTP fetches                                  |
| `-enrich`       | `false`          | Only stamp `tvg-genre` on an existing `-out` file (no download) |

The resulting file can be fed to the server with `--playlist`.

---

## Endpoints

| Method        | Path                       | Description                                                                                   |
|---------------|----------------------------|-----------------------------------------------------------------------------------------------|
| `GET`         | `/`                        | Web UI                                                                                         |
| `GET`         | `/api/channels`            | Catalog as JSON; gzip-compressed, ETag revalidation (304 when unchanged)                      |
| `GET`         | `/api/proxy?url=<url>`     | HLS/DASH proxy: spoofs browser headers, rewrites playlist URLs, prefetches & serves segments from an LRU cache; blocks private/loopback upstreams (SSRF guard) |
| `POST`        | `/api/sync`                | Sync is disabled — always returns `403`                                                       |
| `GET`         | `/api/source`              | Refresh status (`{refreshing, channels, recordingAvailable}`) for the UI to poll              |
| `POST`        | `/api/favourite`           | `{id, on}` — toggle a channel's favourite flag                                                |
| `POST`        | `/api/channels/add`        | `{name, url, license?}` — add a manual channel                                                |
| `POST`        | `/api/channels/update`     | `{id, name, url, license?}` — edit a manual channel                                           |
| `DELETE`      | `/api/channels/add`        | `{id}` — remove a manual channel                                                              |
| `POST`        | `/api/import/parse`        | Raw `.m3u` body → extracted `{entries}` (no save)                                             |
| `POST`        | `/api/import/check`        | `{entries}` → `{new, duplicates}` (link de-dup vs catalog; no save)                           |
| `POST`        | `/api/import/save`         | `{entries}` → `{added}` — persist reviewed entries as manual channels                         |
| `GET`         | `/api/keys`                | ClearKey DRM map (`{kid: key}`)                                                               |
| `POST`        | `/api/record/start\|stop`  | Start/stop a server-side recording (ffmpeg → 720p H.264/AAC MP4)                              |
| `GET`         | `/api/record`              | List recordings (active and finished)                                                         |
| `GET`         | `/api/record/file?name=`   | Download a saved recording                                                                    |

---

## Tests

```sh
go test ./...
```

---

## Layout

```
main.go                    server wiring, flags, embeds, graceful shutdown
internal/store/            SQLite catalog (channels, favourites, manual rows)
internal/playlist/         M3U parser, stable channel IDs, ClearKey extraction
internal/genre/            iptv-org category lookup + tvg-genre enrichment
internal/proxy/            HLS/DASH proxy, LRU segment cache, prefetch, SSRF guard
internal/keystore/         ClearKey DRM key store (keys.json)
internal/recorder/         ffmpeg-driven screen recording to MP4
internal/ffmpeg/           ffmpeg resolver (embedded copy or PATH)
web/templates/index.html   page template
web/static/                ES-module frontend, style.css, vendored hls.js / shaka / mpegts
cmd/import/                CLI tool to fetch and enrich streams from iptv-org
```
