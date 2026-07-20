// Package store is the SQLite-backed catalog of record for watchLive. It holds
// every channel (fetched from the iptv-org API, or added manually) together with
// the user state that used to live in the browser and on-disk JSON: which
// channels are favourited.
//
// The catalog is the source of truth; the m3u feed is only the transport format
// it is populated from. Channel IDs are the stable IDs minted by package
// playlist, so favourites re-attach to the right rows across re-syncs (the old
// positional IDs shifted every sync, which is why the browser keyed favourites
// by name).
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"watchlive/internal/playlist"

	_ "modernc.org/sqlite"
)

// Defaults for manually-added channels: they have no upstream metadata, so they
// land in their own country group and a neutral category.
const (
	manualGroup = "Custom"
	manualType  = "Entertainment"
)

// ErrNotFound / ErrNotManual let handlers map a failed delete to 404 vs 409.
var (
	ErrNotFound  = errors.New("channel not found")
	ErrNotManual = errors.New("channel is not a manual entry")
	// ErrInvalidSetting flags an UpdatePlaylistFields call with a blank name or
	// an out-of-range update_freq or stream_type value.
	ErrInvalidSetting = errors.New("invalid xtream setting")
)

// now is a seam so tests can drive staleness deterministically.
var now = time.Now

const schema = `
CREATE TABLE IF NOT EXISTS channels (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    logo            TEXT NOT NULL DEFAULT '',
    grp             TEXT NOT NULL DEFAULT '',
    typ             TEXT NOT NULL DEFAULT '',
    servers         TEXT NOT NULL DEFAULT '[]',
    clear_keys      TEXT NOT NULL DEFAULT '',
    http_user_agent TEXT NOT NULL DEFAULT '',
    http_referer    TEXT NOT NULL DEFAULT '',
    resolver        TEXT NOT NULL DEFAULT '',
    resolver_arg    TEXT NOT NULL DEFAULT '',
    is_favourite    INTEGER NOT NULL DEFAULT 0,
    is_manual       INTEGER NOT NULL DEFAULT 0,
    sort_name       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_channels_sort    ON channels(sort_name);

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS xtream_playlists (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    server            TEXT NOT NULL,
    username          TEXT NOT NULL,
    password          TEXT NOT NULL,
    created_at        INTEGER NOT NULL,
    update_freq       TEXT NOT NULL DEFAULT 'manual',
    stream_type       TEXT NOT NULL DEFAULT 'ts',
    last_refreshed_at INTEGER NOT NULL DEFAULT 0
);
`

// Channel is a catalog row as served to the UI. It mirrors playlist.Channel and
// adds the persisted user state.
type Channel struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Logo    string            `json:"logo"`
	Group   string            `json:"group"`
	Type    string            `json:"type"`
	Servers []playlist.Server `json:"servers"`
	// ClearKeys holds ClearKey DRM pairs {kidHex: keyHex} for CENC streams,
	// passed straight to Shaka's drm.clearKeys. Omitted when the stream is clear.
	ClearKeys map[string]string `json:"clear_keys,omitempty"`
	// UserAgent / Referer are #EXTVLCOPT header hints the proxy applies to the
	// upstream fetch; empty when the channel specified none. Not secrets.
	UserAgent string `json:"http_user_agent,omitempty"`
	Referer   string `json:"http_referer,omitempty"`
	// Resolver / ResolverArg make a channel "dynamic": instead of a fixed stream
	// URL, the app resolves a fresh signed URL at play time via the named provider
	// (e.g. "exposestrat") using ResolverArg (e.g. the stream slug). Empty for
	// ordinary channels. Servers still holds the last-resolved URL as a cache/hint.
	Resolver    string `json:"resolver,omitempty"`
	ResolverArg string `json:"resolver_arg,omitempty"`
	IsFavourite bool   `json:"is_favourite"`
	// CatOrder is the category's position in its source Xtream panel, used to
	// render Xtream groups in panel order. 0 for non-Xtream channels.
	CatOrder int `json:"cat_order"`
	// XtreamPlaylistID ties an Xtream-imported channel to its saved playlist so
	// the browse sidebar can scope the list to one playlist. Empty for manual
	// and .m3u-imported channels.
	XtreamPlaylistID string `json:"xtream_playlist_id"`
}

// Store wraps the SQLite database. A single connection serializes all access
// (modernc.org/sqlite is single-writer; with one local user there is no read
// concurrency to lose), which sidesteps SQLITE_BUSY entirely.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the catalog database at path and applies the
// schema. Pass ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: serializes writes and keeps an in-memory DB alive for the
	// process lifetime.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	// Columns added after the initial schema. SQLite has no "ADD COLUMN IF NOT
	// EXISTS", so a duplicate-column error on an already-migrated DB is benign.
	for _, col := range []string{"clear_keys", "http_user_agent", "http_referer", "resolver", "resolver_arg", "xtream_playlist_id"} {
		if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("store: migrate %s: %w", col, err)
		}
	}
	// cat_order is INTEGER (category position from the Xtream panel), so it
	// can't ride the string-column loop above. Same duplicate-column tolerance.
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN cat_order INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("store: migrate cat_order: %w", err)
	}
	// Per-playlist settings added after the initial xtream_playlists table, same
	// duplicate-column tolerance as above.
	for _, col := range []struct{ name, def string }{
		{"update_freq", `TEXT NOT NULL DEFAULT 'manual'`},
		{"stream_type", `TEXT NOT NULL DEFAULT 'ts'`},
		{"last_refreshed_at", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if _, err := db.Exec(`ALTER TABLE xtream_playlists ADD COLUMN ` + col.name + ` ` + col.def); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("store: migrate %s: %w", col.name, err)
		}
	}
	return &Store{db: db}, nil
}

// Close checkpoints the WAL and closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Count returns the number of channels in the catalog.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&n)
	return n, err
}

// IsEmpty reports whether the catalog has no channels yet (cold start).
func (s *Store) IsEmpty() (bool, error) {
	n, err := s.Count()
	return n == 0, err
}

// ListChannels returns the whole catalog in display order (A→Z), each row
// carrying its favourite state.
func (s *Store) ListChannels() ([]Channel, error) {
	rows, err := s.db.Query(`
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, resolver, resolver_arg, is_favourite, cat_order, xtream_playlist_id
		FROM channels ORDER BY sort_name, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanChannel(row scanner) (Channel, error) {
	var (
		ch        Channel
		serversJS string
		clearJS   string
		fav       int
	)
	if err := row.Scan(&ch.ID, &ch.Name, &ch.Logo, &ch.Group, &ch.Type, &serversJS, &clearJS, &ch.UserAgent, &ch.Referer, &ch.Resolver, &ch.ResolverArg, &fav, &ch.CatOrder, &ch.XtreamPlaylistID); err != nil {
		return Channel{}, err
	}
	if serversJS != "" {
		if err := json.Unmarshal([]byte(serversJS), &ch.Servers); err != nil {
			return Channel{}, fmt.Errorf("store: servers json for %s: %w", ch.ID, err)
		}
	}
	if clearJS != "" {
		if err := json.Unmarshal([]byte(clearJS), &ch.ClearKeys); err != nil {
			return Channel{}, fmt.Errorf("store: clear_keys json for %s: %w", ch.ID, err)
		}
	}
	ch.IsFavourite = fav != 0
	return ch, nil
}

// UpsertCatalog inserts new channels and updates the feed-sourced fields of
// existing ones, preserving user state (favourite, manual rows). It returns
// counts and the set of IDs present in this feed, which the caller can pass to
// PruneOrphans.
func (s *Store) UpsertCatalog(chs []playlist.Channel) (ins, upd int, seen map[string]bool, err error) {
	seen = make(map[string]bool, len(chs))
	existing, err := s.idSet()
	if err != nil {
		return 0, 0, nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// is_favourite is intentionally absent from the SET list so it survives a
	// re-sync; the WHERE keeps manual rows (which never appear in the feed
	// anyway) untouched even on an ID collision.
	stmt, err := tx.Prepare(`
		INSERT INTO channels (id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, sort_name, is_manual)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, logo=excluded.logo, grp=excluded.grp,
			typ=excluded.typ, servers=excluded.servers, clear_keys=excluded.clear_keys,
			http_user_agent=excluded.http_user_agent, http_referer=excluded.http_referer,
			sort_name=excluded.sort_name
		WHERE channels.is_manual = 0`)
	if err != nil {
		return 0, 0, nil, err
	}
	defer stmt.Close()

	for _, ch := range chs {
		serversJS, merr := json.Marshal(ch.Servers)
		if merr != nil || string(serversJS) == "null" {
			serversJS = []byte("[]")
		}
		if _, err = stmt.Exec(ch.ID, ch.Name, ch.Logo, ch.Group, ch.Type, string(serversJS), marshalKeys(ch.ClearKeys), ch.UserAgent, ch.Referer, strings.ToLower(ch.Name)); err != nil {
			return 0, 0, nil, err
		}
		if seen[ch.ID] {
			continue // duplicate within the feed: count once
		}
		seen[ch.ID] = true
		if existing[ch.ID] {
			upd++
		} else {
			ins++
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, nil, err
	}
	return ins, upd, seen, nil
}

func (s *Store) idSet() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}

// SetFavourite flips a channel's favourite flag. ok is false when no such
// channel exists.
func (s *Store) SetFavourite(id string, on bool) (ok bool, err error) {
	v := 0
	if on {
		v = 1
	}
	res, err := s.db.Exec(`UPDATE channels SET is_favourite=? WHERE id=?`, v, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AddManual inserts a user-provided channel. It is favourited by default (the
// user just gave us a URL they want), and idempotent on the same name+url so a
// re-add doesn't duplicate.
func (s *Store) AddManual(name, url string, clearKeys map[string]string, referer, userAgent string) (Channel, error) {
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	referer, userAgent = strings.TrimSpace(referer), strings.TrimSpace(userAgent)
	id := "manual:" + manualHash(name, url)
	serversJS, _ := json.Marshal([]playlist.Server{{URL: url}})

	_, err := s.db.Exec(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, clear_keys, http_referer, http_user_agent, is_favourite, is_manual, sort_name)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, 1, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, servers=excluded.servers, clear_keys=excluded.clear_keys,
			http_referer=excluded.http_referer, http_user_agent=excluded.http_user_agent, sort_name=excluded.sort_name`,
		id, name, manualGroup, manualType, string(serversJS), marshalKeys(clearKeys), referer, userAgent, strings.ToLower(name))
	if err != nil {
		return Channel{}, err
	}
	return s.getChannel(id)
}

// AddResolvable inserts a dynamic channel whose playable URL is not stored but
// resolved at play time by the named provider from arg (e.g. a stream slug).
// referer/userAgent are the headers the *resolved* stream needs when played.
// Like AddManual it is favourited by default and idempotent on
// name+provider+arg. Servers starts empty and is filled by SetResolvedURL after
// the first resolve.
func (s *Store) AddResolvable(name, provider, arg, referer, userAgent string) (Channel, error) {
	name, provider, arg = strings.TrimSpace(name), strings.TrimSpace(provider), strings.TrimSpace(arg)
	referer, userAgent = strings.TrimSpace(referer), strings.TrimSpace(userAgent)
	id := "manual:" + manualHash(name, provider+"|"+arg)
	_, err := s.db.Exec(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, clear_keys, http_referer, http_user_agent, resolver, resolver_arg, is_favourite, is_manual, sort_name)
		VALUES (?, ?, '', ?, ?, '[]', '', ?, ?, ?, ?, 1, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, http_referer=excluded.http_referer, http_user_agent=excluded.http_user_agent,
			resolver=excluded.resolver, resolver_arg=excluded.resolver_arg, sort_name=excluded.sort_name`,
		id, name, manualGroup, manualType, referer, userAgent, provider, arg, strings.ToLower(name))
	if err != nil {
		return Channel{}, err
	}
	return s.getChannel(id)
}

// SetResolvedURL caches a freshly-resolved stream URL into a channel's servers[0]
// so the proxy's per-host header map (keyed by upstream host) picks up a rotated
// CDN host. It touches only servers; the resolver recipe and user state are kept.
func (s *Store) SetResolvedURL(id, streamURL string) error {
	serversJS, _ := json.Marshal([]playlist.Server{{URL: streamURL}})
	res, err := s.db.Exec(`UPDATE channels SET servers=? WHERE id=?`, string(serversJS), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ImportEntry is one channel from an imported playlist after the user has
// reviewed/edited it.
type ImportEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// ClearKeys carries ClearKey pairs parsed from the playlist (or passed
	// through the review UI) for CENC streams. Nil when clear.
	ClearKeys map[string]string `json:"clear_keys,omitempty"`
}

// ChannelRef identifies an existing channel by a link, for the import
// duplicate report.
type ChannelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// URLIndex maps every stream URL currently in the catalog (across all channels'
// servers) to the channel that owns it. Used to dedupe an import by link: a
// link already present in the library is not added again. First owner wins on
// the (rare) chance two channels share a URL.
func (s *Store) URLIndex() (map[string]ChannelRef, error) {
	rows, err := s.db.Query(`SELECT id, name, servers FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := make(map[string]ChannelRef)
	for rows.Next() {
		var id, name, serversJS string
		if err := rows.Scan(&id, &name, &serversJS); err != nil {
			return nil, err
		}
		if serversJS == "" {
			continue
		}
		var servers []playlist.Server
		if err := json.Unmarshal([]byte(serversJS), &servers); err != nil {
			return nil, err
		}
		for _, sv := range servers {
			if sv.URL != "" {
				if _, ok := idx[sv.URL]; !ok {
					idx[sv.URL] = ChannelRef{ID: id, Name: name}
				}
			}
		}
	}
	return idx, rows.Err()
}

// ImportManual bulk-inserts reviewed playlist entries as manual channels in one
// transaction. Like AddManual they survive re-syncs, but they are NOT
// auto-favourited (importing a list shouldn't flood Favourites — the single "+"
// add does favourite, since that's a deliberate one-off).
//
// Dedupe is by LINK (exact URL), not name: an entry whose URL already exists
// anywhere in the catalog is skipped, as is a repeated URL within the batch.
// Entries with an empty name or a non-http(s) URL are skipped too. This makes
// save authoritative regardless of what the client sends. Returns the count
// actually inserted.
func (s *Store) ImportManual(entries []ImportEntry) (added int, err error) {
	idx, err := s.URLIndex()
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, clear_keys, is_favourite, is_manual, sort_name)
		VALUES (?, ?, '', ?, ?, ?, ?, 0, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, servers=excluded.servers, clear_keys=excluded.clear_keys, sort_name=excluded.sort_name`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	seenURL := make(map[string]bool, len(entries))
	for _, e := range entries {
		name, url := strings.TrimSpace(e.Name), strings.TrimSpace(e.URL)
		if name == "" || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
			continue
		}
		if _, exists := idx[url]; exists {
			continue // link already in the library
		}
		if seenURL[url] {
			continue // duplicate link within this batch
		}
		seenURL[url] = true
		id := "manual:" + manualHash(name, url)
		serversJS, _ := json.Marshal([]playlist.Server{{URL: url}})
		if _, err = stmt.Exec(id, name, manualGroup, manualType, string(serversJS), marshalKeys(e.ClearKeys), strings.ToLower(name)); err != nil {
			return 0, err
		}
		added++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// UpdateManual replaces a manual channel's name and stream link in place, keyed
// by ID so the favourite state tied to that ID survives the edit (the ID
// is opaque after creation, so it intentionally does NOT track the new
// name/url hash). It refuses feed channels (their fields are overwritten on the
// next sync anyway), mirroring DeleteManual's errors: ErrNotFound when the id is
// unknown, ErrNotManual when it exists but isn't a manual entry.
func (s *Store) UpdateManual(id, name, url, referer, userAgent string) (Channel, error) {
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	referer, userAgent = strings.TrimSpace(referer), strings.TrimSpace(userAgent)
	var isManual int
	err := s.db.QueryRow(`SELECT is_manual FROM channels WHERE id=?`, id).Scan(&isManual)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, err
	}
	if isManual == 0 {
		return Channel{}, ErrNotManual
	}
	serversJS, _ := json.Marshal([]playlist.Server{{URL: url}})
	if _, err := s.db.Exec(
		`UPDATE channels SET name=?, sort_name=?, servers=?, http_referer=?, http_user_agent=? WHERE id=? AND is_manual=1`,
		name, strings.ToLower(name), string(serversJS), referer, userAgent, id); err != nil {
		return Channel{}, err
	}
	return s.getChannel(id)
}

// DeleteManual removes a manually-added channel. It refuses feed channels:
// ErrNotFound when the id is unknown, ErrNotManual when it exists but isn't a
// manual entry.
func (s *Store) DeleteManual(id string) error {
	var isManual int
	err := s.db.QueryRow(`SELECT is_manual FROM channels WHERE id=?`, id).Scan(&isManual)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isManual == 0 {
		return ErrNotManual
	}
	_, err = s.db.Exec(`DELETE FROM channels WHERE id=? AND is_manual=1`, id)
	return err
}

// Get returns a single channel by id, or ErrNotFound if absent.
func (s *Store) Get(id string) (Channel, error) {
	ch, err := s.getChannel(id)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return ch, err
}

func (s *Store) getChannel(id string) (Channel, error) {
	row := s.db.QueryRow(`
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent, http_referer, resolver, resolver_arg, is_favourite, cat_order, xtream_playlist_id
		FROM channels WHERE id=?`, id)
	return scanChannel(row)
}

// PruneOrphans deletes channels that are no longer in the latest feed (not in
// seen) and that the user has no stake in (not favourited, not manual). It is
// optional: removed-upstream channels are kept by default so a favourite can't
// silently vanish.
func (s *Store) PruneOrphans(seen map[string]bool) (int, error) {
	rows, err := s.db.Query(`SELECT id FROM channels WHERE is_favourite=0 AND is_manual=0`)
	if err != nil {
		return 0, err
	}
	var orphans []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[id] {
			orphans = append(orphans, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(orphans) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`DELETE FROM channels WHERE id=?`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	for _, id := range orphans {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(orphans), nil
}

// CountUnkept returns how many channels PruneUnkept would delete (not
// favourited, not manual), for a dry-run preview.
func (s *Store) CountUnkept() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM channels WHERE is_favourite=0 AND is_manual=0`).Scan(&n)
	return n, err
}

// PruneUnkept deletes every channel the user has no stake in — not favourited
// and not manual (is_favourite=0 AND is_manual=0) — regardless of any feed. It
// is the unconditional form of PruneOrphans (which restricts to channels absent
// from the latest feed), intended for a one-off catalog cleanup. Favourites,
// manual channels, imported .m3u entries and Xtream-imported channels (all
// is_manual=1) are kept. Returns the number of rows deleted.
func (s *Store) PruneUnkept() (int, error) {
	res, err := s.db.Exec(`DELETE FROM channels WHERE is_favourite=0 AND is_manual=0`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetMeta returns a meta value, or "" when the key is absent.
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta upserts a meta value.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// XtreamPlaylist is a saved Xtream Codes panel the user imports live channels
// from. Password is stored plaintext, consistent with the app's local-only
// single-user model (channel URLs and ClearKeys are plaintext too), and is
// omitted from any response served to the UI.
type XtreamPlaylist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Server    string `json:"server"`
	Username  string `json:"username"`
	Password  string `json:"-"`
	CreatedAt int64  `json:"created_at"`
	// UpdateFreq is the auto-refresh cadence: "manual" | "daily" | "3days" |
	// "weekly". StreamType is the extension used to build stream URLs: "ts" |
	// "m3u8". LastRefreshedAt (unix seconds) drives the startup interval sweep.
	UpdateFreq      string `json:"update_freq"`
	StreamType      string `json:"stream_type"`
	LastRefreshedAt int64  `json:"-"`
	// Imported reports whether any channel row already references this playlist;
	// populated by ListXtreamPlaylists, false elsewhere.
	Imported bool `json:"imported"`
}

// XtreamStream is one live channel to import, as distilled from the panel's
// get_live_streams response by the caller (package xtream). StreamID +
// PlaylistID form the stable catalog ID, so a refresh upserts instead of
// duplicating.
type XtreamStream struct {
	StreamID int
	Name     string
	Logo     string
	// URL is the fully-built playable stream URL (server/live/user/pass/id.ext).
	URL string
	// Group is the channel's category name (from the panel), stored as the
	// channel's typ so the browse UI groups by it. Empty → "Uncategorized".
	Group string
	// CatOrder is the category's index in the panel's category list, used to
	// order groups in the browse UI.
	CatOrder int
}

// SaveXtreamPlaylist inserts a new saved playlist with a freshly-minted opaque
// ID and returns it. Server is stored as-given (the caller normalizes it).
func (s *Store) SaveXtreamPlaylist(name, server, username, password string) (XtreamPlaylist, error) {
	p := XtreamPlaylist{
		ID:        "xt_" + randHex(8),
		Name:      strings.TrimSpace(name),
		Server:    strings.TrimSpace(server),
		Username:  strings.TrimSpace(username),
		Password:  password,
		CreatedAt: now().Unix(),
	}
	_, err := s.db.Exec(`
		INSERT INTO xtream_playlists (id, name, server, username, password, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Server, p.Username, p.Password, p.CreatedAt)
	if err != nil {
		return XtreamPlaylist{}, err
	}
	// Matches the SQL column defaults so the returned value is accurate without
	// a round-trip read.
	p.UpdateFreq = "manual"
	p.StreamType = "ts"
	return p, nil
}

// ListXtreamPlaylists returns saved playlists (newest first) with Imported set
// per playlist by a single scan of channel.xtream_playlist_id. Passwords are
// loaded (callers need them to refresh) but are omitted from JSON via the tag.
func (s *Store) ListXtreamPlaylists() ([]XtreamPlaylist, error) {
	imported, err := s.importedPlaylistIDs()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT id, name, server, username, password, created_at, update_freq, stream_type, last_refreshed_at
		FROM xtream_playlists ORDER BY created_at DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []XtreamPlaylist
	for rows.Next() {
		var p XtreamPlaylist
		if err := rows.Scan(&p.ID, &p.Name, &p.Server, &p.Username, &p.Password, &p.CreatedAt, &p.UpdateFreq, &p.StreamType, &p.LastRefreshedAt); err != nil {
			return nil, err
		}
		p.Imported = imported[p.ID]
		out = append(out, p)
	}
	return out, rows.Err()
}

// importedPlaylistIDs is the set of playlist ids that have at least one channel
// row, used to compute XtreamPlaylist.Imported without an N+1 query.
func (s *Store) importedPlaylistIDs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT xtream_playlist_id FROM channels WHERE xtream_playlist_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}

// GetXtreamPlaylist returns a saved playlist by id (with its password, for
// refresh), or ErrNotFound if absent.
func (s *Store) GetXtreamPlaylist(id string) (XtreamPlaylist, error) {
	var p XtreamPlaylist
	err := s.db.QueryRow(`
		SELECT id, name, server, username, password, created_at, update_freq, stream_type, last_refreshed_at
		FROM xtream_playlists WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Server, &p.Username, &p.Password, &p.CreatedAt, &p.UpdateFreq, &p.StreamType, &p.LastRefreshedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return XtreamPlaylist{}, ErrNotFound
	}
	return p, err
}

// validUpdateFreq / validStreamType are the accepted setting values.
var validUpdateFreq = map[string]bool{"manual": true, "daily": true, "3days": true, "weekly": true}
var validStreamType = map[string]bool{"ts": true, "m3u8": true}

// UpdatePlaylistFields partially updates a saved playlist: name, update_freq
// and stream_type are each optional (nil leaves that column untouched),
// letting the Playlist management tab change one field at a time without
// resending the others. A nil name is "don't touch"; a non-nil name must be
// non-blank after trimming. updateFreq/streamType, if provided, are validated
// against the existing enums. Passing all three as nil is a no-op read of the
// current row. ErrInvalidSetting on a bad or blank value; ErrNotFound if no
// such playlist.
func (s *Store) UpdatePlaylistFields(id string, name, updateFreq, streamType *string) (XtreamPlaylist, error) {
	var sets []string
	var args []any

	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "name=?")
		args = append(args, trimmed)
	}
	if updateFreq != nil {
		if !validUpdateFreq[*updateFreq] {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "update_freq=?")
		args = append(args, *updateFreq)
	}
	if streamType != nil {
		if !validStreamType[*streamType] {
			return XtreamPlaylist{}, ErrInvalidSetting
		}
		sets = append(sets, "stream_type=?")
		args = append(args, *streamType)
	}

	if len(sets) == 0 {
		return s.GetXtreamPlaylist(id)
	}

	args = append(args, id)
	res, err := s.db.Exec(`UPDATE xtream_playlists SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	if err != nil {
		return XtreamPlaylist{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return XtreamPlaylist{}, ErrNotFound
	}
	return s.GetXtreamPlaylist(id)
}

// DeleteXtreamPlaylist removes one saved Xtream playlist together with every
// channel it imported — favourited or not, since the playlist is going away
// and its channels can no longer be refreshed. Both deletes run in one
// transaction. ErrNotFound when the id matches no saved playlist. Returns the
// channel-delete count (the playlist itself is always exactly one row).
func (s *Store) DeleteXtreamPlaylist(id string) (channelsDeleted int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	res, err := tx.Exec(`DELETE FROM channels WHERE xtream_playlist_id = ?`, id)
	if err != nil {
		return 0, err
	}
	cn, _ := res.RowsAffected()

	res, err = tx.Exec(`DELETE FROM xtream_playlists WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		err = ErrNotFound
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(cn), nil
}

// StampXtreamRefreshed records that a playlist was just refreshed, so the
// startup interval sweep can tell when it is next due.
func (s *Store) StampXtreamRefreshed(id string) error {
	_, err := s.db.Exec(`UPDATE xtream_playlists SET last_refreshed_at=? WHERE id=?`, now().Unix(), id)
	return err
}

// DueXtreamPlaylists returns saved playlists whose auto-refresh cadence has
// elapsed as of now, for the startup sweep. Each returned playlist carries its
// credentials so the caller can refresh it.
func (s *Store) DueXtreamPlaylists() ([]XtreamPlaylist, error) {
	all, err := s.ListXtreamPlaylists()
	if err != nil {
		return nil, err
	}
	dueIDs := playlistsDueForRefresh(all, now().Unix())
	dueSet := make(map[string]bool, len(dueIDs))
	for _, id := range dueIDs {
		dueSet[id] = true
	}
	var due []XtreamPlaylist
	for _, p := range all {
		if dueSet[p.ID] {
			due = append(due, p)
		}
	}
	return due, nil
}

// UpsertXtreamChannels imports (or refreshes) a playlist's live channels in one
// transaction. Each channel's ID is "xtream:<playlistID>:<streamID>", stable
// across refreshes so re-importing updates name/logo/servers in place instead of
// duplicating. Imported rows are is_manual=1 (survive pruning/re-sync, like an
// .m3u import) and is_favourite=0 (bulk imports must not flood Favourites).
//
// Returns how many rows were newly inserted vs updated. A row with an empty name
// or a non-http(s) URL is skipped.
func (s *Store) UpsertXtreamChannels(playlistID string, streams []XtreamStream) (added, updated int, err error) {
	existing, err := s.idSet()
	if err != nil {
		return 0, 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	// is_favourite is absent from the SET list so a refresh preserves the user's
	// favourite.
	stmt, err := tx.Prepare(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, is_favourite, is_manual, sort_name, xtream_playlist_id, cat_order)
		VALUES (?, ?, ?, ?, ?, ?, 0, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, logo=excluded.logo, grp=excluded.grp, typ=excluded.typ,
			servers=excluded.servers, sort_name=excluded.sort_name,
			xtream_playlist_id=excluded.xtream_playlist_id, cat_order=excluded.cat_order`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	seen := make(map[string]bool, len(streams))
	for _, st := range streams {
		name, u := strings.TrimSpace(st.Name), strings.TrimSpace(st.URL)
		if name == "" || !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			continue
		}
		id := fmt.Sprintf("xtream:%s:%d", playlistID, st.StreamID)
		if seen[id] {
			continue // duplicate stream_id within this batch
		}
		seen[id] = true
		serversJS, _ := json.Marshal([]playlist.Server{{URL: u}})
		grp := strings.TrimSpace(st.Group)
		if grp == "" {
			grp = "Uncategorized"
		}
		// grp column (country) stays neutral — categories are not countries and
		// must not pollute the country dropdown. The category name is the typ.
		if _, err = stmt.Exec(id, name, st.Logo, manualGroup, grp, string(serversJS), strings.ToLower(name), playlistID, st.CatOrder); err != nil {
			return 0, 0, err
		}
		if existing[id] {
			updated++
		} else {
			added++
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, updated, nil
}

// randHex returns n random bytes as a lowercase hex string, for opaque IDs.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is catastrophic and near-impossible; fall back to a
		// time-derived value so we never mint an empty (colliding) ID.
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// marshalKeys serializes a ClearKey map to JSON for the clear_keys column,
// returning "" (the column default) when there are no keys.
func marshalKeys(keys map[string]string) string {
	if len(keys) == 0 {
		return ""
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return ""
	}
	return string(b)
}

func manualHash(name, url string) string {
	sum := sha256.Sum256([]byte(name + "||" + url))
	return hex.EncodeToString(sum[:6]) // 12 hex chars
}

// refreshInterval maps an update_freq to its cadence in seconds. "manual" (and
// any unknown value) returns 0, meaning "never auto-refresh".
func refreshInterval(freq string) int64 {
	switch freq {
	case "daily":
		return 24 * 3600
	case "3days":
		return 3 * 24 * 3600
	case "weekly":
		return 7 * 24 * 3600
	default:
		return 0
	}
}

// playlistsDueForRefresh returns the ids of playlists whose auto-refresh cadence
// has elapsed as of nowUnix. "manual" playlists are never due; a playlist that
// has never been refreshed (LastRefreshedAt == 0) with a non-manual cadence is
// always due.
func playlistsDueForRefresh(playlists []XtreamPlaylist, nowUnix int64) []string {
	var due []string
	for _, p := range playlists {
		interval := refreshInterval(p.UpdateFreq)
		if interval == 0 {
			continue
		}
		if nowUnix >= p.LastRefreshedAt+interval {
			due = append(due, p.ID)
		}
	}
	return due
}
