// Package playlist parses M3U/M3U8 channel playlists into Channel entries.
// Entries that refer to the same channel under different names — e.g.
// "CNN (720p)" and "CNN (1080p) [Geo-blocked]" — are grouped into one
// Channel with multiple Servers.
package playlist

import (
	"regexp"
	"sort"
	"strconv"
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
}

var (
	logoRe  = regexp.MustCompile(`tvg-logo="([^"]*)"`)
	groupRe = regexp.MustCompile(`group-title="([^"]*)"`)
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

type rawEntry struct {
	name, logo, group, url string
}

// Parse extracts channels from M3U content. Entries in the same group whose
// normalized names match (case-insensitive) are merged into one channel; each
// distinct URL becomes a Server, in order of appearance. The group is part of
// the merge key so that identically named channels from different groups
// (e.g. countries) stay separate.
func Parse(content string) []Channel {
	rawLines := strings.Split(content, "\n")
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = strings.TrimSpace(l)
	}

	var entries []rawEntry
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
		entries = append(entries, rawEntry{name: name, logo: logo, group: group, url: url})
	}

	// Group entries by normalized name into channels with servers.
	var channels []Channel
	byKey := make(map[string]int)

	for _, e := range entries {
		clean, label := normalizeName(e.name)
		if clean == "" {
			clean = e.name
		}
		key := strings.ToLower(e.group) + "||" + strings.ToLower(clean)

		idx, ok := byKey[key]
		if !ok {
			idx = len(channels)
			byKey[key] = idx
			channels = append(channels, Channel{
				Name:  clean,
				Logo:  e.logo,
				Group: e.group,
				Type:  classify(e.group),
			})
		}
		ch := &channels[idx]
		if ch.Logo == "" {
			ch.Logo = e.logo
		}

		dup := false
		for _, s := range ch.Servers {
			if s.URL == e.url {
				dup = true
				break
			}
		}
		if !dup {
			ch.Servers = append(ch.Servers, Server{URL: e.url, Label: label})
		}
	}

	// Sort A→Z (case-insensitive); IDs are assigned after sorting so they
	// stay sequential in display order.
	sort.SliceStable(channels, func(i, j int) bool {
		return strings.ToLower(channels[i].Name) < strings.ToLower(channels[j].Name)
	})
	for i := range channels {
		channels[i].ID = strconv.Itoa(i)
	}

	return channels
}
