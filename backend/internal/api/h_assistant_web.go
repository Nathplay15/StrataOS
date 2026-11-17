package api

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"strataos/internal/netguard"
)

// Web access of the assistant: a web_search tool (DuckDuckGo Lite, no API
// key) and an open_url tool to read a public page. open_url refuses anything
// but a public host: the NAS must not be made to call its own internal
// services on the model's word (SSRF). The check is made on the address
// actually connected to, redirects included (netguard), so a name that
// changes its answer between the check and the connection (DNS rebinding)
// gains nothing.

const webUA = "StrataOS-Assistant/1.0"

// webClient only connects to public addresses and follows a few redirects.
var webClient = netguard.Client(netguard.Options{Timeout: 20 * time.Second, MaxRedirects: 3})

// publicURL refuses anything but a public http(s) address. It gives an early,
// readable answer; the dialer of webClient makes the real check.
func publicURL(u *url.URL) error {
	if err := netguard.CheckURL(u, false); err != nil {
		return err
	}
	ips, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("cannot resolve %s", u.Hostname())
	}
	for _, ip := range ips {
		if netguard.Blocked(ip) {
			return netguard.ErrBlocked
		}
	}
	return nil
}

var (
	ddgLinkRe = regexp.MustCompile(`(?is)<a[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	ddgSnipRe = regexp.MustCompile(`(?is)class=["']result-snippet["'][^>]*>(.*?)</td>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	blockRe   = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>|<svg[^>]*>.*?</svg>`)
	breakRe   = regexp.MustCompile(`(?is)<br[^>]*>|</p>|</div>|</li>|</h[1-6]>|</tr>`)
	spaceRe   = regexp.MustCompile(`[ \t\r\f]+`)
	nlSpaceRe = regexp.MustCompile(`\n[ \t]+`)
	nlRe      = regexp.MustCompile(`\n\s*\n+`)
)

// stripTags turns a piece of HTML into text.
func stripTags(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(s, " ")), " "))
}

// htmlText strips scripts, styles and tags, keeping line breaks.
func htmlText(s string) string {
	s = blockRe.ReplaceAllString(s, " ")
	s = breakRe.ReplaceAllString(s, "\n")
	s = spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(s, " ")), " ")
	s = nlSpaceRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(nlRe.ReplaceAllString(s, "\n"))
}

// webSearchFn is webSearch (replaced in tests: no network).
var webSearchFn = webSearch

// webSearch queries DuckDuckGo Lite and returns the top results as text.
func webSearch(ctx context.Context, query string) (string, error) {
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://lite.duckduckgo.com/lite/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", webUA)
	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	page := string(body)
	links, snips := ddgLinkRe.FindAllStringSubmatch(page, 16), ddgSnipRe.FindAllStringSubmatch(page, 16)
	var b strings.Builder
	n := 0
	for _, l := range links {
		href := strings.TrimSpace(l[1])
		title := stripTags(l[2])
		if href == "" || title == "" || strings.Contains(href, "duckduckgo.com") {
			continue
		}
		if u, err := url.Parse(href); err == nil {
			if real := u.Query().Get("uddg"); real != "" {
				href = real
			}
		}
		n++
		fmt.Fprintf(&b, "%d. %s\n   %s\n", n, title, href)
		if n <= len(snips) {
			if snip := stripTags(snips[n-1][1]); snip != "" {
				fmt.Fprintf(&b, "   %s\n", snip)
			}
		}
		if n == 8 {
			break
		}
	}
	if n == 0 {
		return "no result", nil
	}
	return b.String(), nil
}

// webFetch reads a public page and returns its text (HTML stripped, capped).
func webFetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid address")
	}
	if err := publicURL(u); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain")
	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("page unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("the page answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	text := htmlText(string(body))
	if len(text) > 12000 {
		text = text[:12000] + "…"
	}
	return text, nil
}

// assistantWebTool runs the web tools of the assistant (read-only, no NAS access).
func assistantWebTool(r *http.Request, name, arg string) string {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var out string
	var err error
	if name == "web_search" {
		out, err = webSearchFn(ctx, arg)
	} else {
		out, err = webFetch(ctx, arg)
	}
	if err != nil {
		return name + " failed: " + err.Error()
	}
	return out
}
