// Package xtream is a thin client for the Xtream Codes player_api.php protocol,
// the JSON API served by most IPTV panels (XUI.one and compatibles). It speaks
// only the small slice this app needs: authenticating a set of credentials and
// listing a panel's live channels.
//
// VOD (movies/series), EPG, catch-up/archive and the various stream-type knobs
// are deliberately out of scope — only the live-channel list is fetched, and it
// is turned into ordinary catalog channels by the caller.
//
// Panels vary wildly in which fields they populate and how they type them, so
// decoding is tolerant: missing fields decode to zero values rather than
// erroring, and a panel that answers with an HTML error page (non-JSON body) or
// with auth==0 is reported as an authentication/connectivity error, never a
// panic.
package xtream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrAuth is returned when the panel is reachable but rejects the credentials
// (user_info.auth == 0) or answers with a body we can't read as an Xtream
// login response.
var ErrAuth = errors.New("xtream: authentication failed")

// httpClient bounds every call: panels are often slow or half-dead, and a
// hung request would otherwise block the import handler indefinitely.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// UserInfo mirrors the user_info block of a player_api.php login response. Only
// the fields this app acts on are decoded; Auth drives the ErrAuth decision.
type UserInfo struct {
	Username string `json:"username"`
	// Auth is the panel's own verdict: 1 = credentials accepted, 0 = rejected.
	// Decoded via flexInt because panels send it as a number or a string.
	Auth   flexInt `json:"auth"`
	Status string  `json:"status"`
}

// ServerInfo mirrors the server_info block. Kept minimal; callers rarely need
// more than confirmation the login round-tripped.
type ServerInfo struct {
	URL    string `json:"url"`
	Port   string `json:"port"`
	HTTPS  string `json:"https_port"`
	Server string `json:"server_protocol"`
}

// loginResponse is the top-level player_api.php login envelope.
type loginResponse struct {
	UserInfo   UserInfo   `json:"user_info"`
	ServerInfo ServerInfo `json:"server_info"`
}

// Stream is one live channel as returned by get_live_streams. Fields absent
// from a given panel decode to their zero value.
type Stream struct {
	StreamID   int    `json:"stream_id"`
	Name       string `json:"name"`
	Icon       string `json:"stream_icon"`
	CategoryID string `json:"category_id"`
	// Extension the panel serves this stream under (e.g. "ts", "m3u8"); may be
	// empty, in which case the caller falls back to the "ts" default.
	Extension string `json:"container_extension"`
}

// flexInt decodes a JSON value that a panel might send as either a number or a
// quoted string (Xtream panels are inconsistent about this for ids and flags).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// A non-numeric string (rare) is treated as zero rather than failing the
		// whole decode — tolerance is the point of this type.
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}

// flexStreamID lets a stream_id arrive as a number or a quoted string.
type flexStreamID int

func (f *flexStreamID) UnmarshalJSON(b []byte) error {
	var n flexInt
	if err := n.UnmarshalJSON(b); err != nil {
		return err
	}
	*f = flexStreamID(n)
	return nil
}

// rawStream is the wire shape; StreamID uses flexStreamID for panel tolerance,
// then is copied into the exported Stream.
type rawStream struct {
	StreamID   flexStreamID `json:"stream_id"`
	Name       string       `json:"name"`
	Icon       string       `json:"stream_icon"`
	CategoryID string       `json:"category_id"`
	Extension  string       `json:"container_extension"`
}

// Category is one live-stream category as returned by get_live_categories.
// ID is decoded via flexString so a numeric or quoted id both land as a string
// (it is only ever matched against Stream.CategoryID, itself a string).
type Category struct {
	ID   string `json:"category_id"`
	Name string `json:"category_name"`
}

// rawCategory is the wire shape; category_id may arrive as a number or a quoted
// string, so it is decoded tolerantly then copied into the exported Category.
type rawCategory struct {
	ID   flexString `json:"category_id"`
	Name string     `json:"category_name"`
}

// flexString decodes a JSON value that a panel might send as either a number or
// a quoted string into its string form.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	*f = flexString(strings.Trim(string(b), `"`))
	return nil
}

// NormalizeServer trims a trailing slash and surrounding whitespace. It does NOT
// add or guess a scheme or port — the caller is required to supply an explicit
// http:// or https:// (and a port when non-default). Returns the cleaned value.
func NormalizeServer(server string) string {
	return strings.TrimRight(strings.TrimSpace(server), "/")
}

// playerAPI builds a player_api.php URL for server with the given query params.
func playerAPI(server, username, password string, extra url.Values) string {
	q := url.Values{}
	q.Set("username", username)
	q.Set("password", password)
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return NormalizeServer(server) + "/player_api.php?" + q.Encode()
}

// Login authenticates credentials against a panel. A decodable response whose
// user_info.auth != 1, a non-2xx status, or a body that isn't the expected JSON
// are all reported as ErrAuth (wrapped) rather than panicking, so a bad
// server/username/password can never take down the caller.
func Login(server, username, password string) (UserInfo, ServerInfo, error) {
	body, status, err := get(playerAPI(server, username, password, nil))
	if err != nil {
		return UserInfo{}, ServerInfo{}, fmt.Errorf("xtream: login: %w", err)
	}
	if status < 200 || status >= 300 {
		return UserInfo{}, ServerInfo{}, fmt.Errorf("%w: panel returned status %d", ErrAuth, status)
	}
	var lr loginResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return UserInfo{}, ServerInfo{}, fmt.Errorf("%w: unexpected response body", ErrAuth)
	}
	if lr.UserInfo.Auth != 1 {
		return UserInfo{}, ServerInfo{}, ErrAuth
	}
	return lr.UserInfo, lr.ServerInfo, nil
}

// LiveStreams authenticates and returns the panel's live-channel list. It
// verifies auth first (so bad credentials surface as ErrAuth, not an empty
// list), then fetches get_live_streams. Missing/extra fields are tolerated.
func LiveStreams(server, username, password string) ([]Stream, error) {
	if _, _, err := Login(server, username, password); err != nil {
		return nil, err
	}
	u := playerAPI(server, username, password, url.Values{"action": {"get_live_streams"}})
	body, status, err := get(u)
	if err != nil {
		return nil, fmt.Errorf("xtream: live streams: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("xtream: live streams: panel returned status %d", status)
	}
	var raw []rawStream
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("xtream: live streams: decode: %w", err)
	}
	out := make([]Stream, 0, len(raw))
	for _, r := range raw {
		out = append(out, Stream{
			StreamID:   int(r.StreamID),
			Name:       strings.TrimSpace(r.Name),
			Icon:       r.Icon,
			CategoryID: r.CategoryID,
			Extension:  r.Extension,
		})
	}
	return out, nil
}

// LiveCategories authenticates and returns the panel's live-stream categories in
// the order the panel lists them (used to preserve group ordering on import).
// Auth is verified first so bad credentials surface as ErrAuth, not an empty
// list. Missing/extra fields are tolerated.
func LiveCategories(server, username, password string) ([]Category, error) {
	if _, _, err := Login(server, username, password); err != nil {
		return nil, err
	}
	u := playerAPI(server, username, password, url.Values{"action": {"get_live_categories"}})
	body, status, err := get(u)
	if err != nil {
		return nil, fmt.Errorf("xtream: live categories: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("xtream: live categories: panel returned status %d", status)
	}
	var raw []rawCategory
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("xtream: live categories: decode: %w", err)
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		out = append(out, Category{ID: string(r.ID), Name: strings.TrimSpace(r.Name)})
	}
	return out, nil
}

// StreamURL builds the playable live URL for a stream:
// {server}/live/{username}/{password}/{streamID}.{ext}. ext defaults to "ts"
// when empty. The server is normalized (trailing slash trimmed) but its scheme
// and port are used exactly as the caller supplied them.
func StreamURL(server, username, password string, streamID int, ext string) string {
	if ext == "" {
		ext = "ts"
	}
	return fmt.Sprintf("%s/live/%s/%s/%d.%s",
		NormalizeServer(server), username, password, streamID, ext)
}

// get performs a GET and returns the body bytes and status code. The body is
// size-limited defensively so a misbehaving panel can't stream unbounded data.
func get(rawURL string) (body []byte, status int, err error) {
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}
