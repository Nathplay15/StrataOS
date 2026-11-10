package app

import (
	"os"
	"regexp"
	"testing"

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

func TestTextsInCatalog(t *testing.T) {
	en := i18n.Catalog("en")
	var src string
	for _, f := range []string{"app.go", "e2e.go", "../agent/agent.go", "../agent/routes.go", "../agent/proxy.go", "../agent/transfers.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src += string(b)
	}
	re := regexp.MustCompile(`(?:\.T\(|\bT\()"([a-z0-9]+\.[a-z0-9.]+)"`)
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
	if n < 15 {
		t.Fatalf("only %d texts found: the pattern is wrong", n)
	}
}
