package resolver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Exposestrat resolves channels served by the exposestrat.com / *.zohanayaan.com
// embed family (used by aggregators like free-sports.live).
//
// How it works (reverse-engineered from the live player page):
//   - The player page is  https://exposestrat.com/maestrohd1.php?player=desktop&live=<slug>
//   - That page is gated on Referer: it returns a tiny stub unless the request
//     carries Referer https://embedhd.org/ (the parent iframe). With it, the full
//     player JS is served.
//   - The signed .m3u8 URL is rendered into the page by the server, but obfuscated:
//     a function (whose name rotates every load) returns the URL as a per-character
//     array literal joined together, optionally concatenated with the innerHTML of
//     a hidden element (whose id also rotates). Example:
//        function grtUteptlH(){ return(["h","t","t","p",...].join("")
//            + someEmptyVar.join("") + document.getElementById("ifa…rk").innerHTML); }
//   - We don't run JS: we extract the char array (and any referenced hidden
//     element's text) and join them to reconstruct the fresh URL.
//   - The reconstructed URL is then PLAYED with Referer https://exposestrat.com/
//     (a different referer than the one used to fetch the page).
//
// This is inherently brittle — if they change the page structure, the regexes
// below need updating. That's the unavoidable cost of resolving someone else's
// player; the token itself is unforgeable, so scraping is the only option.
type Exposestrat struct {
	Client *http.Client
}

const (
	exposestratPlayer  = "https://exposestrat.com/maestrohd1.php?player=desktop&live="
	exposestratPageRef = "https://embedhd.org/"      // Referer required to FETCH the page
	exposestratPlayRef = "https://exposestrat.com/"  // Referer required to PLAY the stream
	browserUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:152.0) Gecko/20100101 Firefox/152.0"
)

// Matches:  function NAME(){ return( [ ...array... ].join("")  <tail> ) ;
// The name is intentionally not anchored (it rotates). (?s) lets . span newlines.
var esFnRe = regexp.MustCompile(`(?s)function\s+\w+\s*\(\)\s*\{\s*return\(\s*(\[.*?\])\.join\(""\)(.*?)\)\s*;`)

// Every quoted token inside the char-array literal (handles escapes like \/).
var esCharRe = regexp.MustCompile(`"((?:\\.|[^"\\])*)"`)

// getElementById("id") references appearing in the function tail.
var esGebiRe = regexp.MustCompile(`getElementById\(\s*"([^"]+)"\s*\)`)

func (Exposestrat) Name() string { return "exposestrat" }

func (e Exposestrat) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (e Exposestrat) Resolve(ctx context.Context, slug string) (Resolved, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Resolved{}, fmt.Errorf("exposestrat: empty slug")
	}
	page := exposestratPlayer + url.QueryEscape(slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return Resolved{}, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", exposestratPageRef)
	req.Header.Set("Accept", "*/*")

	resp, err := e.client().Do(req)
	if err != nil {
		return Resolved{}, fmt.Errorf("exposestrat: fetch page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Resolved{}, fmt.Errorf("exposestrat: page status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return Resolved{}, fmt.Errorf("exposestrat: read page: %w", err)
	}

	streamURL, err := extractExposestratURL(string(body))
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		URL:     streamURL,
		Referer: exposestratPlayRef,
		Expires: parseExpires(streamURL),
	}, nil
}

// extractExposestratURL reconstructs the signed stream URL from the player HTML.
// Exported-in-spirit via a separate func so it can be unit-tested against a
// saved page fixture without any network.
func extractExposestratURL(html string) (string, error) {
	m := esFnRe.FindStringSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("exposestrat: token function not found (page changed?)")
	}
	arrayLiteral, tail := m[1], m[2]

	var b strings.Builder
	for _, tok := range esCharRe.FindAllStringSubmatch(arrayLiteral, -1) {
		b.WriteString(jsUnescape(tok[1]))
	}

	// The tail may append the innerHTML of one or more hidden elements.
	for _, g := range esGebiRe.FindAllStringSubmatch(tail, -1) {
		b.WriteString(hiddenElementText(html, g[1]))
	}

	out := strings.TrimSpace(jsUnescape(b.String()))
	if !strings.HasPrefix(out, "http") || !strings.Contains(out, ".m3u8") {
		return "", fmt.Errorf("exposestrat: reconstructed value is not a stream URL: %q", out)
	}
	return out, nil
}

// hiddenElementText returns the inner text of the first element carrying id=eid.
func hiddenElementText(html, eid string) string {
	re := regexp.MustCompile(`id\s*=\s*"` + regexp.QuoteMeta(eid) + `"[^>]*>(.*?)<`)
	if m := re.FindStringSubmatch(html); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
