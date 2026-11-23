package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestBlocked(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "0.0.0.0": true, "::": true, "224.0.0.1": true,
		"fd00::1": true, "fe80::1": true, "64:ff9b::7f00:1": true, "2002:7f00:1::1": true,
		"::ffff:127.0.0.1": true, "::ffff:10.0.0.1": true, "255.255.255.255": true, "198.18.0.1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		if got := Blocked(netip.MustParseAddr(s)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", s, got, want)
		}
	}
	// The NAS's own public address is refused too.
	old := ownAddrs
	ownAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("203.0.114.7")} }
	defer func() { ownAddrs = old }()
	if !Blocked(netip.MustParseAddr("203.0.114.7")) || Blocked(netip.MustParseAddr("203.0.114.8")) {
		t.Fatal("own address")
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/x": true, "http://example.com/": true, "ftp://example.com/": false,
		"https://user:pw@example.com/": false, "http://127.0.0.1/": false, "http://[::1]/": false, "https:///x": false,
	} {
		u, _ := url.Parse(raw)
		if got := CheckURL(u, false) == nil; got != ok {
			t.Errorf("CheckURL(%s) = %v", raw, got)
		}
	}
	u, _ := url.Parse("http://example.com/")
	if CheckURL(u, true) == nil {
		t.Fatal("http must be refused when https only")
	}
}

// A name resolving to an internal address is refused when connecting, not
// only when checked: what matters is the address actually dialed.
func TestClientRefusesInternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	c := Client(Options{})
	for _, raw := range []string{srv.URL, "http://localhost" + port + "/"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s reached", raw)
		}
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	if _, err := Get(context.Background(), c, "http://localhost"+port+"/", true); err == nil {
		t.Fatal("http with https only")
	}
}

// BlockedLAN keeps the LAN reachable (a self-hosted webhook/ntfy is
// legitimate) but never the NAS itself, loopback or cloud metadata.
func TestBlockedLAN(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "169.254.169.254": true, "fe80::1": true,
		"0.0.0.0": true, "224.0.0.1": true, "100.64.0.1": true,
		"192.168.1.50": false, "10.1.2.3": false, "172.16.0.1": false, "8.8.8.8": false,
	} {
		if got := BlockedLAN(netip.MustParseAddr(s)); got != want {
			t.Errorf("BlockedLAN(%s) = %v, want %v", s, got, want)
		}
	}
	old := ownAddrs
	ownAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.10")} }
	defer func() { ownAddrs = old }()
	if !BlockedLAN(netip.MustParseAddr("192.168.1.10")) {
		t.Error("the NAS's own LAN address must stay refused")
	}
}

// A client with AllowPrivate still refuses loopback at connect time, and
// CheckURLLAN accepts a literal LAN address but not cloud metadata.
func TestClientTLSAllowPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	c := ClientTLS(Options{AllowPrivate: true}, nil)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("loopback must stay refused with AllowPrivate")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
	uLAN, _ := url.Parse("http://192.168.1.50:8080/topic")
	if err := CheckURLLAN(uLAN, false, true); err != nil {
		t.Errorf("LAN address refused: %v", err)
	}
	uMeta, _ := url.Parse("http://169.254.169.254/latest/meta-data")
	if err := CheckURLLAN(uMeta, false, true); err == nil {
		t.Error("cloud metadata must stay refused")
	}
}
