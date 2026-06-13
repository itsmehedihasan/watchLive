# watchlive (Go)

Single-binary live TV streaming server: web UI, HLS stream proxy, and live viewer counter in one ~9 MB executable with zero runtime dependencies. All assets are embedded; the only external file you need is `list.txt`.

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

## Playlist (`list.txt`)

Place a standard M3U file named `list.txt` next to the binary. An embedded fallback is baked in, so the server starts even without one.

```m3u
#EXTM3U
#EXTINF:-1 tvg-logo="https://example.com/logo.png" group-title="News",BBC World News
https://stream.example.com/bbc.m3u8
```

The server also reads `list.sync.m3u` (written by the import tool) from the same directory automatically.

---

## Run

```sh
./watchlive.exe              # http://localhost:3000
```

### Flags

| Flag         | Default                   | Description                                                                      |
|--------------|---------------------------|----------------------------------------------------------------------------------|
| `-addr`      | `:3000`                   | Listen address                                                                   |
| `-playlist`  | `list.txt` next to binary | Path to M3U playlist; falls back to embedded copy if missing                    |
| `-cache-mb`  | `200`                     | In-memory HLS segment cache size (MB)                                            |
| `-sync-url`  | iptv-org index URL        | Upstream source used by `POST /api/sync`                                         |

```sh
./watchlive.exe -addr :8080
./watchlive.exe -playlist /path/to/channels.m3u
./watchlive.exe -cache-mb 50
```

---

## Adding Channels

Edit `list.txt` while the server is running — changes are picked up automatically within **10 seconds**. No restart needed.

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
./import.exe -out list.sync.m3u -country bd -concurrency 20
```

| Flag            | Default         | Description                                      |
|-----------------|-----------------|--------------------------------------------------|
| `-out`          | `list.sync.m3u` | Output file (appended, deduplicated)             |
| `-country`      | *(all)*         | Comma-separated ISO country codes (e.g. `bd,us`) |
| `-concurrency`  | `12`            | Parallel HTTP fetches                            |

---

## Endpoints

| Method       | Path                        | Description                                                                                 |
|--------------|-----------------------------|---------------------------------------------------------------------------------------------|
| `GET`        | `/`                         | Web UI (channel list injected server-side)                                                  |
| `GET`        | `/api/channels`             | Parsed playlist as JSON; gzip-compressed, ETag revalidation (304 when unchanged)            |
| `GET`        | `/api/proxy?url=<url>`      | HLS proxy: spoofs browser headers, rewrites playlist URLs, serves segments from LRU cache (`X-Cache: HIT\|MISS`) |
| `GET\|POST`  | `/api/viewers`              | Heartbeat + live counts (`{total, channelCount, top}`); sessions expire 60 s after last heartbeat |
| `POST`       | `/api/reload`               | Force playlist re-read from disk                                                            |
| `POST`       | `/api/sync`                 | Download fresh streams from `-sync-url` into `list.sync.m3u`                               |

---

## Tests

```sh
go test ./...
```

---

## Layout

```
main.go                    server wiring, embeds, hot reload, graceful shutdown
internal/playlist/         M3U parser and channel grouping
internal/proxy/            HLS proxy + LRU segment cache + singleflight
internal/viewers/          session-derived live viewer counts
web/templates/index.html   page template
web/static/                app.js, style.css, vendored hls.min.js (v1.6.16)
cmd/import/                CLI tool to fetch and merge streams from iptv-org
list.txt                   user-curated channel playlist (embedded fallback)
list.sync.m3u              downloaded synced playlist (written by import tool)
```
