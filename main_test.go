package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"html/template"

	"watchlive/internal/health"
	"watchlive/internal/keystore"
	"watchlive/internal/playlist"
	"watchlive/internal/proxy"
	"watchlive/internal/recorder"
	"watchlive/internal/store"
	"watchlive/internal/viewers"
)

// testMux wires newMux against a real temp SQLite store + keystore so the HTTP
// handlers are exercised end-to-end (decode → store mutation → payload rebuild).
func testMux(t *testing.T) (*http.ServeMux, *channelStore, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ks, err := keystore.Open(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatalf("keystore.Open: %v", err)
	}
	cs := newChannelStore(st, "", false)
	tmpl := template.Must(template.New("index").Parse("ok"))
	mux := newMux(
		proxy.New(1<<20), viewers.NewStore(), fstest.MapFS{}, cs, st, ks,
		recorder.New("", dir), health.New(), tmpl,
	)
	return mux, cs, st
}

func do(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// findChannel returns the catalog channel with the given id, failing if absent.
func findChannel(t *testing.T, st *store.Store, id string) store.Channel {
	t.Helper()
	chs, err := st.ListChannels()
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	for _, c := range chs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("channel %q not found", id)
	return store.Channel{}
}

// seedFeed inserts a non-manual (feed) channel with a known id and URL.
func seedFeed(t *testing.T, st *store.Store, id, name, url string) {
	t.Helper()
	_, _, _, err := st.UpsertCatalog([]playlist.Channel{{
		ID: id, Name: name, Group: "BD", Type: "News",
		Servers: []playlist.Server{{URL: url, Label: "720p"}},
	}})
	if err != nil {
		t.Fatalf("seed feed: %v", err)
	}
}

func TestFavouriteHandler(t *testing.T) {
	mux, _, st := testMux(t)
	ch, err := st.AddManual("Fav Me", "https://cdn.example.com/fav.m3u8", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, mux, http.MethodPost, "/api/favourite", `{"id":"`+ch.ID+`","on":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("favourite: got %d, body %q", rec.Code, rec.Body.String())
	}
	// AddManual already favourites; flip it off and confirm it persists.
	if rec := do(t, mux, http.MethodPost, "/api/favourite", `{"id":"`+ch.ID+`","on":false}`); rec.Code != http.StatusOK {
		t.Fatalf("favourite off: %d", rec.Code)
	}
	if findChannel(t, st, ch.ID).IsFavourite {
		t.Error("favourite flag not cleared in store")
	}

	if rec := do(t, mux, http.MethodPost, "/api/favourite", `{"id":"nope","on":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("missing id: got %d, want 404", rec.Code)
	}
	if rec := do(t, mux, http.MethodPost, "/api/favourite", `{bad json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: got %d, want 400", rec.Code)
	}
}

func TestChannelsAddHandler(t *testing.T) {
	mux, _, _ := testMux(t)

	rec := do(t, mux, http.MethodPost, "/api/channels/add",
		`{"Name":"My Chan","URL":"https://cdn.example.com/my.m3u8"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: got %d, body %q", rec.Code, rec.Body.String())
	}
	var ch store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode added channel: %v", err)
	}
	if !strings.HasPrefix(ch.ID, "manual:") {
		t.Errorf("manual id prefix: got %q", ch.ID)
	}
	if ch.Group != "Custom" || !ch.IsFavourite {
		t.Errorf("manual defaults wrong: group=%q fav=%v", ch.Group, ch.IsFavourite)
	}

	// Optional Referer/User-Agent are persisted (CDN-gated streams need them).
	rec = do(t, mux, http.MethodPost, "/api/channels/add",
		`{"Name":"Gated","URL":"https://cdn9.example.com/g.m3u8","Referer":"https://exposestrat.com/","UserAgent":"CustomUA/1.0"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add gated: got %d, body %q", rec.Code, rec.Body.String())
	}
	var gated store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &gated); err != nil {
		t.Fatalf("decode gated channel: %v", err)
	}
	if gated.Referer != "https://exposestrat.com/" || gated.UserAgent != "CustomUA/1.0" {
		t.Errorf("headers not persisted: ref=%q ua=%q", gated.Referer, gated.UserAgent)
	}

	// Missing/invalid URL → 400.
	if rec := do(t, mux, http.MethodPost, "/api/channels/add", `{"Name":"x","URL":"not-a-url"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad url: got %d, want 400", rec.Code)
	}
	if rec := do(t, mux, http.MethodPost, "/api/channels/add", `{bad`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: got %d, want 400", rec.Code)
	}
}

func TestChannelsDeleteHandler(t *testing.T) {
	mux, _, st := testMux(t)
	ch, _ := st.AddManual("Temp", "https://cdn.example.com/t.m3u8", nil, "", "")

	if rec := do(t, mux, http.MethodDelete, "/api/channels/add", `{"ID":"`+ch.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("delete manual: got %d", rec.Code)
	}
	// Second delete → 404 (already gone).
	if rec := do(t, mux, http.MethodDelete, "/api/channels/add", `{"ID":"`+ch.ID+`"}`); rec.Code != http.StatusNotFound {
		t.Errorf("re-delete: got %d, want 404", rec.Code)
	}
	// Deleting a feed (non-manual) channel → 409.
	seedFeed(t, st, "tvg:feed.delete", "Feed", "https://cdn.example.com/feed.m3u8")
	if rec := do(t, mux, http.MethodDelete, "/api/channels/add", `{"ID":"tvg:feed.delete"}`); rec.Code != http.StatusConflict {
		t.Errorf("delete feed: got %d, want 409", rec.Code)
	}
}

func TestImportCheckAndSave(t *testing.T) {
	mux, _, st := testMux(t)
	seedFeed(t, st, "tvg:exists", "Already Here", "https://cdn.example.com/dup.m3u8")

	body := `{"entries":[
		{"name":"New One","url":"https://cdn.example.com/new1.m3u8"},
		{"name":"Dup Link","url":"https://cdn.example.com/dup.m3u8"}
	]}`

	rec := do(t, mux, http.MethodPost, "/api/import/check", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("check: got %d, body %q", rec.Code, rec.Body.String())
	}
	var checked struct {
		New        []store.ImportEntry `json:"new"`
		Duplicates []struct {
			ExistingID string `json:"existingId"`
		} `json:"duplicates"`
	}
	json.Unmarshal(rec.Body.Bytes(), &checked)
	if len(checked.New) != 1 || len(checked.Duplicates) != 1 {
		t.Fatalf("check split wrong: new=%d dups=%d", len(checked.New), len(checked.Duplicates))
	}
	if checked.Duplicates[0].ExistingID != "tvg:exists" {
		t.Errorf("dup existingId: got %q", checked.Duplicates[0].ExistingID)
	}

	before, _ := st.Count()
	if rec := do(t, mux, http.MethodPost, "/api/import/save", body); rec.Code != http.StatusOK {
		t.Fatalf("save: got %d", rec.Code)
	}
	after, _ := st.Count()
	if after-before != 1 {
		t.Errorf("save added %d channels, want 1 (dup link skipped)", after-before)
	}
}

func TestChannelsETagAndGzip(t *testing.T) {
	mux, _, _ := testMux(t)
	// Add via the handler so the payload is rebuilt to include the channel.
	if rec := do(t, mux, http.MethodPost, "/api/channels/add",
		`{"Name":"Ch","URL":"https://cdn.example.com/c.m3u8"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed add: %d", rec.Code)
	}

	rec := do(t, mux, http.MethodGet, "/api/channels", "")
	etag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || etag == "" {
		t.Fatalf("channels: code=%d etag=%q", rec.Code, etag)
	}
	if !strings.Contains(rec.Body.String(), `"is_favourite"`) || !strings.Contains(rec.Body.String(), `"is_working"`) {
		t.Errorf("channels payload missing is_favourite/is_working fields")
	}

	// If-None-Match → 304.
	r := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	r.Header.Set("If-None-Match", etag)
	rec304 := httptest.NewRecorder()
	mux.ServeHTTP(rec304, r)
	if rec304.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: got %d, want 304", rec304.Code)
	}

	// gzip when requested.
	rg := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	rg.Header.Set("Accept-Encoding", "gzip")
	recg := httptest.NewRecorder()
	mux.ServeHTTP(recg, rg)
	if recg.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("gzip not applied: %q", recg.Header().Get("Content-Encoding"))
	}
}

func TestHandlersRejectMalformedJSON(t *testing.T) {
	mux, _, _ := testMux(t)
	for _, ep := range []string{
		"/api/favourite",
		"/api/channels/add",
		"/api/channels/update",
		"/api/import/check",
		"/api/import/save",
	} {
		if rec := do(t, mux, http.MethodPost, ep, `{not valid`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with bad json: got %d, want 400", ep, rec.Code)
		}
	}
}

func TestUpstreamHeadersFromCatalog(t *testing.T) {
	chs := []store.Channel{
		{ID: "a", UserAgent: "UA1", Servers: []playlist.Server{
			{URL: "https://cdn-a.example.com/x.m3u8"},
			{URL: "https://cdn-a2.example.com/y.m3u8"}, // a channel's every host inherits its headers
		}},
		{ID: "b", Referer: "https://site.example/p", Servers: []playlist.Server{
			{URL: "https://cdn-b.example.com:8080/z.m3u8"}, // host includes the port
		}},
		{ID: "c", Servers: []playlist.Server{{URL: "https://nohints.example.com/q.m3u8"}}}, // no hints → skipped
	}
	m := upstreamHeadersFromCatalog(chs)

	if len(m) != 3 {
		t.Fatalf("want 3 host entries, got %d: %+v", len(m), m)
	}
	if m["cdn-a.example.com"].UserAgent != "UA1" || m["cdn-a2.example.com"].UserAgent != "UA1" {
		t.Errorf("UA not mapped to both hosts of channel a: %+v", m)
	}
	if m["cdn-b.example.com:8080"].Referer != "https://site.example/p" {
		t.Errorf("referer (with port host) not mapped: %+v", m["cdn-b.example.com:8080"])
	}
	if _, ok := m["nohints.example.com"]; ok {
		t.Error("channel without hints should not be mapped")
	}
}

func TestSyncDisabled(t *testing.T) {
	mux, _, _ := testMux(t)
	if rec := do(t, mux, http.MethodPost, "/api/sync", ""); rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/sync: got %d, want 403 (sync disabled)", rec.Code)
	}
}
