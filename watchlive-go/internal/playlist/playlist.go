// Package playlist parses M3U/M3U8 channel playlists into Channel entries.
// It is a faithful port of the original lib/parseM3U.ts.
package playlist

import (
	"regexp"
	"strconv"
	"strings"
)

// Channel is a single playable entry from the playlist.
type Channel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Logo  string `json:"logo"`
	Group string `json:"group"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

var (
	logoRe  = regexp.MustCompile(`tvg-logo="([^"]*)"`)
	groupRe = regexp.MustCompile(`group-title="([^"]*)"`)
	httpRe  = regexp.MustCompile(`(?i)^https?://`)
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

// Parse extracts channels from M3U content. Duplicate (group, name, url)
// triples are dropped; channels sharing a display name are suffixed
// "Name - 1", "Name - 2", … to keep names unique in the UI.
func Parse(content string) []Channel {
	rawLines := strings.Split(content, "\n")
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = strings.TrimSpace(l)
	}

	var channels []Channel
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
		channels = append(channels, Channel{
			ID:    strconv.Itoa(len(channels)),
			Name:  name,
			Logo:  logo,
			Group: group,
			URL:   url,
			Type:  classify(group),
		})
	}

	// Number duplicate names: "Channel" → "Channel - 1", "Channel - 2", …
	nameCount := make(map[string]int)
	for _, ch := range channels {
		nameCount[ch.Name]++
	}
	nameCursor := make(map[string]int)
	for idx := range channels {
		name := channels[idx].Name
		if nameCount[name] > 1 {
			nameCursor[name]++
			channels[idx].Name = name + " - " + strconv.Itoa(nameCursor[name])
		}
	}

	return channels
}
