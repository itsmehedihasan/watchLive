// Command export dumps the SQLite catalog to JSON: a column-faithful copy of
// every channels row plus the meta key/value table. It is read-only — useful for
// backups, inspection, or migrating the catalog elsewhere. The servers and
// clear_keys columns (stored as JSON text) are emitted as nested JSON, not as
// escaped strings, so the output is directly usable.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

// channel mirrors a channels row. Nullable columns use pointers so a missing
// verdict/timestamp serializes as JSON null rather than a zero value.
type channel struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Logo          string          `json:"logo"`
	Group         string          `json:"grp"`
	Type          string          `json:"typ"`
	Servers       json.RawMessage `json:"servers"`
	ClearKeys     json.RawMessage `json:"clear_keys,omitempty"`
	UserAgent     string          `json:"http_user_agent,omitempty"`
	Referer       string          `json:"http_referer,omitempty"`
	IsWorking     *bool           `json:"is_working"`
	LastCheckedAt *int64          `json:"last_checked_at"`
	IsFavourite   bool            `json:"is_favourite"`
	IsManual      bool            `json:"is_manual"`
	SortName      string          `json:"sort_name"`
}

type dump struct {
	Channels []channel         `json:"channels"`
	Meta     map[string]string `json:"meta"`
}

func main() {
	log.SetFlags(0)
	dbPath := flag.String("db", "store/catalog.db", "path to the SQLite catalog")
	outPath := flag.String("out", "catalog.json", "output JSON file (use - for stdout)")
	pretty := flag.Bool("pretty", true, "indent the JSON output")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer db.Close()

	out := dump{Meta: map[string]string{}}

	rows, err := db.Query(`
		SELECT id, name, logo, grp, typ, servers, clear_keys, http_user_agent,
		       http_referer, is_working, last_checked_at, is_favourite, is_manual, sort_name
		FROM channels ORDER BY sort_name, name`)
	if err != nil {
		log.Fatalf("query channels: %v", err)
	}
	for rows.Next() {
		var (
			ch        channel
			serversJS string
			clearJS   string
			working   sql.NullInt64
			checked   sql.NullInt64
			fav, man  int
		)
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Logo, &ch.Group, &ch.Type, &serversJS,
			&clearJS, &ch.UserAgent, &ch.Referer, &working, &checked, &fav, &man, &ch.SortName); err != nil {
			rows.Close()
			log.Fatalf("scan channel: %v", err)
		}
		ch.Servers = rawOrDefault(serversJS, "[]")
		if clearJS != "" {
			ch.ClearKeys = json.RawMessage(clearJS)
		}
		if working.Valid {
			b := working.Int64 != 0
			ch.IsWorking = &b
		}
		if checked.Valid {
			v := checked.Int64
			ch.LastCheckedAt = &v
		}
		ch.IsFavourite = fav != 0
		ch.IsManual = man != 0
		out.Channels = append(out.Channels, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate channels: %v", err)
	}

	mrows, err := db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		log.Fatalf("query meta: %v", err)
	}
	for mrows.Next() {
		var k, v string
		if err := mrows.Scan(&k, &v); err != nil {
			mrows.Close()
			log.Fatalf("scan meta: %v", err)
		}
		out.Meta[k] = v
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		log.Fatalf("iterate meta: %v", err)
	}

	var data []byte
	if *pretty {
		data, err = json.MarshalIndent(out, "", "  ")
	} else {
		data, err = json.Marshal(out)
	}
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	if *outPath == "-" {
		os.Stdout.Write(data)
		os.Stdout.Write([]byte("\n"))
	} else if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		log.Fatalf("write %s: %v", *outPath, err)
	}
	log.Printf("exported %d channel(s), %d meta key(s) to %s", len(out.Channels), len(out.Meta), *outPath)
}

// rawOrDefault returns s as raw JSON, or def when s is blank, so an empty
// servers column still emits a valid [] rather than null.
func rawOrDefault(s, def string) json.RawMessage {
	if s == "" {
		return json.RawMessage(def)
	}
	return json.RawMessage(s)
}
