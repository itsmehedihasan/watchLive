package xtream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubPanel returns a test server standing in for player_api.php. login is the
// body served for a bare login request; streams is served for
// action=get_live_streams. An empty body for either is served verbatim (used to
// simulate malformed/HTML responses).
func stubPanel(t *testing.T, login, streams string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player_api.php" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("action") == "get_live_streams" {
			w.Write([]byte(streams))
			return
		}
		w.Write([]byte(login))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const okLogin = `{"user_info":{"username":"u","auth":1,"status":"Active"},"server_info":{"url":"host","port":"80"}}`

func TestLoginHappyPath(t *testing.T) {
	srv := stubPanel(t, okLogin, "[]")
	ui, si, err := Login(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ui.Auth != 1 {
		t.Errorf("auth = %d, want 1", ui.Auth)
	}
	if ui.Username != "u" {
		t.Errorf("username = %q, want u", ui.Username)
	}
	if si.Port != "80" {
		t.Errorf("server port = %q, want 80", si.Port)
	}
}

func TestLoginAuthZero(t *testing.T) {
	srv := stubPanel(t, `{"user_info":{"auth":0}}`, "[]")
	if _, _, err := Login(srv.URL, "u", "bad"); err == nil {
		t.Fatal("Login with auth:0 should error")
	}
}

// Some panels quote the auth flag as a string; it must still be honoured.
func TestLoginAuthStringOne(t *testing.T) {
	srv := stubPanel(t, `{"user_info":{"username":"u","auth":"1"}}`, "[]")
	if _, _, err := Login(srv.URL, "u", "p"); err != nil {
		t.Fatalf("Login with auth:\"1\": %v", err)
	}
}

func TestLoginMalformedJSON(t *testing.T) {
	srv := stubPanel(t, `<html>Access denied</html>`, "[]")
	if _, _, err := Login(srv.URL, "u", "p"); err == nil {
		t.Fatal("Login with non-JSON body should error")
	}
}

func TestLoginNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, _, err := Login(srv.URL, "u", "p"); err == nil {
		t.Fatal("Login with 403 should error")
	}
}

func TestLiveStreamsHappyPath(t *testing.T) {
	streams := `[
		{"stream_id":101,"name":"Alpha","stream_icon":"http://l/a.png","category_id":"1","container_extension":"m3u8"},
		{"stream_id":"202","name":"Beta"}
	]`
	srv := stubPanel(t, okLogin, streams)
	got, err := LiveStreams(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("LiveStreams: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d streams, want 2", len(got))
	}
	if got[0].StreamID != 101 || got[0].Name != "Alpha" || got[0].Extension != "m3u8" {
		t.Errorf("stream[0] = %+v", got[0])
	}
	// stream_id sent as a quoted string must decode to the int.
	if got[1].StreamID != 202 {
		t.Errorf("stream[1].StreamID = %d, want 202", got[1].StreamID)
	}
	// Missing extension stays empty (caller defaults it).
	if got[1].Extension != "" {
		t.Errorf("stream[1].Extension = %q, want empty", got[1].Extension)
	}
}

func TestLiveStreamsAuthFails(t *testing.T) {
	srv := stubPanel(t, `{"user_info":{"auth":0}}`, `[{"stream_id":1,"name":"X"}]`)
	if _, err := LiveStreams(srv.URL, "u", "bad"); err == nil {
		t.Fatal("LiveStreams should surface auth failure before listing")
	}
}

func TestLiveStreamsMalformed(t *testing.T) {
	srv := stubPanel(t, okLogin, `not json`)
	if _, err := LiveStreams(srv.URL, "u", "p"); err == nil {
		t.Fatal("LiveStreams with malformed body should error")
	}
}

func TestStreamURL(t *testing.T) {
	got := StreamURL("http://panel.example:8080/", "user", "pass", 42, "m3u8")
	want := "http://panel.example:8080/live/user/pass/42.m3u8"
	if got != want {
		t.Errorf("StreamURL = %q, want %q", got, want)
	}
	// Empty extension defaults to ts.
	if got := StreamURL("http://p", "u", "p", 7, ""); got != "http://p/live/u/p/7.ts" {
		t.Errorf("StreamURL default ext = %q", got)
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"http://p.example/":   "http://p.example",
		"  http://p.example ": "http://p.example",
		"https://p:8443":      "https://p:8443",
	}
	for in, want := range cases {
		if got := NormalizeServer(in); got != want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}

// The player_api URL must carry credentials and (when set) the action.
func TestPlayerAPIQuery(t *testing.T) {
	u := playerAPI("http://p/", "u", "p", nil)
	if !strings.Contains(u, "username=u") || !strings.Contains(u, "password=p") {
		t.Errorf("login URL missing creds: %s", u)
	}
	if !strings.HasPrefix(u, "http://p/player_api.php?") {
		t.Errorf("unexpected login URL: %s", u)
	}
}
