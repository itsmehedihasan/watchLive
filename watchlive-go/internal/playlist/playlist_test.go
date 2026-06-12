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

func TestParseGroupsServers(t *testing.T) {
	got := Parse(sample)

	// T Sports (exact dup URL collapses), Somoy TV, Star Sports (2 servers),
	// Peace TV. "No URL Channel" is dropped. Sorted A→Z:
	// Peace TV, Somoy TV, Star Sports, T Sports.
	if len(got) != 4 {
		t.Fatalf("expected 4 channels, got %d: %+v", len(got), got)
	}

	wantOrder := []string{"Peace TV", "Somoy TV", "Star Sports", "T Sports"}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Fatalf("order wrong at %d: got %q, want %q", i, got[i].Name, name)
		}
	}

	ts := got[3]
	if len(ts.Servers) != 1 {
		t.Errorf("T Sports wrong: %+v", ts)
	}
	if ts.Servers[0].URL != "http://198.195.239.50:8095/Tsports/tracks-v1a1/mono.m3u8" {
		t.Errorf("T Sports URL wrong: %+v", ts.Servers)
	}
	if ts.Logo != "https://example.com/logo1.png" {
		t.Errorf("logo not extracted: %q", ts.Logo)
	}

	// Same name + different URLs → one channel, two servers, order preserved.
	star := got[2]
	if star.Name != "Star Sports" || len(star.Servers) != 2 {
		t.Fatalf("Star Sports not grouped: %+v", star)
	}
	if star.Servers[0].URL != "https://example.com/star/index.m3u8" ||
		star.Servers[1].URL != "https://example.com/star2/index.m3u8" {
		t.Errorf("server order wrong: %+v", star.Servers)
	}

	// IDs are sequential indices.
	for i, ch := range got {
		if ch.ID != string(rune('0'+i)) {
			t.Errorf("channel %d has ID %q", i, ch.ID)
		}
	}
}

func TestParseNormalizedGrouping(t *testing.T) {
	const m3u = `#EXTM3U
#EXTINF:-1 group-title="News",CNN (720p)
https://example.com/cnn-720.m3u8
#EXTINF:-1 tvg-logo="https://example.com/cnn.png" group-title="News",CNN (1080p) [Geo-blocked]
https://example.com/cnn-1080.m3u8
#EXTINF:-1 group-title="News",CNN International
https://example.com/cnn-intl.m3u8
`
	got := Parse(m3u)
	if len(got) != 2 {
		t.Fatalf("expected 2 channels, got %d: %+v", len(got), got)
	}

	cnn := got[0]
	if cnn.Name != "CNN" || len(cnn.Servers) != 2 {
		t.Fatalf("CNN variants not grouped: %+v", cnn)
	}
	if cnn.Servers[0].Label != "720p" {
		t.Errorf("server 0 label = %q, want 720p", cnn.Servers[0].Label)
	}
	if cnn.Servers[1].Label != "1080p · Geo-blocked" {
		t.Errorf("server 1 label = %q", cnn.Servers[1].Label)
	}
	// Logo backfilled from the second entry (first had none).
	if cnn.Logo != "https://example.com/cnn.png" {
		t.Errorf("logo not backfilled: %q", cnn.Logo)
	}

	// "CNN International" must NOT merge into "CNN".
	if got[1].Name != "CNN International" {
		t.Errorf("unexpected merge: %+v", got[1])
	}
}

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, clean, label string }{
		{"CNN", "CNN", ""},
		{"CNN (720p)", "CNN", "720p"},
		{"CNN (1080p) [Geo-blocked]", "CNN", "1080p · Geo-blocked"},
		{"Some TV (640x360) [Not 24/7]", "Some TV", "640x360 · Not 24/7"},
		{"T Sports 1", "T Sports 1", ""}, // trailing digits are part of the name
		{"(2160p) 4K Channel", "4K Channel", "2160p"},
	}
	for _, c := range cases {
		clean, label := normalizeName(c.in)
		if clean != c.clean || label != c.label {
			t.Errorf("normalizeName(%q) = (%q, %q), want (%q, %q)", c.in, clean, label, c.clean, c.label)
		}
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
