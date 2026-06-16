// Package store is the SQLite-backed catalog of record for watchLive. It holds
// every channel (fetched from the iptv-org API, or added manually) together with
// the user state that used to live in the browser and on-disk JSON: which
// channels are favourited, and which the health prober found reachable.
//
// The catalog is the source of truth; the m3u feed is only the transport format
// it is populated from. Channel IDs are the stable IDs minted by package
// playlist, so favourites and health verdicts re-attach to the right rows across
// re-syncs (the old positional IDs shifted every sync, which is why the browser
// keyed favourites by name).
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"watchlive/internal/health"
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
    is_working      INTEGER,
    last_checked_at INTEGER,
    is_favourite    INTEGER NOT NULL DEFAULT 0,
    is_manual       INTEGER NOT NULL DEFAULT 0,
    sort_name       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_channels_sort    ON channels(sort_name);
CREATE INDEX IF NOT EXISTS idx_channels_checked ON channels(last_checked_at);

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// Channel is a catalog row as served to the UI. It mirrors playlist.Channel and
// adds the persisted user state.
type Channel struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Logo        string            `json:"logo"`
	Group       string            `json:"group"`
	Type        string            `json:"type"`
	Servers     []playlist.Server `json:"servers"`
	IsFavourite bool              `json:"is_favourite"`
	// IsWorking is null until the channel has been probed, then true/false. The
	// frontend treats only an explicit false as "hide when working-only".
	IsWorking *bool `json:"is_working"`
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
// carrying its favourite and working state.
func (s *Store) ListChannels() ([]Channel, error) {
	rows, err := s.db.Query(`
		SELECT id, name, logo, grp, typ, servers, is_favourite, is_working
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
		fav       int
		working   sql.NullInt64
	)
	if err := row.Scan(&ch.ID, &ch.Name, &ch.Logo, &ch.Group, &ch.Type, &serversJS, &fav, &working); err != nil {
		return Channel{}, err
	}
	if serversJS != "" {
		if err := json.Unmarshal([]byte(serversJS), &ch.Servers); err != nil {
			return Channel{}, fmt.Errorf("store: servers json for %s: %w", ch.ID, err)
		}
	}
	ch.IsFavourite = fav != 0
	if working.Valid {
		b := working.Int64 != 0
		ch.IsWorking = &b
	}
	return ch, nil
}

// UpsertCatalog inserts new channels and updates the feed-sourced fields of
// existing ones, preserving user state (favourite, working verdict, manual
// rows). It returns counts and the set of IDs present in this feed, which the
// caller can pass to PruneOrphans.
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

	// is_favourite / is_working / last_checked_at are intentionally absent from
	// the SET list so they survive a re-sync; the WHERE keeps manual rows (which
	// never appear in the feed anyway) untouched even on an ID collision.
	stmt, err := tx.Prepare(`
		INSERT INTO channels (id, name, logo, grp, typ, servers, sort_name, is_manual)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, logo=excluded.logo, grp=excluded.grp,
			typ=excluded.typ, servers=excluded.servers, sort_name=excluded.sort_name
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
		if _, err = stmt.Exec(ch.ID, ch.Name, ch.Logo, ch.Group, ch.Type, string(serversJS), strings.ToLower(ch.Name)); err != nil {
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

// AddManual inserts a user-provided channel. It is favourited and marked working
// by default (the user just gave us a URL they want), and idempotent on the same
// name+url so a re-add doesn't duplicate.
func (s *Store) AddManual(name, url string) (Channel, error) {
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	id := "manual:" + manualHash(name, url)
	serversJS, _ := json.Marshal([]playlist.Server{{URL: url}})

	_, err := s.db.Exec(`
		INSERT INTO channels
			(id, name, logo, grp, typ, servers, is_working, last_checked_at, is_favourite, is_manual, sort_name)
		VALUES (?, ?, '', ?, ?, ?, 1, ?, 1, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, servers=excluded.servers, sort_name=excluded.sort_name`,
		id, name, manualGroup, manualType, string(serversJS), now().Unix(), strings.ToLower(name))
	if err != nil {
		return Channel{}, err
	}
	return s.getChannel(id)
}

// ImportEntry is one channel from an imported playlist after the user has
// reviewed/edited it.
type ImportEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
// transaction. Like AddManual they are marked working and survive re-syncs, but
// they are NOT auto-favourited (importing a list shouldn't flood Favourites —
// the single "+" add does favourite, since that's a deliberate one-off).
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
			(id, name, logo, grp, typ, servers, is_working, last_checked_at, is_favourite, is_manual, sort_name)
		VALUES (?, ?, '', ?, ?, ?, 1, ?, 0, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, servers=excluded.servers, sort_name=excluded.sort_name`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	ts := now().Unix()
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
		if _, err = stmt.Exec(id, name, manualGroup, manualType, string(serversJS), ts, strings.ToLower(name)); err != nil {
			return 0, err
		}
		added++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
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

func (s *Store) getChannel(id string) (Channel, error) {
	row := s.db.QueryRow(`
		SELECT id, name, logo, grp, typ, servers, is_favourite, is_working
		FROM channels WHERE id=?`, id)
	return scanChannel(row)
}

// SetHealth records probe verdicts: for each mapped channel it stores the
// reachable flag and the check time. Channels absent from the map keep their
// prior verdict (a selective re-probe only touches stale rows).
func (s *Store) SetHealth(verdicts map[string]bool, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(`UPDATE channels SET is_working=?, last_checked_at=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ts := at.Unix()
	for id, alive := range verdicts {
		v := 0
		if alive {
			v = 1
		}
		if _, err = stmt.Exec(v, ts, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StaleTargets returns probe targets for channels whose verdict is missing or
// older than ttl. force returns every channel regardless of age (the "Working
// only" toggle re-checks the whole catalog).
func (s *Store) StaleTargets(ttl time.Duration, force bool) ([]health.Target, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if force {
		rows, err = s.db.Query(`SELECT id, servers FROM channels`)
	} else {
		cutoff := now().Add(-ttl).Unix()
		rows, err = s.db.Query(`
			SELECT id, servers FROM channels
			WHERE last_checked_at IS NULL OR last_checked_at < ?`, cutoff)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []health.Target
	for rows.Next() {
		var id, serversJS string
		if err := rows.Scan(&id, &serversJS); err != nil {
			return nil, err
		}
		var servers []playlist.Server
		if serversJS != "" {
			if err := json.Unmarshal([]byte(serversJS), &servers); err != nil {
				return nil, err
			}
		}
		urls := make([]string, 0, len(servers))
		for _, sv := range servers {
			urls = append(urls, sv.URL)
		}
		targets = append(targets, health.Target{ID: id, URLs: urls})
	}
	return targets, rows.Err()
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

// HealthVerdicts returns the persisted reachable/unreachable verdict for every
// channel that has been probed (is_working not null). It seeds the in-memory
// prober at startup so a fresh process reports health without re-probing.
func (s *Store) HealthVerdicts() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id, is_working FROM channels WHERE is_working IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var (
			id      string
			working int
		)
		if err := rows.Scan(&id, &working); err != nil {
			return nil, err
		}
		out[id] = working != 0
	}
	return out, rows.Err()
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

func manualHash(name, url string) string {
	sum := sha256.Sum256([]byte(name + "||" + url))
	return hex.EncodeToString(sum[:6]) // 12 hex chars
}
