// Package netguard makes HTTP requests to addresses chosen by someone else
// (an AI model, a community catalog) without letting them reach the NAS's
// own services or the local network (SSRF). The check runs when the
// connection is made, on the address actually dialed: a name that resolves
// to a public address when checked and to 127.0.0.1 a moment later (DNS
// rebinding) is refused, and so is every redirect, since it goes through the
// same dialer. Proxies from the environment are never used.
package netguard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlocked is returned for an address that is not public.
var ErrBlocked = errors.New("internal addresses are not allowed")

// blockedPrefixes are the ranges that are never public, beyond what netip
// classifies (loopback, private, link-local, multicast, unspecified).
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, broadcast
		"64:ff9b::/96",    // NAT64: embeds any IPv4 address
		"64:ff9b:1::/48",  // local-use NAT64
		"2002::/16",       // 6to4: embeds any IPv4 address
		"2001::/32",       // Teredo
		"2001:db8::/32",   // documentation
		"fc00::/7",        // unique local
		"fec0::/10",       // site-local (deprecated)
		"100::/64",        // discard
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// ownAddrs returns the addresses of the NAS's interfaces (a public address
// of the NAS is still the NAS). Replaced in tests.
var ownAddrs = func() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out
}

// Blocked reports whether ip must not be reached.
func Blocked(ip netip.Addr) bool {
	return blocked(ip, false)
}

// BlockedLAN is Blocked without the private ranges: for a target an
// administrator chose themselves (a webhook, a ntfy address), a server on
// the LAN is legitimate, but the NAS itself, loopback and link-local (cloud
// metadata) never are.
func BlockedLAN(ip netip.Addr) bool {
	return blocked(ip, true)
}

// BlockedSelf is BlockedLAN that also lets the NAS itself and loopback
// through (an app of the NAS as webhook target, chosen by an
// administrator); link-local (cloud metadata) and the reserved ranges stay
// refused.
func BlockedSelf(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if a := ip.Unmap(); a.IsLoopback() || isOwn(a) {
		return false
	}
	return blocked(ip, true)
}

func isOwn(ip netip.Addr) bool {
	for _, own := range ownAddrs() {
		if own == ip {
			return true
		}
	}
	return false
}

func blocked(ip netip.Addr, allowPrivate bool) bool {
	if !ip.IsValid() {
		return true
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		(!allowPrivate && ip.IsPrivate()) {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	for _, own := range ownAddrs() {
		if own == ip {
			return true
		}
	}
	return false
}

// control runs on every connection attempt, with the address being dialed.
func control(network, address string, _ syscall.RawConn) error {
	return controlFor(address, false)
}

func controlFor(address string, allowPrivate bool) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrBlocked
	}
	if blocked(ap.Addr(), allowPrivate) {
		return fmt.Errorf("%w (%s)", ErrBlocked, ap.Addr())
	}
	return nil
}

// Dialer returns a dialer that refuses non-public addresses at connect time.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: control}
}

// DialerLAN is Dialer but keeps private LAN addresses reachable (admin-chosen
// targets such as a self-hosted webhook or ntfy).
func DialerLAN(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		return controlFor(address, true)
	}}
}

// Options of a guarded client.
type Options struct {
	Timeout time.Duration
	// HTTPSOnly refuses plain http, also on redirects.
	HTTPSOnly bool
	// MaxRedirects followed (0: 3).
	MaxRedirects int
	// AllowPrivate keeps private LAN addresses reachable (a target an
	// administrator chose, like a self-hosted webhook or ntfy). The NAS
	// itself, loopback and link-local (cloud metadata) stay refused.
	AllowPrivate bool
	// AllowSelf also lets the NAS itself and loopback through (implies
	// AllowPrivate): administrators' webhook and ntfy targets.
	AllowSelf bool
}

// CheckURL refuses a URL that is not http(s) (https only with httpsOnly),
// has no host or carries credentials. The address itself is checked when
// connecting. With allowPrivate, a literal private LAN address is accepted
// (admin-chosen target); the NAS itself and loopback stay refused either way.
func CheckURL(u *url.URL, httpsOnly bool) error {
	return CheckURLLAN(u, httpsOnly, false)
}

// CheckURLLAN is CheckURL with the private-LAN allowance of BlockedLAN.
func CheckURLLAN(u *url.URL, httpsOnly, allowPrivate bool) error {
	if u == nil || (u.Scheme != "https" && (httpsOnly || u.Scheme != "http")) {
		if httpsOnly {
			return errors.New("only https addresses are allowed")
		}
		return errors.New("only http and https addresses are allowed")
	}
	if u.Hostname() == "" || u.User != nil {
		return errors.New("invalid address")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && blocked(ip, allowPrivate) {
		return ErrBlocked
	}
	return nil
}

// Client returns an HTTP client that only reaches public addresses.
func Client(o Options) *http.Client {
	return ClientTLS(o, nil)
}

// ClientTLS is Client with a TLS configuration of its own (e.g. tlstrust for
// a self-signed server). o.AllowPrivate applies as in BlockedLAN.
func ClientTLS(o Options, tlsCfg *tls.Config) *http.Client {
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxRedirects == 0 {
		o.MaxRedirects = 3
	}
	dialer := Dialer(10 * time.Second)
	if o.AllowPrivate {
		dialer = DialerLAN(10 * time.Second)
	}
	if o.AllowSelf {
		o.AllowPrivate = true
		dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || BlockedSelf(ap.Addr()) {
				return ErrBlocked
			}
			return nil
		}}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     tlsCfg != nil,
	}
	return &http.Client{
		Timeout:   o.Timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > o.MaxRedirects {
				return errors.New("too many redirects")
			}
			if o.AllowSelf {
				return checkURLSelf(req.URL, o.HTTPSOnly)
			}
			return CheckURLLAN(req.URL, o.HTTPSOnly, o.AllowPrivate)
		},
	}
}

// Get fetches a URL with a guarded client.
func Get(ctx context.Context, c *http.Client, raw string, httpsOnly bool) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid address")
	}
	if err := CheckURL(u, httpsOnly); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// checkURLSelf is CheckURLLAN for AllowSelf clients.
func checkURLSelf(u *url.URL, httpsOnly bool) error {
	if u != nil {
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
			if BlockedSelf(ip) {
				return ErrBlocked
			}
			v := *u
			v.Host = "example.com" // the address is fine: check the rest
			return CheckURLLAN(&v, httpsOnly, true)
		}
	}
	return CheckURLLAN(u, httpsOnly, true)
}
