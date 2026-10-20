// Package proxy is the reverse proxy of the app domains: host names served
// by the daemon on ports 80 and 443 and sent to local services, with
// certificates from Let's Encrypt (self-signed until one is obtained).
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHost normalises a host name (lower case, no trailing dot) and checks
// it is a full domain name: at least two labels, a letter-only top level,
// no IP address, no wildcard.
func ValidHost(h string) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" || len(h) > 253 {
		return "", fmt.Errorf("invalid host name")
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return "", fmt.Errorf("enter a host name, not an IP address")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("enter a full host name (for example jellyfin.example.com)")
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return "", fmt.Errorf("invalid host name %q", h)
		}
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "abcdefghijklmnopqrstuvwxyz") != "" || len(tld) < 2 {
		return "", fmt.Errorf("invalid host name %q", h)
	}
	return h, nil
}

// localTLDs never get a public certificate (Let's Encrypt refuses them).
var localTLDs = []string{"test", "local", "lan", "home", "internal", "localhost", "example", "invalid", "localdomain", "intranet", "corp", "home.arpa"}

// LocalName reports whether h only exists on a private network.
func LocalName(h string) bool {
	for _, t := range localTLDs {
		if strings.HasSuffix(h, "."+t) {
			return true
		}
	}
	return false
}

// Route is where one host name goes. Target nil means the StrataOS interface,
// served in-process so that it sees the real client address.
type Route struct {
	Host    string
	Target  *url.URL
	LANOnly bool
	proxy   *httputil.ReverseProxy
}

// Table holds the routes by host name.
type Table map[string]*Route

// hostOnly drops the port and the trailing dot of a Host header or SNI name.
func hostOnly(h string) string {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// Lookup finds the route of a Host header or SNI name.
func (t Table) Lookup(host string) (*Route, bool) {
	r, ok := t[hostOnly(host)]
	return r, ok
}

// ForwardedFor returns the client address passed by a forwarder running on
// this machine (cloudflared sets Cf-Connecting-IP; others X-Forwarded-For,
// whose last entry is the address the forwarder saw). Only trusted when the
// direct peer is the loopback: it can only lower the trust given to it.
func ForwardedFor(peer string, h http.Header) string {
	a, err := netip.ParseAddr(peer)
	if err != nil || !a.Unmap().IsLoopback() {
		return ""
	}
	if v, err := netip.ParseAddr(strings.TrimSpace(h.Get("Cf-Connecting-IP"))); err == nil {
		return v.Unmap().String()
	}
	if xff := h.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(xff[len(xff)-1], ",")
		if v, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			return v.Unmap().String()
		}
	}
	return ""
}

// RealIP is the address of the client of a request.
func RealIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if f := ForwardedFor(host, r.Header); f != "" {
		host = f
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// Server serves the app domains.
type Server struct {
	mu     sync.RWMutex
	routes Table
	srv80  *http.Server
	srv443 *http.Server

	Certs *CertStore
	// UI serves the StrataOS interface (routes without target).
	UI http.Handler
	// Allow applies the address blocking rules; nil allows everyone.
	Allow func(netip.Addr) bool
	// Private tells which addresses belong to the local network (LAN-only routes).
	Private func(netip.Addr) bool
	// Fallback80 answers the port 80 requests of other names (Let's Encrypt
	// challenge of the interface certificate); nil answers 404.
	Fallback80 func() http.Handler
	// FallbackCert is the certificate for unknown names.
	FallbackCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	// StripCookie is never passed to the apps nor set by them (the session
	// cookie of the interface, which browsers also send to other ports).
	StripCookie string
	// Listen addresses (":80" and ":443"; changed in tests).
	HTTPAddr, HTTPSAddr string
}

// SetRoutes replaces the routing table.
func (s *Server) SetRoutes(t Table) {
	for _, r := range t {
		if r.Target != nil && r.proxy == nil {
			r.proxy = s.newProxy(r.Target)
		}
	}
	s.mu.Lock()
	s.routes = t
	s.mu.Unlock()
}

func (s *Server) lookup(host string) (*Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.routes.Lookup(host)
}

// Running reports whether the proxy listens.
func (s *Server) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.srv443 != nil
}

// ErrPortBusy names the port another program uses.
type ErrPortBusy struct{ Port int }

func (e *ErrPortBusy) Error() string { return fmt.Sprintf("port %d is already in use", e.Port) }

// Start listens on ports 80 and 443; it fails without listening on either
// when one is taken.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv443 != nil {
		return nil
	}
	a80, a443 := s.HTTPAddr, s.HTTPSAddr
	if a80 == "" {
		a80 = ":80"
	}
	if a443 == "" {
		a443 = ":443"
	}
	l80, err := net.Listen("tcp", a80)
	if err != nil {
		return &ErrPortBusy{Port: portOf(a80)}
	}
	l443, err := net.Listen("tcp", a443)
	if err != nil {
		l80.Close()
		return &ErrPortBusy{Port: portOf(a443)}
	}
	s.srv80 = &http.Server{Handler: http.HandlerFunc(s.serve80), ReadHeaderTimeout: 15 * time.Second}
	s.srv443 = &http.Server{Handler: s, ReadHeaderTimeout: 15 * time.Second,
		TLSConfig: &tls.Config{GetCertificate: s.getCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}}
	go serve(s.srv80, l80, false)
	go serve(s.srv443, l443, true)
	return nil
}

func serve(srv *http.Server, ln net.Listener, tls bool) {
	var err error
	if tls {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("app domains listener", "addr", ln.Addr(), "err", err)
	}
}

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

// Stop closes both listeners.
func (s *Server) Stop() {
	s.mu.Lock()
	a, b := s.srv80, s.srv443
	s.srv80, s.srv443 = nil, nil
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range []*http.Server{a, b} {
		if srv != nil {
			_ = srv.Shutdown(ctx)
		}
	}
}

// getCertificate picks the certificate of the SNI name.
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if r, ok := s.lookup(hello.ServerName); ok && s.Certs != nil {
		if c, err := s.Certs.Get(r.Host); err == nil {
			return c, nil
		}
	}
	if s.FallbackCert != nil {
		return s.FallbackCert(hello)
	}
	return nil, fmt.Errorf("unknown host name")
}

// serve80 answers the ACME challenges and redirects the rest to HTTPS.
func (s *Server) serve80(w http.ResponseWriter, r *http.Request) {
	if s.Certs != nil && s.Certs.ServeChallenge(w, r) {
		return
	}
	rt, ok := s.lookup(r.Host)
	if !ok {
		if s.Fallback80 != nil {
			if h := s.Fallback80(); h != nil {
				h.ServeHTTP(w, r)
				return
			}
		}
		page(w, http.StatusNotFound, "Unknown site", "No application is published under this name.")
		return
	}
	http.Redirect(w, r, "https://"+rt.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}

// ServeHTTP serves the HTTPS requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.lookup(r.Host)
	if !ok {
		page(w, http.StatusNotFound, "Unknown site", "No application is published under this name.")
		return
	}
	ip := RealIP(r)
	if s.Allow != nil && !s.Allow(ip) {
		page(w, http.StatusForbidden, "Access denied", "Your address is blocked.")
		return
	}
	if rt.LANOnly && (s.Private == nil || !s.Private(ip)) {
		page(w, http.StatusForbidden, "Access denied", "This application is only available from the local network.")
		return
	}
	w.Header().Set("Strict-Transport-Security", "max-age=15552000")
	if rt.Target == nil {
		if s.UI == nil {
			page(w, http.StatusBadGateway, "Unavailable", "The interface is not available.")
			return
		}
		s.UI.ServeHTTP(w, r)
		return
	}
	rt.proxy.ServeHTTP(w, r)
}

func (s *Server) newProxy(target *url.URL) *httputil.ReverseProxy {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // never through the environment's proxy
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: localOnly}).DialContext
	if target.Scheme == "https" {
		// Local services mostly use self-signed certificates.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			Forwarded(pr.Out.Header, pr.In)
			StripCookie(pr.Out.Header, s.StripCookie)
		},
		ModifyResponse: func(resp *http.Response) error {
			StripSetCookie(resp.Header, s.StripCookie)
			return nil
		},
		Transport:     tr,
		FlushInterval: 100 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Debug("app domains upstream", "host", r.Host, "err", err)
			page(w, http.StatusBadGateway, "Application unavailable", "The application does not answer. Check that it is running.")
		},
	}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// LocalAddr reports whether a service address is on this machine or the
// local network (Tailscale addresses included).
func LocalAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// localOnly refuses connections to the Internet: a name of the local network
// must not resolve to a public server.
func localOnly(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !LocalAddr(ap.Addr()) {
		return fmt.Errorf("%s is not on the local network", address)
	}
	return nil
}

// Forwarded sets the X-Forwarded-* headers of a request sent to an app from
// the incoming one (the client's own values are never passed on).
func Forwarded(out http.Header, in *http.Request) {
	ip := RealIP(in)
	for _, k := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Ip", "Cf-Connecting-Ip"} {
		out.Del(k)
	}
	if ip.IsValid() {
		out.Set("X-Forwarded-For", ip.String())
		out.Set("X-Real-IP", ip.String())
	}
	proto, port := "http", "80"
	if in.TLS != nil {
		proto, port = "https", "443"
	}
	out.Set("X-Forwarded-Proto", proto)
	out.Set("X-Forwarded-Host", hostOnly(in.Host))
	out.Set("X-Forwarded-Port", port)
}

// StripCookie removes one cookie from a Cookie header.
func StripCookie(h http.Header, name string) {
	if name == "" {
		return
	}
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, c := range strings.Split(line, ";") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if n, _, _ := strings.Cut(c, "="); strings.TrimSpace(n) == name {
				continue
			}
			keep = append(keep, c)
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

// StripSetCookie drops the Set-Cookie headers of one cookie.
func StripSetCookie(h http.Header, name string) {
	if name == "" {
		return
	}
	var keep []string
	for _, v := range h.Values("Set-Cookie") {
		if n, _, _ := strings.Cut(v, "="); strings.TrimSpace(n) == name {
			continue
		}
		keep = append(keep, v)
	}
	h.Del("Set-Cookie")
	for _, v := range keep {
		h.Add("Set-Cookie", v)
	}
}

func page(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s</title><body style="font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#333"><h1 style="font-size:1.4rem">%s</h1><p>%s</p><p style="color:#888;font-size:.85rem">StrataOS</p></body>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}

// ParseTarget checks the address of a service: http(s)://host:port with an
// optional path, host being an IP address or a name.
func ParseTarget(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the address must look like http://192.168.1.20:8080")
	}
	host, port := u.Hostname(), u.Port()
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port")
		}
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !LocalAddr(a) {
			return nil, fmt.Errorf("the service must be on this NAS or the local network")
		}
	} else if host != "localhost" {
		if _, err := ValidHost(host); err != nil && !labelRe.MatchString(host) {
			return nil, fmt.Errorf("invalid host in the address")
		}
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}
