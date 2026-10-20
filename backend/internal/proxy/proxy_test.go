package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestValidHost(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"jellyfin.example.com", "jellyfin.example.com", true},
		{" Photos.Example.COM. ", "photos.example.com", true},
		{"a-b.c-d.example.co.uk", "a-b.c-d.example.co.uk", true},
		{"kuma.strata.test", "kuma.strata.test", true},
		{"localhost", "", false},
		{"example", "", false},
		{"192.168.1.10", "", false},
		{"::1", "", false},
		{"*.example.com", "", false},
		{"-bad.example.com", "", false},
		{"bad-.example.com", "", false},
		{"under_score.example.com", "", false},
		{"example.c0m", "", false},
		{"example.c", "", false},
		{"a..example.com", "", false},
		{"evil.com/path", "", false},
		{"evil.com:443", "", false},
		{strings.Repeat("a", 64) + ".com", "", false},
		{"", "", false},
	} {
		got, err := ValidHost(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ValidHost(%q) = %q, %v", c.in, got, err)
		}
	}
}

func TestLocalName(t *testing.T) {
	for h, want := range map[string]bool{"kuma.strata.test": true, "nas.local": true, "x.home.arpa": true, "jellyfin.example.com": false, "test.com": false, "nas.lan": true} {
		if LocalName(h) != want {
			t.Errorf("LocalName(%q) != %v", h, want)
		}
	}
}

func TestParseTarget(t *testing.T) {
	for in, ok := range map[string]bool{
		"http://127.0.0.1:3001":       true,
		"http://192.168.1.20:8080/":   true,
		"https://10.0.0.5:8443/app":   true,
		"http://localhost:8096":       true,
		"http://nas-media:8096":       true,
		"http://100.101.102.103:80":   true,
		"http://8.8.8.8:80":           false,
		"ftp://127.0.0.1:21":          false,
		"http://user:pw@127.0.0.1:80": false,
		"http://127.0.0.1:99999":      false,
		"http://127.0.0.1:80/?a=b":    false,
		"127.0.0.1:80":                false,
		"http://bad_host:80":          false,
	} {
		if _, err := ParseTarget(in); (err == nil) != ok {
			t.Errorf("ParseTarget(%q): %v", in, err)
		}
	}
}

func TestLookupAndSNI(t *testing.T) {
	cs, err := NewCertStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fallback := &tls.Certificate{}
	s := &Server{Certs: cs, FallbackCert: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return fallback, nil }}
	s.SetRoutes(Table{"kuma.example.com": {Host: "kuma.example.com"}, "nas.example.com": {Host: "nas.example.com"}})
	for in, want := range map[string]string{"kuma.example.com": "kuma.example.com", "KUMA.example.com.": "kuma.example.com", "kuma.example.com:443": "kuma.example.com", "other.example.com": ""} {
		r, ok := s.lookup(in)
		if (want == "") == ok || ok && r.Host != want {
			t.Errorf("lookup(%q) = %v %v", in, r, ok)
		}
	}
	c, err := s.getCertificate(&tls.ClientHelloInfo{ServerName: "kuma.example.com"})
	if err != nil || c.Leaf == nil || c.Leaf.Subject.CommonName != "kuma.example.com" || c.Leaf.DNSNames[0] != "kuma.example.com" {
		t.Fatalf("certificate for kuma: %v %v", c, err)
	}
	if info := cs.Info("kuma.example.com"); info.Source != "self-signed" {
		t.Errorf("source = %s", info.Source)
	}
	if !cs.NeedsCertificate("kuma.example.com") {
		t.Error("a self-signed certificate must still need a real one")
	}
	again, _ := s.getCertificate(&tls.ClientHelloInfo{ServerName: "kuma.example.com"})
	if again != c {
		t.Error("certificate not cached")
	}
	if c, _ := s.getCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"}); c != fallback {
		t.Error("unknown name must get the fallback certificate")
	}
	if c, _ := s.getCertificate(&tls.ClientHelloInfo{}); c != fallback {
		t.Error("no SNI must get the fallback certificate")
	}
}

func TestForwardedFor(t *testing.T) {
	h := http.Header{}
	h.Set("Cf-Connecting-IP", "203.0.113.7")
	if got := ForwardedFor("127.0.0.1", h); got != "203.0.113.7" {
		t.Errorf("cloudflared: %q", got)
	}
	if got := ForwardedFor("192.168.1.50", h); got != "" {
		t.Errorf("header trusted from the network: %q", got)
	}
	h = http.Header{}
	h.Add("X-Forwarded-For", "10.0.0.1, 198.51.100.4")
	if got := ForwardedFor("::1", h); got != "198.51.100.4" {
		t.Errorf("x-forwarded-for: %q", got)
	}
	h.Set("X-Forwarded-For", "garbage")
	if got := ForwardedFor("127.0.0.1", h); got != "" {
		t.Errorf("garbage accepted: %q", got)
	}
}

func TestStripCookie(t *testing.T) {
	h := http.Header{}
	h.Add("Cookie", "a=1; strata_session=secret; b=2")
	h.Add("Cookie", "strata_session=x")
	StripCookie(h, "strata_session")
	if got := h.Get("Cookie"); got != "a=1; b=2" || len(h.Values("Cookie")) != 1 {
		t.Errorf("cookie = %q", got)
	}
	h = http.Header{}
	h.Add("Set-Cookie", "strata_session=evil; Path=/")
	h.Add("Set-Cookie", "app=1; Path=/")
	StripSetCookie(h, "strata_session")
	if v := h.Values("Set-Cookie"); len(v) != 1 || v[0] != "app=1; Path=/" {
		t.Errorf("set-cookie = %v", v)
	}
}

// testServer puts the proxy in front of an app, over TLS.
func testServer(t *testing.T, app http.Handler, route *Route) (*Server, *httptest.Server, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(app)
	t.Cleanup(backend.Close)
	cs, _ := NewCertStore(t.TempDir())
	u, _ := url.Parse(backend.URL)
	route.Target = u
	s := &Server{Certs: cs, StripCookie: "strata_session", Private: func(a netip.Addr) bool { return a.IsLoopback() }}
	s.SetRoutes(Table{route.Host: route})
	front := httptest.NewUnstartedServer(s)
	front.TLS = &tls.Config{GetCertificate: s.getCertificate}
	front.StartTLS()
	t.Cleanup(front.Close)
	return s, backend, front
}

func client(front *httptest.Server) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "app.example.com"},
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return net.Dial("tcp", front.Listener.Addr().String())
			}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestProxyHeaders(t *testing.T) {
	var got *http.Request
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		http.SetCookie(w, &http.Cookie{Name: "strata_session", Value: "fixation"})
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "1"})
		io.WriteString(w, "hello")
	})
	_, _, front := testServer(t, app, &Route{Host: "app.example.com"})
	req, _ := http.NewRequest("GET", "https://app.example.com/path?q=1", nil)
	// From the loopback, as cloudflared does: the header names the visitor.
	req.Header.Set("Cf-Connecting-IP", "203.0.113.9")
	req.Header.Set("X-Real-IP", "6.6.6.6")
	req.Header.Set("Cookie", "strata_session=secret; app=1")
	resp, err := client(front).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("body %q", body)
	}
	if got.Host != "app.example.com" || got.URL.RequestURI() != "/path?q=1" {
		t.Errorf("host %q uri %q", got.Host, got.URL.RequestURI())
	}
	for k, want := range map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Real-Ip": "203.0.113.9", "Cf-Connecting-Ip": "", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "app.example.com", "Cookie": "app=1"} {
		if v := got.Header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	for _, c := range resp.Cookies() {
		if c.Name == "strata_session" {
			t.Error("app could set the session cookie of the interface")
		}
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS")
	}
}

func TestForwardedHeaders(t *testing.T) {
	in := httptest.NewRequest("GET", "http://app.example.com:80/", nil)
	in.RemoteAddr = "192.0.2.10:5555"
	in.Header.Set("X-Forwarded-For", "6.6.6.6")
	in.Header.Set("Cf-Connecting-IP", "6.6.6.6")
	in.Header.Set("Forwarded", "for=6.6.6.6")
	out := in.Header.Clone()
	Forwarded(out, in)
	for k, want := range map[string]string{"X-Forwarded-For": "192.0.2.10", "X-Real-Ip": "192.0.2.10", "X-Forwarded-Proto": "http", "X-Forwarded-Host": "app.example.com", "X-Forwarded-Port": "80", "Forwarded": "", "Cf-Connecting-Ip": ""} {
		if v := out.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestProxyAccessRules(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	s, _, front := testServer(t, app, &Route{Host: "app.example.com", LANOnly: true})
	get := func() int {
		resp, err := client(front).Get("https://app.example.com/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get(); c != 200 {
		t.Errorf("local client refused: %d", c)
	}
	s.Private = func(netip.Addr) bool { return false }
	if c := get(); c != 403 {
		t.Errorf("LAN-only open to the Internet: %d", c)
	}
	s.Private = func(netip.Addr) bool { return true }
	s.Allow = func(netip.Addr) bool { return false }
	if c := get(); c != 403 {
		t.Errorf("blocked address served: %d", c)
	}
	s.Allow = nil
	resp, _ := client(front).Get("https://other.example.com/")
	if resp != nil && resp.StatusCode != 404 {
		t.Errorf("unknown host: %d", resp.StatusCode)
	}
}

func TestRedirect80(t *testing.T) {
	cs, _ := NewCertStore(t.TempDir())
	s := &Server{Certs: cs}
	s.SetRoutes(Table{"app.example.com": {Host: "app.example.com", Target: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}})
	cs.tokens["tok123"] = "tok123.keyauth"
	for _, c := range []struct{ host, path, code, loc, body string }{
		{"app.example.com", "/a/b?c=d", "308", "https://app.example.com/a/b?c=d", ""},
		{"app.example.com:80", "/", "308", "https://app.example.com/", ""},
		{"app.example.com", "/.well-known/acme-challenge/tok123", "200", "", "tok123.keyauth"},
		{"evil.example.com", "/", "404", "", ""},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		s.serve80(w, r)
		if code := w.Code; strconv.Itoa(code) != c.code || w.Header().Get("Location") != c.loc || c.body != "" && w.Body.String() != c.body {
			t.Errorf("%s%s: %d %q %q", c.host, c.path, w.Code, w.Header().Get("Location"), w.Body.String())
		}
	}
	// The interface's own challenge handler gets the other names.
	called := false
	s.Fallback80 = func() http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "nas.example.com"
	s.serve80(httptest.NewRecorder(), r)
	if !called {
		t.Error("fallback not called")
	}
}

func TestWebSocketUpgrade(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no upgrade", 400)
			return
		}
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		line, _ := rw.ReadString('\n')
		rw.WriteString("echo:" + line)
		rw.Flush()
	})
	_, _, front := testServer(t, app, &Route{Host: "app.example.com"})
	conn, err := tls.Dial("tcp", front.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, ServerName: "app.example.com", NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	io.WriteString(conn, "ping\n")
	if l, _ := br.ReadString('\n'); l != "echo:ping\n" {
		t.Errorf("tunnel: %q", l)
	}
}
