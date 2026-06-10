# LiveTV

A browser-based live TV streaming app built with Next.js. Loads an M3U playlist, proxies HLS streams, and tracks live viewers per channel in real time.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Framework | Next.js 16.2.9 (App Router) |
| UI | React 19, Tailwind CSS 4 |
| Streaming | HLS.js 1.6.16 |
| Language | TypeScript 5 |
| Storage | Upstash Redis (serverless) |

---

## Project Structure

```
app/
  layout.tsx              Root layout (metadata, fonts)
  page.tsx                Home page — channel list, search, header, session logic
  globals.css             Tailwind + custom animations
  api/
    viewers/route.ts      Session heartbeat & viewer count API (Node.js runtime)
    proxy/route.ts        HLS stream/playlist proxy (Edge Runtime)

components/
  VideoPlayer.tsx         Video player, carousel, controls

lib/
  parseM3U.ts             M3U playlist parser and Channel data model
  redis.ts                Singleton Upstash Redis client

public/
  list.txt                M3U playlist (channel source)

scripts/
  check-streams.mjs       Checks which stream URLs are still alive
  filter-bangladeshi.mjs  Filters playlist to Bangladeshi channels only
```

---

## How Channels Are Loaded

### 1. Playlist Source

All channels come from `public/list.txt`, a static M3U8 file bundled with the app. Each entry looks like:

```
#EXTINF:-1 tvg-logo="https://..." group-title="Sports"
T Sports 1
https://cdn.example.com/tsports1/stream.m3u8
```

### 2. Parsing (`lib/parseM3U.ts`)

`parseM3U(text)` reads the file line by line and extracts:

| Field | Source |
|---|---|
| `id` | Sequential index as string (`"0"`, `"1"`, …) |
| `name` | Channel name line after `#EXTINF` |
| `logo` | `tvg-logo="..."` attribute |
| `group` | `group-title="..."` attribute |
| `url` | First `http(s)://` URL after the `#EXTINF` block |
| `type` | Derived from `group` (see below) |

**Type categorization:**

```
"News"          ← group contains "news"
"Movies"        ← contains "movie", "cinema", or starts with "goldmine"
"Music"         ← contains "music", "talkies", "beats", "sangeet"
"Sports"        ← exactly "sports", "live sports", or contains "football"/"cricket"
"Kids"          ← contains "kids", "cartoon", "duronto"
"Religious"     ← contains "religion", "islamic", "peace"
"Entertainment" ← everything else (default)
```

Duplicate entries (same group + name + url) are removed. Channels with the same name are renamed `Channel - 1`, `Channel - 2`, etc.

### 3. Home Page Fetch (`app/page.tsx`)

On mount, the home page:

1. Fetches `/list.txt`
2. Parses it with `parseM3U()`
3. Stores the result in React state (`channels`)
4. Renders the sidebar channel list

Search is a live client-side filter over `channel.name` and `channel.group` (case-insensitive). No server round-trip needed.

---

## Viewer Tracking & Counts

### Session Identity

On first load, `page.tsx` generates a session ID:

```ts
Math.random().toString(36).slice(2) + Date.now().toString(36)
```

This is stored in `sessionStorage` — it survives page refreshes but clears when the browser tab is closed.

### Heartbeat

Every **30 seconds**, and immediately when the user switches channels, the client POSTs to `/api/viewers`:

```json
{
  "sessionId": "abc123xyz789",
  "channelId": "5"
}
```

`channelId` is `null` when the user is on the home screen with no channel selected.

### Server-Side Tracking (`app/api/viewers/route.ts`)

Viewer state is stored in **Upstash Redis** — a serverless Redis instance accessible over HTTPS. This means counts are shared across all Vercel function instances and survive server restarts.

**Redis key schema:**

| Key | Type | TTL | Purpose |
|---|---|---|---|
| `session:{sid}` | Hash (`channelId`, `ts`) | 60 s | Per-session data |
| `session:channel:{sid}` | String | 60 s | Previous-channel lookup for DECR on switch |
| `viewers:total` | String (int) | none | Global active session counter (INCR/DECR) |
| `channel:viewers:{channelId}` | String (int) | 90 s | Active viewers on one channel |
| `tunein:counts` | Sorted set | none | Cumulative tune-ins per channel (ZINCRBY) |

On each POST:

1. Read `session:channel:{sid}` to get the previous channel
2. Build a pipeline:
   - Upsert session hash + refresh TTL
   - If new session → `INCR viewers:total`
   - If channel changed → `DECR channel:viewers:{prev}`, `ZINCRBY tunein:counts`, `INCR channel:viewers:{new}`
   - If same channel → refresh TTL on `channel:viewers:{channelId}`
3. Read pipeline: `viewers:total`, `channel:viewers:{channelId}`, top-5 from `tunein:counts`

Response (same shape as before):

```json
{
  "total": 42,          // total active sessions across all channels
  "channelCount": 5,    // active sessions on the requested channel (null if no channel)
  "top": [
    { "id": "3", "count": 15 },
    { "id": "1", "count": 12 },
    { "id": "7", "count": 8 }
  ]
}
```

### Count Definitions

| Field | Meaning |
|---|---|
| `total` | Sessions active in the last 60 seconds (all channels) |
| `channelCount` | Sessions currently on a specific channel |
| `top[].count` | Cumulative tune-ins for that channel (persists across restarts) |

### Where Counts Are Displayed

- **Header** — `N active` (total viewers, shown only on the home screen)
- **Now Playing bar** — `N watching` (viewers on the current channel)
- **Carousel** — channels are ordered by `top[].count` to surface popular channels

---

## HLS Stream Proxy (`app/api/proxy`)

Stream URLs are routed through a proxy to solve CORS restrictions on third-party CDNs. The proxy runs on **Vercel Edge Runtime** — no cold starts, executes at the nearest PoP to the user.

**Request:**
```
GET /api/proxy?url=https://cdn.example.com/channel1/stream.m3u8
```

The proxy:

1. Forwards the request with browser-like headers (User-Agent, Referer, Origin) to bypass hotlink protection
2. Detects whether the response is an M3U8 playlist or a binary segment
3. **Playlist** — rewrites all relative segment and key URLs to absolute proxied URLs, so subsequent requests also go through the proxy
4. **Segments** — streams binary data with a 10-second edge cache (`Cache-Control: public, max-age=10`). HLS segments are write-once, so caching them reduces duplicate upstream fetches when multiple viewers watch the same channel

The video player points HLS.js at the proxy URL — the app never fetches stream data directly from the CDN.

---

## Video Player (`components/VideoPlayer.tsx`)

### Channel Selected

When a channel is chosen, `VideoPlayer` loads the stream via HLS.js (with native fallback for Safari). It shows:

- The video element with native browser controls
- A **Now Playing** bar at the bottom: logo, channel name, group, viewer count, LIVE badge
- `F` key shortcut to toggle fullscreen

### No Channel Selected — Carousel

When no channel is selected, a carousel of popular channels is shown instead of the video:

- Populated from `topChannelIds` returned by the viewer API
- Always shows at least 5 slots; padded with zero-viewer channels if needed
- Auto-rotates every 4 seconds
- Manual navigation via prev/next buttons and dot indicators
- Clicking a slide selects that channel

---

## UI Layout

```
┌─────────────────────────────────────────────────┐
│ Header: [☰] LiveTV               [N active]     │
├──────────────┬──────────────────────────────────┤
│              │                                  │
│  Sidebar     │   Main Area                      │
│  (channel    │   • No channel: Carousel         │
│   list +     │   • Channel selected: HLS player │
│   search)    │     + Now Playing bar            │
│              │                                  │
└──────────────┴──────────────────────────────────┘
```

On mobile, the sidebar hides when a channel is selected. A "More Channels" button reopens it.

**Keyboard shortcuts:**

| Key | Action |
|---|---|
| `/` | Focus the search input |
| `Escape` | Clear search and blur |
| `F` | Toggle fullscreen (while playing) |

---

## Running the App

### Environment variables

Create a `.env.local` file in the project root (see `.env.local.example`):

```
UPSTASH_REDIS_REST_URL=https://<your-db>.upstash.io
UPSTASH_REDIS_REST_TOKEN=<your-token>
```

Get these from [upstash.com](https://upstash.com) — free tier is sufficient. Add the same variables to your Vercel project settings before deploying.

### Commands

```bash
npm install
npm run dev        # dev server on localhost:3000
npm run build      # production build
npm start          # run production build
```

**Utility scripts:**

```bash
node scripts/check-streams.mjs
# Checks each channel URL in list.txt and removes dead streams (concurrent, 6s timeout)

node scripts/filter-bangladeshi.mjs
# Removes non-Bangladeshi channels from list.txt
```

---

## Data Flow Summary

```
Browser                         Server (Vercel)
──────────────────────────────────────────────────────────────────────────
  Load page
    → fetch /list.txt           ← static file
    → parseM3U()
    → render channel list

  Generate sessionId (sessionStorage)

  POST /api/viewers             → read prev channel from Redis
    ← { total, channelCount,      pipeline: upsert session, INCR/DECR counters
        top }                      read pipeline: total, channelCount, top-5
  (repeat every 30s or on         ↕ Upstash Redis (shared across instances)
   channel switch)

  Select channel
    → HLS.js loads
       /api/proxy?url=...       → Edge Runtime (nearest PoP)
         ← playlist               fetch from CDN, rewrite URLs
         ← segments               stream binary, cache 10s at edge
```
