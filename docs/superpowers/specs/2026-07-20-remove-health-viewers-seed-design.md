# Remove health probing, viewer counting, and the embedded seed

**Date:** 2026-07-20
**Status:** Approved

## Goal

The app is now used solely on one local machine. The seed playlist,
server-side stream-health probing, and live viewer counting are no longer
wanted. `seed.m3u`, `internal/health/`, and `internal/viewers/` were deleted
from disk, which broke the build (`main.go` still embeds the seed and imports
both packages). This spec removes all three features cleanly across the Go
backend, the frontend, and the tests.

## Non-goals

- No destructive DB migration. The existing `store/catalog.db` keeps its
  `is_working` / `last_checked_at` columns physically; we simply stop naming
  them in queries. A fresh DB is created without them.
- No change to Xtream, recording, resolver, proxy, or favourites behaviour.

## Changes

### 1. `main.go`

- Drop imports `watchlive/internal/health` and `watchlive/internal/viewers`.
- Delete the `//go:embed .\seed.m3u` directive and the `seedPlaylist` var.
- Delete the non-playlist cold-start seed call. Playlist mode (`-playlist`)
  still seeds from the user-supplied file via `seedIfEmpty`, which is retained.
- Delete the seed header-backfill block (depends on `seedPlaylist` +
  `store.BackfillHeaders`).
- Remove `viewerStore`, the `/api/viewers` route, and the viewer-prune
  goroutine.
- Remove `prober`, `prober.OnFinish`, the seed-from-verdicts block,
  `startStaleProbe`, `healthTTL`, the `go startStaleProbe()` call, and the
  `POST`/`GET /api/health` routes.
- Update `newMux` to drop the `viewerStore` and `prober` parameters.
- Retain the `-no-refresh` / `-source-url` / `-prune` flags and `refresh()` /
  `fetchAndEnrich()` (unrelated to the removed features; sync is already off).

### 2. `internal/store/store.go`

- Drop the `watchlive/internal/health` import.
- Delete methods `SetHealth`, `StaleTargets`, `HealthVerdicts`, and
  `BackfillHeaders` (its only callers — the main seed block and `cmd/backfill`
  — are both removed).
- Remove `is_working` and `last_checked_at` from the `schema` string and drop
  the `idx_channels_checked` index.
- Remove those two columns from every SELECT / INSERT column list and row scan.
- Remove the `IsWorking *bool` field from the `Channel` struct.
- Remove the `is_working=NULL, last_checked_at=NULL` reset statements in the
  manual-update paths.

### 3. Deleted files

- `cmd/backfill/` (entire directory — exists only to process `seed.m3u`).
- `web/static/health.js`.

### 4. Frontend

- `init.js`: remove the `health.js` import, `state.health` / `is_working`
  seeding, the health-toggle wiring, and `observeHealth`.
- `state.js`, `channels.js`, `player.js`, `picker.js`, `style.css`: remove
  health-toggle state, working-only filtering, and viewer-badge UI. Each file
  is audited precisely during implementation.
- `web/templates/index.html`: remove the "Working only" toggle button and the
  viewer-count badge element.

This deletes two user-facing features: the **Working only** filter and the
**live viewer badge**. Accepted.

### 5. Tests

- `main_test.go`: update the `newMux(...)` call to the new signature.
- `internal/store/store_test.go`: remove tests for the deleted health methods.

## Verification

- `go build ./...` and `go vet ./...` succeed.
- `go test ./...` passes.
- `go run .` starts, serves the UI, and lists channels from the existing
  catalog with no health/viewer references in the console or the page.
