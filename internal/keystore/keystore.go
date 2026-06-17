// Package keystore persists ClearKey DRM pairs (KID→KEY, lowercase hex) in a
// standalone keys.json file kept OUTSIDE the SQLite catalog directory, so the
// keys survive a database wipe ("clear the junk") and are applied globally to
// any DASH stream whose manifest advertises a matching KID.
//
// ClearKey is not real DRM — the key is handed to the browser in the clear so
// it can decrypt — so this file is a convenience cache, not a secret store. It
// is git-ignored and never served from the embedded static FS; the only way out
// is the authenticated app's own /api/keys endpoint.
package keystore

import (
	"encoding/json"
	"os"
	"sync"
)

// Store is a thread-safe, file-backed KID→KEY map. Reads happen on every DASH
// playback (via the API); writes happen when a key is added through the UI or
// harvested from an imported playlist.
type Store struct {
	path string
	mu   sync.RWMutex
	keys map[string]string
}

// Open loads the keystore at path, returning an empty (in-memory) store if the
// file is absent or empty. A malformed file is a hard error so a typo never
// silently discards saved keys.
func Open(path string) (*Store, error) {
	s := &Store{path: path, keys: map[string]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.keys); err != nil {
		return nil, err
	}
	if s.keys == nil {
		s.keys = map[string]string{}
	}
	return s, nil
}

// All returns a copy of the full KID→KEY map, safe to hand to a JSON encoder
// without holding the lock.
func (s *Store) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.keys))
	for k, v := range s.keys {
		out[k] = v
	}
	return out
}

// Count returns how many pairs are stored.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys)
}

// Merge adds the given pairs (a later value overwrites an existing KID) and
// persists the file only when something actually changed. Blank kid/key entries
// are skipped; a nil/empty input is a no-op. Returns how many pairs were added
// or changed.
func (s *Store) Merge(pairs map[string]string) (changed int, err error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for kid, key := range pairs {
		if kid == "" || key == "" {
			continue
		}
		if s.keys[kid] != key {
			s.keys[kid] = key
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.save(); err != nil {
		return changed, err
	}
	return changed, nil
}

// save writes the map atomically (temp file + rename) so a crash mid-write can
// never corrupt keys.json. json.Marshal sorts string keys, so the file is
// diff-friendly. The caller holds the write lock.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
