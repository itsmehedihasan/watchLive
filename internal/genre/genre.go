// Package genre maps channels to the app's fixed UI categories using
// iptv-org's channel database, which carries category tags keyed by channel id
// (the same value playlists put in tvg-id). It exposes a lookup map plus an
// Inject helper that stamps a tvg-genre="<Category>" attribute onto each
// #EXTINF line so the playlist parser can categorize channels without a
// network round-trip of its own.
package genre

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DatabaseURL is iptv-org's channel metadata, keyed by channel id (== tvg-id).
const DatabaseURL = "https://iptv-org.github.io/api/channels.json"

// Categories are the fixed UI buckets every channel is reduced to.
const (
	News          = "News"
	Sports        = "Sports"
	Movies        = "Movies"
	Music         = "Music"
	Kids          = "Kids"
	Religious     = "Religious"
	Entertainment = "Entertainment"
)

// Map resolves a tvg-id (feed suffix stripped) to one app category.
type Map map[string]string

type apiChannel struct {
	ID         string   `json:"id"`
	Country    string   `json:"country"`
	Categories []string `json:"categories"`
	IsNSFW     bool     `json:"is_nsfw"`
	Closed     *string  `json:"closed"`
}

// Info is the per-channel metadata the app needs, resolved from the iptv-org
// database and keyed by channel id (== tvg-id, feed suffix stripped).
type Info struct {
	Category string // one of the fixed app categories
	Country  string // ISO 3166-1 alpha-2, uppercase; "" if unknown
	NSFW     bool
	Closed   bool // channel has a closed date → off-air
}

// DB resolves a tvg-id to its Info. It is the richer counterpart of Map, used
// when both country (for the country sidebar) and category (for the category
// sidebar) are needed — e.g. when sourcing the whole catalog from the API.
type DB map[string]Info

// Load fetches the iptv-org channel database and builds an id → category map.
func Load() (Map, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(DatabaseURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var chans []apiChannel
	if err := json.Unmarshal(data, &chans); err != nil {
		return nil, err
	}
	m := make(Map, len(chans))
	for _, c := range chans {
		if c.ID == "" {
			continue
		}
		m[c.ID] = categoryFor(c.Categories)
	}
	return m, nil
}

// LoadDB fetches the iptv-org channel database and builds an id → Info map,
// carrying country, category and content flags for each channel.
func LoadDB() (DB, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(DatabaseURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var chans []apiChannel
	if err := json.Unmarshal(data, &chans); err != nil {
		return nil, err
	}
	db := make(DB, len(chans))
	for _, c := range chans {
		if c.ID == "" {
			continue
		}
		db[c.ID] = Info{
			Category: categoryFor(c.Categories),
			Country:  strings.ToUpper(strings.TrimSpace(c.Country)),
			NSFW:     c.IsNSFW,
			Closed:   c.Closed != nil && strings.TrimSpace(*c.Closed) != "",
		}
	}
	return db, nil
}

// categoryFor reduces iptv-org's category slugs to one app category. The first
// slug that maps to a specific category wins; channels with only generic tags
// fall through to Entertainment.
func categoryFor(cats []string) string {
	for _, c := range cats {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "news", "weather":
			return News
		case "sports", "outdoor":
			return Sports
		case "movies", "series":
			return Movies
		case "music":
			return Music
		case "kids", "animation", "family":
			return Kids
		case "religious":
			return Religious
		}
	}
	return Entertainment
}

// normalizeID strips the feed/quality suffix some playlists append to tvg-id
// (e.g. "BBCNews.uk@SD" → "BBCNews.uk") so it matches the database key.
func normalizeID(id string) string {
	if i := strings.IndexByte(id, '@'); i >= 0 {
		return id[:i]
	}
	return id
}

var (
	tvgIDRe = regexp.MustCompile(`tvg-id="([^"]*)"`)
	// existing tvg-genre (with any leading whitespace), removed before re-stamping
	genreRe = regexp.MustCompile(`\s*tvg-genre="[^"]*"`)
	// the "#EXTINF:<duration>" head, up to the first space or comma
	headRe = regexp.MustCompile(`^(#EXTINF:[^\s,]*)`)
	// group-title attribute (value captured), removed/rewritten during Enrich
	groupAttrRe = regexp.MustCompile(`\s*group-title="([^"]*)"`)
)

// Inject rewrites every #EXTINF line to carry tvg-genre="<Category>", looked up
// by tvg-id. Existing tvg-genre attributes are replaced; lines whose tvg-id
// can't be resolved are left untouched. Returns the rewritten playlist and the
// number of lines stamped.
func (m Map) Inject(playlist []byte) ([]byte, int) {
	lines := strings.Split(string(playlist), "\n")
	stamped := 0
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXTINF") {
			continue
		}
		match := tvgIDRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		cat, ok := m[normalizeID(match[1])]
		if !ok {
			continue
		}
		line = genreRe.ReplaceAllString(line, "")
		lines[i] = headRe.ReplaceAllString(line, `$1 tvg-genre="`+cat+`"`)
		stamped++
	}
	if stamped == 0 {
		return playlist, 0
	}
	return []byte(strings.Join(lines, "\n")), stamped
}

// Enrich rewrites every #EXTINF line for the two-sidebar UI: group-title is set
// to the channel's country code (so the country sidebar groups correctly) and
// tvg-genre to its app category. Category comes from the line's original
// group-title — in iptv-org's index.m3u that field already holds categories —
// and country from the database, by tvg-id; entries whose country can't be
// resolved are grouped under "Other". Returns the rewritten playlist and the
// number of #EXTINF lines processed.
//
// Doing this at fetch time means the written list.m3u is self-describing: an
// offline reload reconstructs the same country/category split with no network.
func (db DB) Enrich(playlist []byte) ([]byte, int) {
	lines := strings.Split(string(playlist), "\n")
	processed := 0
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXTINF") {
			continue
		}

		category := Entertainment
		if m := groupAttrRe.FindStringSubmatch(line); m != nil {
			category = categoryFor(splitCategories(m[1]))
		}
		country := "Other"
		if m := tvgIDRe.FindStringSubmatch(line); m != nil {
			if info, ok := db[normalizeID(m[1])]; ok && info.Country != "" {
				country = info.Country
			}
		}

		line = genreRe.ReplaceAllString(line, "")
		line = groupAttrRe.ReplaceAllString(line, "")
		lines[i] = headRe.ReplaceAllString(line, `$1 group-title="`+country+`" tvg-genre="`+category+`"`)
		processed++
	}
	if processed == 0 {
		return playlist, 0
	}
	return []byte(strings.Join(lines, "\n")), processed
}

// splitCategories splits a group-title that may list several categories
// (iptv-org uses ";" e.g. "Entertainment;Family") into trimmed slugs.
func splitCategories(group string) []string {
	parts := strings.Split(group, ";")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
