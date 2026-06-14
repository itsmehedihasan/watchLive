# watchlive (Go)

Single-binary live TV streaming server: web UI, HLS stream proxy, and live viewer counter in one executable with zero runtime dependencies. Everything — web assets and a default starter playlist — is embedded, so it runs with no external files. Drop a `list.m3u` next to the binary to override the bundled playlist.

The UI has two independent browse panels: a **category** accordion on the left (News / Sports / Movies / Music / Kids / Religious / Entertainment) and a **country** accordion on the right with search and sync. Categories come from iptv-org's channel database, joined on `tvg-id`.

---

## Download & Run (Windows)

No install, no Go, no config:

1. Download **`watchlive-windows-amd64.zip`** from the [Releases](../../releases) page.
2. Unzip it anywhere.
3. Double-click **`WatchLive.bat`**.

The server starts, your browser opens to `http://localhost:3000`, and channels appear
immediately — a default playlist is bundled into the binary, so it works even offline.
With internet, the list refreshes from iptv-org in the background. Close the console
window to stop the server.

> Building from source instead? See [Build](#build) below.

---

## Prerequisites

- **Go 1.24.5 or newer** — [go.dev/dl](https://go.dev/dl/)
- Verify: `go version`

No other runtime dependencies.

---

## Build

```sh
go build -ldflags "-s -w" -o watchlive.exe .
```

Omit `-ldflags "-s -w"` if you need a debuggable build.

---

## Playlist (`list.m3u`)

The binary ships with an embedded default playlist (`seed.m3u`), so it shows channels on first run with no setup. To use your own, place a standard M3U file named `list.m3u` next to the binary — it overrides the bundled default. The server reads it on startup and hot-reloads it automatically.

```m3u
#EXTM3U
#EXTINF:-1 tvg-logo="https://example.com/logo.png" group-title="News",BBC World News
https://stream.example.com/bbc.m3u8
```

---

## Run

```sh
./watchlive.exe              # http://localhost:3000
```

### Flags

| Flag         | Default                   | Description                                                                      |
|--------------|---------------------------|----------------------------------------------------------------------------------|
| `-addr`      | `:3000`                   | Listen address                                                                   |
| `-playlist`  | `list.m3u` next to binary | Path to M3U playlist                                                             |
| `-cache-mb`  | `200`                     | In-memory HLS segment cache size (MB)                                            |
| `-open`      | `true`                    | Open the web UI in the default browser once the server is listening              |
| `-sync-url`  | iptv-org index URL        | Upstream source used by `POST /api/sync`                                         |

```sh
./watchlive.exe -addr :8080
./watchlive.exe -playlist /path/to/channels.m3u
./watchlive.exe -cache-mb 50
```

---

## Adding Channels

Edit `list.m3u` while the server is running — changes are picked up automatically within **10 seconds**. No restart needed.

Force an immediate reload:

```sh
curl -X POST http://localhost:3000/api/reload
```

Refresh the browser after a reload to see the updated list.

---

## Import from iptv-org

Fetches free streams from [iptv-org/iptv](https://github.com/iptv-org/iptv) and appends new deduplicated entries to `list.sync.m3u`.

```sh
go build -o import.exe ./cmd/import

./import.exe                          # all countries
./import.exe -country bd,us,gb        # specific countries only
./import.exe -out list.m3u -country bd -concurrency 20
```

| Flag            | Default    | Description                                      |
|-----------------|------------|--------------------------------------------------|
| `-out`          | `list.sync.m3u` | Output file (appended, deduplicated)        |
| `-country`      | *(all)*    | Comma-separated ISO country codes (e.g. `bd,us`) |
| `-concurrency`  | `12`       | Parallel HTTP fetches                            |

---

## Endpoints

| Method       | Path                        | Description                                                                                 |
|--------------|-----------------------------|---------------------------------------------------------------------------------------------|
| `GET`        | `/`                         | Web UI (channel list injected server-side)                                                  |
| `GET`        | `/api/channels`             | Parsed playlist as JSON; gzip-compressed, ETag revalidation (304 when unchanged)            |
| `GET`        | `/api/proxy?url=<url>`      | HLS proxy: spoofs browser headers, rewrites playlist URLs, serves segments from LRU cache (`X-Cache: HIT\|MISS`) |
| `GET\|POST`  | `/api/viewers`              | Heartbeat + live counts (`{total, channelCount, top}`); sessions expire 60 s after last heartbeat |
| `POST`       | `/api/reload`               | Force playlist re-read from disk                                                            |
| `POST`       | `/api/sync`                 | Download upstream, **merge** new streams into `list.m3u` (dedup by URL — never removes existing), then stamp `tvg-genre` on every entry from iptv-org's category DB. Returns `{channels,added,tagged}` |

---

## Tests

```sh
go test ./...
```

---

## Layout

```
main.go                    server wiring, embeds, hot reload, merge-sync, graceful shutdown
internal/playlist/         M3U parser and channel grouping (reads tvg-genre)
internal/genre/            iptv-org category lookup + tvg-genre injection
internal/proxy/            HLS proxy + LRU segment cache + singleflight
internal/viewers/          session-derived live viewer counts
web/templates/index.html   page template (category + country sidebars)
web/static/                app.js, style.css, vendored hls.min.js (v1.6.16)
cmd/import/                CLI tool to fetch and merge streams from iptv-org
list.m3u                   channel playlist (hot-reloaded from disk)
```
