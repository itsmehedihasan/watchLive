package main

import "testing"

func TestMergeNewEntries(t *testing.T) {
	existing := []byte("#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"BD\",Channel A\n" +
		"https://a.example/stream.m3u8\n" +
		"#EXTINF:-1 group-title=\"BD\",Channel B\n" +
		"https://b.example/stream.m3u8\n")

	upstream := []byte("#EXTM3U\n" +
		// duplicate URL — must be skipped even though the name differs
		"#EXTINF:-1 group-title=\"US\",Channel A renamed\n" +
		"https://a.example/stream.m3u8\n" +
		// new entry with an extra directive line — both lines must be kept
		"#EXTINF:-1 group-title=\"US\",Channel C\n" +
		"#EXTVLCOPT:http-referrer=https://x\n" +
		"https://c.example/stream.m3u8\n")

	merged, added := mergeNewEntries(existing, upstream)
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	got := string(merged)
	want := string(existing) +
		"#EXTINF:-1 group-title=\"US\",Channel C\n" +
		"#EXTVLCOPT:http-referrer=https://x\n" +
		"https://c.example/stream.m3u8\n"
	if got != want {
		t.Fatalf("merged mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestMergeNewEntriesNoNew(t *testing.T) {
	existing := []byte("#EXTM3U\n#EXTINF:-1,A\nhttps://a.example/s.m3u8\n")
	// Upstream is a strict subset — nothing should change.
	upstream := []byte("#EXTM3U\n#EXTINF:-1,A dup\nhttps://a.example/s.m3u8\n")

	merged, added := mergeNewEntries(existing, upstream)
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if string(merged) != string(existing) {
		t.Fatalf("file changed despite no new entries: %q", string(merged))
	}
}

func TestMergeNewEntriesAppendsNewline(t *testing.T) {
	// Existing file lacks a trailing newline — merge must not glue the first
	// appended line onto the last existing URL.
	existing := []byte("#EXTM3U\n#EXTINF:-1,A\nhttps://a.example/s.m3u8")
	upstream := []byte("#EXTM3U\n#EXTINF:-1,B\nhttps://b.example/s.m3u8\n")

	merged, added := mergeNewEntries(existing, upstream)
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	want := "#EXTM3U\n#EXTINF:-1,A\nhttps://a.example/s.m3u8\n" +
		"#EXTINF:-1,B\nhttps://b.example/s.m3u8\n"
	if string(merged) != want {
		t.Fatalf("merged mismatch:\n got: %q\nwant: %q", string(merged), want)
	}
}
