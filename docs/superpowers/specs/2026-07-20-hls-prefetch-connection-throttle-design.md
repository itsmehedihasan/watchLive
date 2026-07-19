# HLS Segment Prefetch — Connection Throttle

**Date:** 2026-07-20
**Status:** Approved (design)

## Problem

Xtream live channels imported as `.m3u8` (HLS) play smoothly — the format
buffers ahead and refills before draining, unlike raw `.ts` (which froze every
~50s when the upstream closed its single continuous connection).

But switching this provider's playlist to HLS caused the panel to reject stream
requests with `402`/`403`. Root cause, confirmed against the panel's
`player_api.php`:

```
active_cons:            255
max_connections:        250      ← account is OVER its cap
status:                 Active
allowed_output_formats: ["m3u8", "ts", "rtmp"]
```

A raw `.ts` stream uses exactly **one** upstream connection per channel. HLS uses
more: the playlist fetch, the actively-downloading segment, **and** the proxy's
segment prefetch — which currently warms *every* upcoming segment in the
playlist concurrently (`prefetchConcurrency = 12` in
[internal/proxy/proxy.go](../../../internal/proxy/proxy.go)). One HLS channel can
therefore hold up to a dozen simultaneous upstream connections, which tips a
connection-capped account over its limit and triggers the 402/403 rejections.

`.ts` still returns `200` at the same instant an HLS playlist returns `403`,
because `.ts` is one connection and HLS (with prefetch) is many — direct
evidence the ceiling is *connection count*, not the account or the format.

## Goal

Play a single HLS channel reliably under the provider's connection cap, keeping
the seamless HLS experience (no ~50s freeze, no black flash, no replayed
footage) — while holding only ~1–2 concurrent upstream connections per channel.

Non-goal (for now): optimizing the multi-cell video wall's total connection
budget. Noted as future work below.

## Approach

Throttle segment prefetch to the **next segment only**, instead of warming the
entire upcoming segment list.

HLS segments are brief fetches: a ~10s segment downloads in ~1s and its
connection closes. So with prefetch limited to a single upcoming segment, a
channel holds at any moment roughly:

- 1 playlist connection (refreshed every ~2s via the playlist micro-cache; brief), plus
- 1 active segment connection (the one the player is downloading; brief), plus
- 1 prefetch connection (warming the single next segment; brief)

≈ **2–3 connections**, transient and mostly non-overlapping — comparable to
`.ts`'s single connection, but seamless.

### Change

In [internal/proxy/proxy.go](../../../internal/proxy/proxy.go), `schedulePrefetch(urls []string)`
currently loops over the full ordered segment list, launching a warm-up per URL
bounded only by the shared `sem` (cap 12). Change it to warm only the first
not-yet-cached segment (`urls[0]` after skipping already-warm ones), leaving the
rest to the player's own on-demand requests.

The existing machinery is reused unchanged:

- `segments.get(target)` skip-if-warm check stays — we still walk past
  already-cached segments to find the genuine next one to warm.
- `beginPrefetch` / `endPrefetch` in-flight dedup stays.
- The `sem` semaphore stays (now rarely contended, since we enqueue one item).

Because HLS playlists slide forward one segment per refresh and the proxy
re-runs `schedulePrefetch` on each playlist fetch, warming just the next segment
each cycle keeps the buffer one step ahead without a connection storm.

## Alternatives considered

- **Disable prefetch entirely (flag, default off).** Simplest; ~1–2 connections
  per channel. Rejected in favor of next-segment throttle, which keeps a small
  latency-hiding benefit at nearly the same connection cost.
- **Global upstream-connection semaphore.** Cap total concurrent upstream
  connections app-wide. Most robust for the multi-cell wall, but the most code
  and it can stall cells under saturation. Overkill for the current "one channel
  reliably" goal.

## Error handling

Unchanged. Prefetch is already best-effort: warm-up failures are swallowed
(the player's own segment request re-fetches on a miss). Throttling to one
segment does not change any failure path — a missed warm-up simply means the
player pays one proxy→upstream round trip, absorbed by the existing 30s forward
buffer (`maxBufferLength` in [web/static/player.js](../../../web/static/player.js)).

## Testing

- **Unit:** extend the proxy tests so that, given a media playlist with several
  segments, `schedulePrefetch` (via `fetchUpstream` on an HLS body) warms at most
  one segment rather than all of them. Assert against the segment cache / warm
  set.
- **Behavioral (manual):** play the previously-failing channel and confirm it
  streams past 50s smoothly with no 402/403 and no freeze/flash/replay.

## Future work (out of scope)

Multi-cell wall: the prefetch semaphore (`prefetchConcurrency`) and total
upstream connections are shared across cells. Playing many HLS channels at once
still multiplies connection usage. If the wall needs to run under a tight cap, a
global upstream-connection semaphore (Alternative above) is the follow-up.
