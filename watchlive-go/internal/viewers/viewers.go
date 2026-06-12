// Package viewers tracks live viewer sessions per channel.
//
// The original implementation kept Redis counters (INCR/DECR with TTLs) next
// to session records, which could drift on races (see the counter-inflation
// fix in the old codebase). Here the session map is the single source of
// truth and every count is derived from it under one mutex, so counts cannot
// drift by construction. The HTTP contract is unchanged:
//
//	POST {sessionId, channelId} → {total, channelCount, top}
//	GET  ?channelId=&top=n      → {total, channelCount, top}
package viewers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	// SessionTTL is how long a session counts as active after its last heartbeat.
	SessionTTL = 60 * time.Second
	defaultTop = 5
	maxTop     = 20
)

type session struct {
	channelID string
	lastSeen  time.Time
}

// Store holds all viewer state. Safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*session
	tuneIns  map[string]int64 // cumulative tune-ins per channel, never expires
	now      func() time.Time
}

// NewStore creates an empty viewer store.
func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*session),
		tuneIns:  make(map[string]int64),
		now:      time.Now,
	}
}

// TopChannel is one entry of the tune-in leaderboard.
type TopChannel struct {
	ID    string `json:"id"`
	Count int64  `json:"count"`
}

// Snapshot is the API response shape.
type Snapshot struct {
	Total        int          `json:"total"`
	ChannelCount *int         `json:"channelCount"`
	Top          []TopChannel `json:"top"`
}

// Heartbeat registers a session as active on channelID (may be empty when the
// user is on the home screen) and returns fresh counts.
func (s *Store) Heartbeat(sessionID, channelID string, topN int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.pruneLocked(now)

	prev, exists := s.sessions[sessionID]
	// A tune-in is counted when a session starts watching a channel it wasn't
	// already on — new session or channel switch. Rapid switching back and
	// forth still cannot inflate live counts: those are derived from sessions.
	if channelID != "" && (!exists || prev.channelID != channelID) {
		s.tuneIns[channelID]++
	}
	if exists {
		prev.channelID = channelID
		prev.lastSeen = now
	} else {
		s.sessions[sessionID] = &session{channelID: channelID, lastSeen: now}
	}

	return s.snapshotLocked(channelID, topN)
}

// Read returns current counts without registering a heartbeat.
// channelID may be empty; ChannelCount is null in that case.
func (s *Store) Read(channelID string, topN int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return s.snapshotLocked(channelID, topN)
}

// Prune drops sessions whose last heartbeat is older than SessionTTL.
// Called periodically from a background sweeper; reads also prune inline.
func (s *Store) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
}

func (s *Store) pruneLocked(now time.Time) {
	for id, sess := range s.sessions {
		if now.Sub(sess.lastSeen) > SessionTTL {
			delete(s.sessions, id)
		}
	}
}

func (s *Store) snapshotLocked(channelID string, topN int) Snapshot {
	snap := Snapshot{Total: len(s.sessions), Top: s.topLocked(topN)}
	if channelID != "" {
		n := 0
		for _, sess := range s.sessions {
			if sess.channelID == channelID {
				n++
			}
		}
		snap.ChannelCount = &n
	}
	return snap
}

func (s *Store) topLocked(n int) []TopChannel {
	if n <= 0 {
		n = defaultTop
	}
	if n > maxTop {
		n = maxTop
	}
	top := make([]TopChannel, 0, len(s.tuneIns))
	for id, count := range s.tuneIns {
		top = append(top, TopChannel{ID: id, Count: count})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].ID < top[j].ID // deterministic order for equal counts
	})
	if len(top) > n {
		top = top[:n]
	}
	return top
}

// Handler serves /api/viewers (GET and POST).
type Handler struct {
	Store *Store
}

type heartbeatBody struct {
	SessionID string `json:"sessionId"`
	ChannelID string `json:"channelId"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body heartbeatBody
		// Mirror the original behavior: a malformed body or missing sessionId
		// skips the write but still returns current counts.
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		var snap Snapshot
		if err == nil && body.SessionID != "" {
			snap = h.Store.Heartbeat(body.SessionID, body.ChannelID, defaultTop)
		} else {
			snap = h.Store.Read("", defaultTop)
		}
		writeJSON(w, snap)
	case http.MethodGet:
		cid := r.URL.Query().Get("channelId")
		topN := defaultTop
		if v := r.URL.Query().Get("top"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				topN = n
			}
		}
		writeJSON(w, h.Store.Read(cid, topN))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
