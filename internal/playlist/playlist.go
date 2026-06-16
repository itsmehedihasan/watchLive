// Package playlist parses M3U/M3U8 channel playlists into Channel entries.
// Entries that refer to the same channel under different names — e.g.
// "CNN (720p)" and "CNN (1080p) [Geo-blocked]" — are grouped into one
// Channel with multiple Servers.
package playlist

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// Server is one stream source for a channel.
type Server struct {
	URL string `json:"url"`
	// Label carries quality/status hints stripped from the original entry
	// name, e.g. "1080p" or "720p · Geo-blocked".
	Label string `json:"label,omitempty"`
}

// Channel is a single channel in the UI, backed by one or more servers.
type Channel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Logo    string   `json:"logo"`
	Group   string   `json:"group"`
	Type    string   `json:"type"`
	Servers []Server `json:"servers"`

	// TvgID is the upstream tvg-id (when present); it seeds the stable channel
	// ID. mergeKey is the (group, normalized-name) key the channel was merged
	// under and is the fallback basis for the stable ID. Neither is serialized.
	TvgID    string `json:"-"`
	mergeKey string `json:"-"`
}

var (
	logoRe  = regexp.MustCompile(`tvg-logo="([^"]*)"`)
	groupRe = regexp.MustCompile(`group-title="([^"]*)"`)
	genreRe = regexp.MustCompile(`tvg-genre="([^"]*)"`)
	tvgIDRe = regexp.MustCompile(`tvg-id="([^"]*)"`)
	httpRe  = regexp.MustCompile(`(?i)^https?://`)
	// Quality suffixes like "(720p)", "(1080p)", "(2160p)" and resolution
	// forms like "(640x360)" — used as iptv-org naming convention.
	qualityRe = regexp.MustCompile(`\s*\((\d{3,4}p|\d+x\d+)\)`)
	// Bracketed status tags like "[Geo-blocked]", "[Not 24/7]".
	statusRe = regexp.MustCompile(`\s*\[([^\]]*)\]`)
	spaceRe  = regexp.MustCompile(`\s{2,}`)
)

// classify maps a group-title to one of the fixed UI categories.
func classify(group string) string {
	g := strings.TrimSpace(strings.ToLower(group))
	switch {
	case strings.Contains(g, "news"):
		return "News"
	case strings.Contains(g, "movie"), strings.Contains(g, "cinema"), strings.HasPrefix(g, "goldmine"):
		return "Movies"
	case strings.Contains(g, "music"), strings.Contains(g, "talkies"), strings.Contains(g, "beats"), strings.Contains(g, "sangeet"):
		return "Music"
	case g == "sports", g == "live sports", strings.Contains(g, "football"), g == "bijoy", strings.Contains(g, "cricket"):
		return "Sports"
	case g == "kids", strings.Contains(g, "cartoon"), strings.Contains(g, "duronto"):
		return "Kids"
	case strings.Contains(g, "relagion"), strings.Contains(g, "religion"), g == "islamic", strings.Contains(g, "peace"):
		return "Religious"
	default:
		return "Entertainment"
	}
}

// normalizeName strips quality and status decorations from an entry name.
// It returns the clean display name and a label describing what was
// stripped, e.g. ("CNN", "1080p · Geo-blocked").
func normalizeName(name string) (clean, label string) {
	var parts []string
	if m := qualityRe.FindStringSubmatch(name); m != nil {
		parts = append(parts, m[1])
	}
	for _, m := range statusRe.FindAllStringSubmatch(name, -1) {
		if tag := strings.TrimSpace(m[1]); tag != "" {
			parts = append(parts, tag)
		}
	}
	clean = qualityRe.ReplaceAllString(name, "")
	clean = statusRe.ReplaceAllString(clean, "")
	clean = strings.TrimSpace(spaceRe.ReplaceAllString(clean, " "))
	return clean, strings.Join(parts, " · ")
}

// Entry is one raw #EXTINF + stream URL extracted from an M3U, before entries
// for the same channel are merged into a Channel with multiple Servers.
type Entry struct {
	Name, Logo, Group, URL, Genre, TvgID string
}

// ParseEntries extracts the raw (name, url, …) entries from M3U content, one per
// #EXTINF + following stream URL, deduplicated by (group, name, url). Unlike
// Parse it does no merging — useful for an import flow that shows the user one
// editable row per stream. Parse is built on top of it.
func ParseEntries(content string) []Entry {
	rawLines := strings.Split(content, "\n")
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = strings.TrimSpace(l)
	}

	var entries []Entry
	seen := make(map[string]struct{})

	for i, line := range lines {
		if !strings.HasPrefix(line, "#EXTINF") {
			continue
		}

		logo := ""
		if m := logoRe.FindStringSubmatch(line); m != nil {
			logo = m[1]
		}

		group := "Other"
		if m := groupRe.FindStringSubmatch(line); m != nil {
			group = strings.TrimSpace(m[1])
		}

		// tvg-genre, when present (stamped from iptv-org metadata), is an
		// explicit UI category that overrides group-based classification.
		genre := ""
		if m := genreRe.FindStringSubmatch(line); m != nil {
			genre = strings.TrimSpace(m[1])
		}

		tvgID := ""
		if m := tvgIDRe.FindStringSubmatch(line); m != nil {
			tvgID = strings.TrimSpace(m[1])
		}

		name := ""
		if idx := strings.LastIndex(line, ","); idx >= 0 {
			name = strings.TrimSpace(line[idx+1:])
		}
		if name == "" {
			continue
		}

		// Take the first valid HTTP URL after this #EXTINF line.
		url := ""
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			if strings.HasPrefix(next, "#EXTINF") {
				break
			}
			if next != "" && !strings.HasPrefix(next, "#") && httpRe.MatchString(next) {
				url = next
				break
			}
		}
		if url == "" {
			continue
		}

		key := group + "||" + name + "||" + url
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, Entry{Name: name, Logo: logo, Group: group, URL: url, Genre: genre, TvgID: tvgID})
	}
	return entries
}

// normalizeID strips the feed/quality suffix some playlists append to tvg-id
// (e.g. "BBCNews.uk@SD" → "BBCNews.uk"). Mirrors genre.normalizeID; duplicated
// here to avoid an import cycle (genre imports nothing from playlist, but
// keeping playlist dependency-free is cheaper than the coupling).
func normalizeID(id string) string {
	if i := strings.IndexByte(id, '@'); i >= 0 {
		return id[:i]
	}
	return id
}

// stableID derives a durable channel ID that survives re-syncs (unlike the old
// positional index). A real tvg-id wins; otherwise the (group, normalized-name)
// merge key is hashed. Prefixes namespace the three ID spaces (tvg:/h:/manual:)
// so values from different sources can never collide.
func stableID(tvgID, mergeKey string) string {
	if t := normalizeID(strings.TrimSpace(tvgID)); t != "" {
		return "tvg:" + t
	}
	sum := sha256.Sum256([]byte(mergeKey))
	return "h:" + hex.EncodeToString(sum[:8])
}

// Parse extracts channels from M3U content. Entries in the same group whose
// normalized names match (case-insensitive) are merged into one channel; each
// distinct URL becomes a Server, in order of appearance. The group is part of
// the merge key so that identically named channels from different groups
// (e.g. countries) stay separate.
func Parse(content string) []Channel {
	entries := ParseEntries(content)

	// Group entries by normalized name into channels with servers.
	var channels []Channel
	byKey := make(map[string]int)

	for _, e := range entries {
		clean, label := normalizeName(e.Name)
		if clean == "" {
			clean = e.Name
		}
		key := strings.ToLower(e.Group) + "||" + strings.ToLower(clean)

		idx, ok := byKey[key]
		if !ok {
			idx = len(channels)
			byKey[key] = idx
			// An explicit tvg-genre wins; otherwise fall back to classifying
			// the group title (which for country-grouped lists is just a code,
			// so most land in Entertainment until enriched).
			typ := e.Genre
			if typ == "" {
				typ = classify(e.Group)
			}
			channels = append(channels, Channel{
				Name:     clean,
				Logo:     e.Logo,
				Group:    e.Group,
				Type:     typ,
				TvgID:    e.TvgID,
				mergeKey: key,
			})
		}
		ch := &channels[idx]
		if ch.Logo == "" {
			ch.Logo = e.Logo
		}
		// First non-empty tvg-id among the merged entries seeds the stable ID,
		// mirroring the logo backfill above.
		if ch.TvgID == "" {
			ch.TvgID = e.TvgID
		}

		dup := false
		for _, s := range ch.Servers {
			if s.URL == e.URL {
				dup = true
				break
			}
		}
		if !dup {
			ch.Servers = append(ch.Servers, Server{URL: e.URL, Label: label})
		}
	}

	// Assign stable IDs in first-appearance (feed) order so tvg-id collision
	// resolution is deterministic regardless of the display sort below: the
	// first channel claiming a tvg-id keeps it; later colliders fall back to
	// their hash id. Two distinct merge keys can resolve to the same tvg-id in
	// real iptv-org feeds, so this guards the SQLite primary key.
	usedTvg := make(map[string]bool, len(channels))
	for i := range channels {
		ch := &channels[i]
		id := stableID(ch.TvgID, ch.mergeKey)
		if strings.HasPrefix(id, "tvg:") && usedTvg[id] {
			sum := sha256.Sum256([]byte(ch.mergeKey))
			id = "h:" + hex.EncodeToString(sum[:8])
		}
		if strings.HasPrefix(id, "tvg:") {
			usedTvg[id] = true
		}
		ch.ID = id
	}

	// Sort A→Z (case-insensitive) for display; IDs are content-derived above so
	// the order no longer affects them.
	sort.SliceStable(channels, func(i, j int) bool {
		return strings.ToLower(channels[i].Name) < strings.ToLower(channels[j].Name)
	})

	return channels
}
