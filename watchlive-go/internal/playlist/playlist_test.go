package playlist

import "testing"

const sample = `#EXTM3U
#EXTINF:-1 tvg-logo="https://example.com/logo1.png" group-title="Bangla",T Sports
http://198.195.239.50:8095/Tsports/tracks-v1a1/mono.m3u8
#EXTINF:-1 tvg-logo="https://example.com/logo2.png" group-title="Bangla News",Somoy TV
https://example.com/somoy/index.m3u8
#EXTINF:-1 group-title="Sports",Star Sports
https://example.com/star/index.m3u8
#EXTINF:-1 group-title="Sports",Star Sports
https://example.com/star2/index.m3u8
#EXTINF:-1 group-title="Kids",No URL Channel
#EXTINF:-1 group-title="Islamic",Peace TV
https://example.com/peace.m3u8
#EXTINF:-1 group-title="Bangla",T Sports
http://198.195.239.50:8095/Tsports/tracks-v1a1/mono.m3u8
`

func TestParse(t *testing.T) {
	got := Parse(sample)

	// 6 EXTINF entries with URLs, one exact duplicate dropped, one without URL dropped.
	if len(got) != 5 {
		t.Fatalf("expected 5 channels, got %d: %+v", len(got), got)
	}

	if got[0].Name != "T Sports" || got[0].URL != "http://198.195.239.50:8095/Tsports/tracks-v1a1/mono.m3u8" {
		t.Errorf("first channel wrong: %+v", got[0])
	}
	if got[0].Logo != "https://example.com/logo1.png" {
		t.Errorf("logo not extracted: %q", got[0].Logo)
	}

	// IDs are sequential indices.
	for i, ch := range got {
		if ch.ID != string(rune('0'+i)) {
			t.Errorf("channel %d has ID %q", i, ch.ID)
		}
	}

	// Duplicate display names get numbered suffixes.
	if got[2].Name != "Star Sports - 1" || got[3].Name != "Star Sports - 2" {
		t.Errorf("duplicate names not numbered: %q, %q", got[2].Name, got[3].Name)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"Bangla News":  "News",
		"Movies":       "Movies",
		"Cinema Hall":  "Movies",
		"Music Beats":  "Music",
		"Sports":       "Sports",
		"Live Sports":  "Sports",
		"Football HD":  "Sports",
		"Kids":         "Kids",
		"Cartoon Land": "Kids",
		"Islamic":      "Religious",
		"Peace TV":     "Religious",
		"Bangla":       "Entertainment",
		"Other":        "Entertainment",
	}
	for group, want := range cases {
		if got := classify(group); got != want {
			t.Errorf("classify(%q) = %q, want %q", group, got, want)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	if got := Parse(""); len(got) != 0 {
		t.Errorf("expected no channels from empty input, got %d", len(got))
	}
}
