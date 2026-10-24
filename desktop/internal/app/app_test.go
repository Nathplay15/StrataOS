package app

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"strings"
	"testing"

	"strataos-sync/internal/config"
	"strataos-sync/internal/i18n"
)

func TestParseLinkAndURL(t *testing.T) {
	base, fp, code, user, err := ParseLink("strataos://pair?url=https%3A%2F%2F192.168.1.5%3A7443&fp=ab%3Acd&code=xyz&user=alice")
	if err != nil || base != "https://192.168.1.5:7443" || code != "xyz" || user != "alice" || fp != "" {
		t.Fatalf("link: %q %q %q %q %v", base, fp, code, user, err)
	}
	if _, _, _, _, err := ParseLink("https://x"); err == nil {
		t.Fatal("not a link accepted")
	}
	for in, want := range map[string]string{"nas.local": "https://nas.local:7443", "http://10.0.0.2:7070": "https://10.0.0.2:7443",
		"https://nas.example.org": "https://nas.example.org:7443", "https://h:8443/x": "https://h:8443", "[fd00::1]": "https://[fd00::1]:7443"} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v", in, got, err)
		}
	}
}

func TestSettingsServer(t *testing.T) {
	a, err := New(&config.Store{Dir: t.TempDir()}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ui, err := a.ServeUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ui.addr
	// Without the token: refused.
	if r, err := http.Get(base + "/"); err != nil || r.StatusCode != 403 {
		t.Fatalf("no token: %v %v", r.StatusCode, err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	r, err := c.Get(ui.URL())
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("with the token: %v", err)
	}
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), "app.js") {
		t.Fatal("page not served")
	}
	// The API needs the header (no cross-site posts) and the cookie.
	if r, _ := c.Post(base+"/api/pause", "application/json", strings.NewReader(`{"on":true}`)); r.StatusCode != 403 {
		t.Fatalf("API without the header: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", base+"/api/state", nil)
	req.Header.Set("X-Strata-Sync", "1")
	r, err = c.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("state: %v %v", r.StatusCode, err)
	}
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `"paired":false`) {
		t.Fatalf("state: %s", b)
	}
	// Another host name (DNS rebinding): refused.
	req, _ = http.NewRequest("GET", base+"/api/state", nil)
	req.Host = "evil.example:80"
	req.Header.Set("X-Strata-Sync", "1")
	if r, _ := c.Do(req); r.StatusCode != 403 {
		t.Fatalf("other host: %d", r.StatusCode)
	}
}

// Every text the page and the Go code use is in the English catalog.
func TestTextsInCatalog(t *testing.T) {
	en := i18n.Catalog("en")
	var src string
	for _, f := range []string{"web/index.html", "web/app.js", "app.go", "server.go", "../tray/tray.go", "../tray/tray_systray.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src += string(b)
	}
	re := regexp.MustCompile(`(?:data-t="|\bt\(|\.T\(|\bT\()"?([a-z]+\.[a-z.]+|about)"`)
	n := 0
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		n++
		if _, ok := en[m[1]]; !ok {
			t.Errorf("text %q missing from en.json", m[1])
		}
	}
	for _, id := range []string{"action.pause", "action.resume", "action.pauseall", "action.resumeall"} {
		if _, ok := en[id]; !ok {
			t.Errorf("text %q missing", id)
		}
	}
	if n < 50 {
		t.Fatalf("only %d texts found: the pattern is wrong", n)
	}
}
