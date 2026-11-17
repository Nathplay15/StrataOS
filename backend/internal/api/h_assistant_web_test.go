package api

import (
	"net/url"
	"testing"
)

func TestPublicURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1/x":     false,
		"http://[::1]/x":         false,
		"http://192.168.1.5/x":   false,
		"http://10.0.0.2/x":      false,
		"http://169.254.1.1/x":   false,
		"http://100.64.0.1/x":    false,
		"ftp://example.com/x":    false,
		"http://localhost/x":     false,
		"http://user@127.0.0.1/": false,
		"https://example.com/x":  true,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := publicURL(u) == nil; got != want {
			t.Errorf("%s: allowed=%v, want %v", raw, got, want)
		}
	}
}

func TestHTMLText(t *testing.T) {
	page := `<html><head><style>body{color:red}</style></head><body><script>x()</script>` +
		`<h1>Title</h1><p>Hello <b>world</b> &amp; friends</p><div>Line one</div><div>Line two</div></body></html>`
	got := htmlText(page)
	if got != "Title\nHello world & friends\nLine one\nLine two" {
		t.Errorf("htmlText: %q", got)
	}
}
