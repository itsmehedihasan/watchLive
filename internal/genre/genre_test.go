package genre

import "testing"

func TestCategoryFor(t *testing.T) {
	cases := []struct {
		cats []string
		want string
	}{
		{[]string{"news"}, News},
		{[]string{"sports"}, Sports},
		{[]string{"movies"}, Movies},
		{[]string{"series"}, Movies},
		{[]string{"music"}, Music},
		{[]string{"kids", "animation"}, Kids},
		{[]string{"religious"}, Religious},
		{[]string{"general"}, Entertainment},
		{nil, Entertainment},
		// first specific slug wins over a trailing generic one
		{[]string{"general", "news"}, News},
	}
	for _, c := range cases {
		if got := categoryFor(c.cats); got != c.want {
			t.Errorf("categoryFor(%v) = %q, want %q", c.cats, got, c.want)
		}
	}
}

func TestInject(t *testing.T) {
	m := Map{"BBCNews.uk": News, "ESPN.us": Sports}
	in := []byte("#EXTM3U\n" +
		// feed suffix must be stripped to match the map key
		`#EXTINF:-1 tvg-id="BBCNews.uk@SD" group-title="UK",BBC News` + "\n" +
		"https://a/s.m3u8\n" +
		// no attributes before the comma — genre inserted after the duration
		`#EXTINF:-1,ESPN` + "\n" +
		"https://b/s.m3u8\n" +
		// unknown id — left untouched
		`#EXTINF:-1 tvg-id="Unknown.xx",Mystery` + "\n" +
		"https://c/s.m3u8\n")

	out, stamped := m.Inject(in)
	if stamped != 1 {
		t.Fatalf("stamped = %d, want 1 (only BBCNews resolved; ESPN line has no tvg-id)", stamped)
	}
	got := string(out)
	if !contains(got, `#EXTINF:-1 tvg-genre="News" tvg-id="BBCNews.uk@SD"`) {
		t.Errorf("BBC line not stamped:\n%s", got)
	}
	if !contains(got, `#EXTINF:-1,ESPN`) {
		t.Errorf("ESPN line (no tvg-id) should be untouched:\n%s", got)
	}
}

func TestInjectReplacesExisting(t *testing.T) {
	m := Map{"X.us": Sports}
	in := []byte(`#EXTINF:-1 tvg-genre="Movies" tvg-id="X.us",X` + "\nhttps://x/s.m3u8\n")
	out, stamped := m.Inject(in)
	if stamped != 1 {
		t.Fatalf("stamped = %d, want 1", stamped)
	}
	got := string(out)
	if contains(got, `tvg-genre="Movies"`) {
		t.Errorf("stale genre not replaced:\n%s", got)
	}
	if !contains(got, `tvg-genre="Sports"`) {
		t.Errorf("new genre missing:\n%s", got)
	}
}

func TestEnrich(t *testing.T) {
	db := DB{
		"1TV.ge":   {Category: News, Country: "GE"},
		"ESPN.us":  {Category: Sports, Country: "US"},
		"NoCtry.xx": {Category: Movies, Country: ""}, // resolvable id but no country
	}
	in := []byte("#EXTM3U\n" +
		// category from group-title (General→Entertainment), country from DB
		`#EXTINF:-1 tvg-id="1TV.ge@SD" tvg-logo="l.png" group-title="General",1TV` + "\n" +
		"https://a/s.m3u8\n" +
		// feed suffix stripped; multi-category group-title; country from DB
		`#EXTINF:-1 tvg-id="ESPN.us" group-title="Sports;General",ESPN` + "\n" +
		"https://b/s.m3u8\n" +
		// no tvg-id → country falls back to Other, category from group-title
		`#EXTINF:-1 group-title="News",Mystery` + "\n" +
		"https://c/s.m3u8\n" +
		// resolvable but empty country → Other
		`#EXTINF:-1 tvg-id="NoCtry.xx" group-title="Movies",NC` + "\n" +
		"https://d/s.m3u8\n")

	out, n := db.Enrich(in)
	if n != 4 {
		t.Fatalf("processed = %d, want 4", n)
	}
	got := string(out)

	// 1TV: country GE, category Entertainment (from "General"), tvg-id/logo kept
	if !contains(got, `group-title="GE"`) || !contains(got, `tvg-genre="Entertainment"`) {
		t.Errorf("1TV not enriched as GE/Entertainment:\n%s", got)
	}
	if !contains(got, `tvg-id="1TV.ge@SD"`) || !contains(got, `tvg-logo="l.png"`) {
		t.Errorf("1TV lost tvg-id/tvg-logo:\n%s", got)
	}
	// ESPN: country US, category Sports (first specific slug)
	if !contains(got, `group-title="US"`) || !contains(got, `tvg-genre="Sports"`) {
		t.Errorf("ESPN not enriched as US/Sports:\n%s", got)
	}
	// Mystery: no id → Other, category News from group-title
	if !contains(got, `group-title="Other"`) || !contains(got, `tvg-genre="News"`) {
		t.Errorf("Mystery not grouped Other/News:\n%s", got)
	}
	// the original category-valued group-titles must not survive
	if contains(got, `group-title="General"`) || contains(got, `group-title="Sports;General"`) {
		t.Errorf("original group-title not rewritten:\n%s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
