// Package resolver turns a "recipe" (a provider name + a small argument such as
// a stream slug) into a fresh, playable stream URL at request time.
//
// Some aggregator/embed providers don't expose a stable stream link: the .m3u8
// is a short-lived, server-signed URL (e.g. nginx secure_link, "?md5=…&expires=…")
// that the embed page mints on every load. We can't forge those tokens — the
// signing secret lives only on their server — so the only way to play such a
// channel is to ask the embed page for a fresh URL each time, exactly as a
// browser would. A Provider encapsulates how to do that for one provider family;
// the Manager adds short-lived caching so we don't refetch on every segment.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnknownProvider is returned when a recipe names a provider we don't have.
var ErrUnknownProvider = errors.New("resolver: unknown provider")

// Resolved is a freshly-minted, playable stream reference.
type Resolved struct {
	URL       string    // the fresh stream URL (usually an .m3u8)
	Referer   string    // Referer the CDN requires when PLAYING the stream
	UserAgent string    // optional UA override (empty = proxy default)
	Expires   time.Time // when URL stops working; zero if unknown
}

// Provider knows how to resolve one provider family's channels.
type Provider interface {
	// Name is the stable key stored on a channel's recipe.
	Name() string
	// Resolve turns the recipe argument (e.g. a stream slug) into a fresh
	// playable reference. It must be safe for concurrent use.
	Resolve(ctx context.Context, arg string) (Resolved, error)
}

// parseExpires reads the "expires" unix-timestamp query parameter that
// secure_link-style signed URLs carry, so the Manager can cache until just
// before it lapses. Returns the zero time when absent/unparseable.
var expiresRe = regexp.MustCompile(`[?&]expires=(\d+)`)

func parseExpires(rawURL string) time.Time {
	m := expiresRe.FindStringSubmatch(rawURL)
	if m == nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// --- Manager: registry + short-lived cache --------------------------------

type cached struct {
	res    Resolved
	stored time.Time
}

// Manager holds the provider registry and caches resolved URLs until shortly
// before they expire, so a channel that's actively playing (many segment and
// playlist refreshes) only triggers one upstream resolve per token lifetime.
type Manager struct {
	mu        sync.Mutex
	providers map[string]Provider
	cache     map[string]cached
	now       func() time.Time // injectable for tests
	// margin is how long before Expires we treat a cached entry as stale, so we
	// never hand back a URL that dies mid-playback.
	margin time.Duration
	// fallbackTTL caps cache lifetime when a resolved URL has no parseable
	// expiry, so we still refresh periodically.
	fallbackTTL time.Duration
}

// NewManager builds an empty Manager. Register providers with Add.
func NewManager() *Manager {
	return &Manager{
		providers:   map[string]Provider{},
		cache:       map[string]cached{},
		now:         time.Now,
		margin:      30 * time.Second,
		fallbackTTL: 2 * time.Minute,
	}
}

// Add registers a provider under its Name. Not safe to call concurrently with
// Resolve; call it during setup.
func (m *Manager) Add(p Provider) { m.providers[p.Name()] = p }

// Has reports whether a provider is registered.
func (m *Manager) Has(name string) bool {
	_, ok := m.providers[name]
	return ok
}

func (m *Manager) fresh(c cached) bool {
	now := m.now()
	if !c.res.Expires.IsZero() {
		return now.Before(c.res.Expires.Add(-m.margin))
	}
	return now.Sub(c.stored) < m.fallbackTTL
}

// Resolve returns a fresh playable reference for (provider, arg), serving a
// cached value while it's still comfortably valid.
func (m *Manager) Resolve(ctx context.Context, provider, arg string) (Resolved, error) {
	key := provider + "\x00" + arg

	m.mu.Lock()
	if c, ok := m.cache[key]; ok && m.fresh(c) {
		m.mu.Unlock()
		return c.res, nil
	}
	p := m.providers[provider]
	m.mu.Unlock()

	if p == nil {
		return Resolved{}, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}

	res, err := p.Resolve(ctx, arg)
	if err != nil {
		return Resolved{}, err
	}
	if res.Expires.IsZero() {
		res.Expires = parseExpires(res.URL)
	}

	m.mu.Lock()
	m.cache[key] = cached{res: res, stored: m.now()}
	m.mu.Unlock()
	return res, nil
}

// --- Shared extraction helpers (used by page-scraping providers) ----------

// jsUnescape turns the few JS string escapes that appear in char-array literals
// (mainly "\/") back into their literal characters.
var jsUnescaper = strings.NewReplacer(`\/`, `/`, `\\`, `\`, `\"`, `"`)

func jsUnescape(s string) string { return jsUnescaper.Replace(s) }
