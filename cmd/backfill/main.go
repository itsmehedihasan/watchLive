// Command backfill gets every header hint in a seed playlist represented in the
// catalog. M3U #EXTVLCOPT hints (http-user-agent / http-referrer) attach to a
// channel row keyed by exact stream URL, so a hint can only live where its
// channel does. This tool does two non-destructive things in one pass:
//
//   - stamps the header hints onto channels that already exist (by exact URL),
//     via store.BackfillHeaders; and
//   - imports the seed-only channels that carry a hint but have no catalog row
//     yet, via a filtered store.UpsertCatalog so the 12k existing rows are left
//     untouched (a full re-sync would flatten their logos/categories).
//
// It is fill-only and idempotent: re-running stamps the same rows and upserts
// the same channels by ID. Run with -dry-run first to see the counts.
package main

import (
	"flag"
	"log"
	"os"

	"watchlive/internal/playlist"
	"watchlive/internal/store"
)

func main() {
	log.SetFlags(0)
	dbPath := flag.String("db", "store/catalog.db", "path to the SQLite catalog")
	seedPath := flag.String("seed", "seed.m3u", "path to the seed .m3u carrying header hints")
	dryRun := flag.Bool("dry-run", false, "report what would change without writing")
	updateLinks := flag.Bool("update-links", false, "refresh stale stream URLs in place from the seed by exact host match (resets health on changed channels); does not stamp headers or import")
	syncLinks := flag.Bool("sync-links", false, "make every shared channel's servers match the seed exactly (links-only: keeps names/logos/categories/membership; resets health on changed channels)")
	flag.Parse()

	seed, err := os.ReadFile(*seedPath)
	if err != nil {
		log.Fatalf("read seed %s: %v", *seedPath, err)
	}
	chs := playlist.Parse(string(seed))

	// Channels in the seed that carry at least one header hint.
	var withHints []playlist.Channel
	for _, ch := range chs {
		if ch.UserAgent != "" || ch.Referer != "" {
			withHints = append(withHints, ch)
		}
	}

	// Open applies the schema migration that adds the http_user_agent /
	// http_referer columns to an older catalog, so this is safe on a stale DB.
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open catalog %s: %v", *dbPath, err)
	}
	defer st.Close()

	// -sync-links makes shared channels' servers match the seed exactly
	// (links-only; membership, names, logos, categories, headers preserved).
	if *syncLinks {
		n, err := st.SyncLinksFromSeed(chs, *dryRun)
		if err != nil {
			log.Fatalf("sync links: %v", err)
		}
		verb := "synced"
		if *dryRun {
			verb = "would sync"
		}
		log.Printf("%s links on %d channel(s) to match seed (health reset on changed)", verb, n)
		return
	}

	// -update-links is a separate, narrow mode: refresh stale stream URLs in
	// place by exact host match. It does not stamp headers or import channels.
	if *updateLinks {
		chCh, links, skipped, err := st.RefreshLinksByHost(chs, *dryRun)
		if err != nil {
			log.Fatalf("update links: %v", err)
		}
		verb := "updated"
		if *dryRun {
			verb = "would update"
		}
		log.Printf("%s %d channel(s), %d link(s) refreshed by host; %d ambiguous server(s) skipped",
			verb, chCh, links, skipped)
		return
	}

	existing, err := st.ListChannels()
	if err != nil {
		log.Fatalf("list channels: %v", err)
	}
	have := make(map[string]bool, len(existing))
	for _, ch := range existing {
		have[ch.ID] = true
	}

	// New hint channels (not yet in the catalog) get imported so their hints have
	// a row to live on; existing ones are stamped by BackfillHeaders below.
	var toImport []playlist.Channel
	stampable := 0
	for _, ch := range withHints {
		if have[ch.ID] {
			stampable++
		} else {
			toImport = append(toImport, ch)
		}
	}

	log.Printf("seed: %d channels, %d carry a header hint", len(chs), len(withHints))
	log.Printf("catalog: %d channels", len(existing))
	log.Printf("plan: import %d new hint channel(s), stamp %d existing", len(toImport), stampable)

	if *dryRun {
		log.Printf("dry-run: no changes written")
		return
	}

	// Import only the new hint channels. Passing this subset to UpsertCatalog
	// leaves every existing row untouched (it only updates rows present in the
	// slice), so logos/categories of the 12k existing channels are preserved.
	// The INSERT path writes http_user_agent / http_referer, so the imports
	// arrive with their hints already set.
	ins, _, _, err := st.UpsertCatalog(toImport)
	if err != nil {
		log.Fatalf("import new hint channels: %v", err)
	}

	// Stamp the existing channels by exact URL match. Fill-only and header-only;
	// covers a channel if any of its server URLs is in the catalog.
	filled, err := st.BackfillHeaders(chs)
	if err != nil {
		log.Fatalf("backfill headers: %v", err)
	}

	log.Printf("done: imported %d, stamped %d", ins, filled)
}
