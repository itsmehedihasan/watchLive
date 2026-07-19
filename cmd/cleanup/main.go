// Command cleanup is a one-off catalog maintenance tool: it deletes every
// channel the user has no stake in — not favourited and not manual
// (is_favourite=0 AND is_manual=0). This is the unconditional form of the
// PruneOrphans predicate the server uses, applied to the whole catalog instead
// of only channels missing from the latest feed.
//
// Kept: favourited channels (any source), and every manual channel — hand-added
// entries, imported .m3u channels, and Xtream-imported channels (all is_manual=1).
//
// It is a deliberate one-time operation, not a server feature: there is no
// endpoint or UI for it. Run with -dry-run first to see the count.
package main

import (
	"flag"
	"log"

	"watchlive/internal/store"
)

func main() {
	log.SetFlags(0)
	dbPath := flag.String("db", "store/catalog.db", "path to the SQLite catalog")
	dryRun := flag.Bool("dry-run", false, "report how many channels would be deleted without writing")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open catalog %s: %v", *dbPath, err)
	}
	defer st.Close()

	before, err := st.Count()
	if err != nil {
		log.Fatalf("count: %v", err)
	}

	if *dryRun {
		n, err := st.CountUnkept()
		if err != nil {
			log.Fatalf("count unkept: %v", err)
		}
		log.Printf("dry-run: would delete %d of %d channel(s) (non-favourite, non-manual); %d would remain",
			n, before, before-n)
		return
	}

	deleted, err := st.PruneUnkept()
	if err != nil {
		log.Fatalf("cleanup: %v", err)
	}
	log.Printf("cleanup: deleted %d non-favourite, non-manual channel(s); %d remain", deleted, before-deleted)
}
