// Command import fetches all streams/*.m3u files from iptv-org/iptv and merges
// new stream URLs into list.sync.m3u, deduplicating against existing content.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"watchlive/internal/genre"
)

func main() {
	outPath := flag.String("out", "list.sync.m3u", "File to append new links to")
	concurrency := flag.Int("concurrency", 12, "Parallel fetches")
	countryStr := flag.String("country", "", "Comma-separated country codes to limit scope (e.g. bd,us,gb)")
	enrichOnly := flag.Bool("enrich", false, "Only stamp tvg-genre on an existing -out file (no stream download)")
	flag.Parse()

	// Resolve output path relative to cwd if not absolute
	if !filepath.IsAbs(*outPath) {
		abs, err := filepath.Abs(*outPath)
		if err != nil {
			log.Fatalf("resolve output path: %v", err)
		}
		*outPath = abs
	}

	if *enrichOnly {
		enrichFile(*outPath)
		return
	}

	// Read existing URLs from -out file to build the dedup set
	seenURLs := make(map[string]bool)
	if data, err := os.ReadFile(*outPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
				seenURLs[line] = true
			}
		}
	} else if !os.IsNotExist(err) {
		log.Fatalf("read %s: %v", *outPath, err)
	}

	log.Printf("loaded %d existing URLs from %s", len(seenURLs), *outPath)

	// Parse -country filter
	countryFilter := make(map[string]bool)
	if *countryStr != "" {
		for _, cc := range strings.Split(*countryStr, ",") {
			cc = strings.TrimSpace(strings.ToLower(cc))
			if cc != "" {
				countryFilter[cc] = true
			}
		}
	}

	// Fetch list of streams/*.m3u files from GitHub API
	files, err := listStreamFiles(countryFilter)
	if err != nil {
		log.Fatalf("list streams: %v", err)
	}
	log.Printf("found %d .m3u files in streams/", len(files))

	// Fan-out fetch each file
	type fetchResult struct {
		cc      string // country code (filename without .m3u)
		entries []entry
		err     error
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, *concurrency)
	results := make(chan fetchResult, len(files))

	for _, file := range files {
		wg.Add(1)
		go func(f githubFile) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			cc := strings.TrimSuffix(strings.ToLower(f.Name), ".m3u")
			entries, err := fetchAndParseFile(f.DownloadURL)
			results <- fetchResult{cc, entries, err}
		}(file)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect all fetched entries
	var newEntries []entry
	skippedDupes := 0
	filesProcessed := 0
	for res := range results {
		if res.err != nil {
			log.Printf("fetch %s: %v (skipped)", res.cc, res.err)
			continue
		}
		filesProcessed++
		for _, e := range res.entries {
			e.cc = strings.ToUpper(res.cc) // uppercase country code for group-title
			url := strings.TrimSpace(e.url)
			if seenURLs[url] {
				skippedDupes++
				continue
			}
			seenURLs[url] = true
			newEntries = append(newEntries, e)
		}
	}

	if len(newEntries) == 0 {
		log.Printf("scanned %d files, %d new links added, %d duplicates skipped", filesProcessed, len(newEntries), skippedDupes)
		return
	}

	// Read existing content and append new entries
	existingContent := ""
	if data, err := os.ReadFile(*outPath); err == nil {
		existingContent = string(data)
		if !strings.HasSuffix(existingContent, "\n") {
			existingContent += "\n"
		}
	} else if !os.IsNotExist(err) {
		log.Fatalf("read %s: %v", *outPath, err)
	}

	// Build new entries block, preserving tvg-id/tvg-logo so the genre pass
	// (and the UI) have something to work with.
	var newBlock strings.Builder
	for _, e := range newEntries {
		newBlock.WriteString(fmt.Sprintf("#EXTINF:-1 tvg-id=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n", e.id, e.logo, e.cc, e.name))
		newBlock.WriteString(e.url)
		newBlock.WriteString("\n")
	}

	content := existingContent + newBlock.String()

	// Stamp tvg-genre on every entry (existing + new) from iptv-org's category
	// database. Best-effort: skip enrichment if the database is unreachable.
	if gm, err := genre.Load(); err != nil {
		log.Printf("genre enrich skipped: %v", err)
	} else {
		var stamped int
		var enriched []byte
		enriched, stamped = gm.Inject([]byte(content))
		content = string(enriched)
		log.Printf("genre: tagged %d entries", stamped)
	}

	// Atomic write: tmp + rename
	tmp := *outPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		log.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, *outPath); err != nil {
		os.Remove(tmp)
		log.Fatalf("rename %s to %s: %v", tmp, *outPath, err)
	}

	log.Printf("scanned %d files, %d existing URLs, %d new links added, %d duplicates skipped",
		filesProcessed, len(seenURLs)-len(newEntries), len(newEntries), skippedDupes)
}

// enrichFile stamps tvg-genre onto every entry of an existing playlist using
// iptv-org's category database, without downloading any streams. Use it to
// categorize a list that was built before genre support existed.
func enrichFile(outPath string) {
	data, err := os.ReadFile(outPath)
	if err != nil {
		log.Fatalf("read %s: %v", outPath, err)
	}
	gm, err := genre.Load()
	if err != nil {
		log.Fatalf("load genre database: %v", err)
	}
	enriched, stamped := gm.Inject(data)

	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, enriched, 0o644); err != nil {
		log.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		log.Fatalf("rename %s to %s: %v", tmp, outPath, err)
	}
	log.Printf("enriched %s: tagged %d entries", outPath, stamped)
}

type githubFile struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
}

// listStreamFiles fetches the GitHub API listing for streams/ directory.
// If countryFilter is non-empty, only returns files matching those country codes.
func listStreamFiles(countryFilter map[string]bool) ([]githubFile, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	url := "https://api.github.com/repos/iptv-org/iptv/contents/streams"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var items []githubFile
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	var filtered []githubFile
	for _, item := range items {
		if !strings.HasSuffix(item.Name, ".m3u") {
			continue
		}
		cc := strings.TrimSuffix(strings.ToLower(item.Name), ".m3u")
		if len(countryFilter) > 0 && !countryFilter[cc] {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

type entry struct {
	cc   string // country code (uppercase) — set by caller
	name string // EXTINF display name (with quality/status suffixes)
	url  string // stream URL
	id   string // tvg-id (preserved for genre lookup)
	logo string // tvg-logo
}

// fetchAndParseFile fetches and parses a single streams/*.m3u file.
// Returns (entries, error); on error, entries is nil.
func fetchAndParseFile(downloadURL string) ([]entry, error) {
	client := &http.Client{Timeout: 1 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var entries []entry
	scanner := bufio.NewScanner(resp.Body)
	var extinf string // Previous EXTINF line

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			extinf = line
			continue
		}
		// If this line looks like a URL and we have a preceding EXTINF, record the entry
		if (strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://")) && extinf != "" {
			// Extract channel name + attributes from the EXTINF line.
			// Format: #EXTINF:-1 tvg-id="..." tvg-logo="...",Channel Name (quality)
			name := extractChannelName(extinf)
			entries = append(entries, entry{
				name: name,
				url:  line,
				id:   extractAttr(extinf, "tvg-id"),
				logo: extractAttr(extinf, "tvg-logo"),
			})
			extinf = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// extractAttr returns the value of a quoted attribute (e.g. tvg-id="x") from an
// EXTINF line, or "" if absent.
func extractAttr(extinf, attr string) string {
	prefix := attr + `="`
	i := strings.Index(extinf, prefix)
	if i < 0 {
		return ""
	}
	rest := extinf[i+len(prefix):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// extractChannelName pulls the display name from an EXTINF line.
// EXTINF format: #EXTINF:-1 tvg-id="...",Channel Name (quality)
// Returns the text after the comma (the channel name with suffixes).
func extractChannelName(extinf string) string {
	// Find the last comma in the line — everything after it is the name
	idx := strings.LastIndex(extinf, ",")
	if idx == -1 {
		return extinf // Fallback: return as-is if no comma
	}
	return strings.TrimSpace(extinf[idx+1:])
}
