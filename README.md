# watchlive (Go)

Single-binary rewrite of the Next.js LiveTV app: web UI, HLS stream proxy and
live viewer counter in one ~9 MB executable with zero runtime dependencies.

## Run

```
go build -ldflags "-s -w" -o watchlive.exe .
./watchlive.exe              # http://localhost:3000
```

Flags:

| Flag        | Default | Meaning                                              |
|-------------|---------|------------------------------------------------------|
| `-addr`     | `:3000` | listen address                                       |
| `-playlist` | *(auto)*| external M3U playlist path; default is `list.txt` next to the binary, falling back to the embedded copy |
| `-cache-mb` | `200`   | in-memory segment cache size (MB)                    |

## Adding channels

Edit `list.txt` next to the binary (standard M3U with `tvg-logo` /
`group-title` attributes). Changes are picked up automatically within 10
seconds, or immediately via `POST /api/reload`. Reload the browser page to see
the new list. No rebuild or restart needed.

## Endpoints

- `GET /` — web UI (channel list injected server-side)
- `GET /api/proxy?url=<m3u8|segment>` — HLS proxy: spoofs browser headers,
  rewrites playlist URLs back through the proxy, serves segments from an LRU
  RAM cache with single-flight upstream fetches (`X-Cache: HIT|MISS`)
- `GET|POST /api/viewers` — heartbeat + live counts
  (`{total, channelCount, top}`); sessions expire 60 s after last heartbeat
- `GET /api/channels` — parsed playlist as JSON (gzip-compressed when the
  client accepts it; ETag revalidation returns 304 while the list is unchanged)
- `POST /api/reload` — force playlist re-read

## Tests

```
go test ./...
```

## Layout

```
main.go                    server wiring, embeds, hot reload, graceful shutdown
internal/playlist/         M3U parser
internal/proxy/            HLS proxy + LRU segment cache + singleflight
internal/viewers/          session-derived viewer counts (cannot drift)
web/templates/index.html   page template
web/static/                app.js, style.css, vendored hls.min.js (1.6.16)
list.txt                   channel playlist (embedded fallback)
```
