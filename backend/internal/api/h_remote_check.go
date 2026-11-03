package api

// Remote access assistant: what the user wants to reach from outside, the
// method recommended for it (gate.Recommend) with the NAS's own facts, and
// the test "Is it reachable from outside?". The test runs from the NAS
// itself without any third-party account: it resolves the name with public
// resolvers (1.1.1.1, 9.9.9.9), connects to the address they give, checks the
// certificate like a browser and asks /api/remote/probe, which proves that
// the answer comes from this NAS. Through a Cloudflare Tunnel the request
// really goes out and comes back in; for a name pointing at the router, it
// depends on the router looping back (hairpin NAT), which the diagnosis says.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/gate"
	"strataos/internal/proxy"
	"strataos/internal/remote"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vpn"
)

// publicResolvers answer for the Internet: the NAS's own resolver may know
// local names (hosts file, router) that nobody outside can resolve.
var publicResolvers = []string{"1.1.1.1:53", "9.9.9.9:53"}

var (
	probeKey = func() []byte {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return b
	}()
	probeLimit = &gate.RateLimiter{Burst: 10, Every: 6 * time.Second}

	natCache struct {
		sync.Mutex
		at              time.Time
		public, router  netip.Addr
		method, verdict string
	}
)

func probeProof(nonce string) string {
	m := hmac.New(sha256.New, probeKey)
	m.Write([]byte(nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// defaultGateway returns the IPv4 address of the default router.
func defaultGateway() netip.Addr {
	out, err := sys.Run("ip", "-4", "route", "show", "default")
	if err != nil {
		return netip.Addr{}
	}
	f := strings.Fields(out)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "via" {
			if a, err := netip.ParseAddr(f[i+1]); err == nil {
				return a
			}
		}
	}
	return netip.Addr{}
}

// natFacts returns the public address and the NAT verdict (cached 10 minutes).
func natFacts(ctx context.Context, fresh bool) (public, router netip.Addr, method, verdict string) {
	natCache.Lock()
	defer natCache.Unlock()
	if !fresh && time.Since(natCache.at) < 10*time.Minute {
		return natCache.public, natCache.router, natCache.method, natCache.verdict
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if ip, err := vpn.PublicIP(ctx); err == nil {
			public, _ = netip.ParseAddr(ip)
		}
	}()
	if gw := defaultGateway(); gw.IsValid() {
		router, method, _ = remote.RouterExternalIP(ctx, gw)
	}
	wg.Wait()
	verdict = gate.ClassifyNAT(router, public)
	natCache.at, natCache.public, natCache.router, natCache.method, natCache.verdict = time.Now(), public, router, method, verdict
	return
}

func publicLookup(ctx context.Context, host string) ([]netip.Addr, bool) {
	for _, srv := range publicResolvers {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv)
		}}
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		addrs, err := r.LookupNetIP(c, "ip4", host)
		cancel()
		if err == nil {
			return unmapAll(addrs), false
		}
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return nil, false // the resolver answered: no such name
		}
	}
	return nil, true
}

func unmapAll(l []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(l))
	for _, a := range l {
		out = append(out, a.Unmap())
	}
	return out
}

func tcpError(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "unreachable"
}

func tlsError(err error) string {
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	var ve *tls.CertificateVerificationError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ua):
		return "untrusted"
	case errors.As(err, &he):
		return "name"
	case errors.As(err, &ci) && ci.Reason == x509.Expired:
		return "expired"
	case errors.As(err, &ve):
		return "untrusted"
	}
	return "error"
}

type checkReq struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Via: "tunnel" (a Cloudflare Tunnel serves the name) or "domain".
	Via string `json:"via"`
	// UI: the name publishes the StrataOS interface (answers the probe).
	UI bool `json:"ui"`
	// HTTP: plain HTTP instead of HTTPS.
	HTTP bool `json:"http"`
}

type checkResp struct {
	Host     string         `json:"host"`
	Address  string         `json:"address,omitempty"`
	Verdict  string         `json:"verdict"`
	Findings []gate.Finding `json:"findings"`
	PublicIP string         `json:"publicIp,omitempty"`
	NAT      string         `json:"nat"`
}

// reachCheck runs the steps of the test and diagnoses them.
func (s *Server) reachCheck(ctx context.Context, req checkReq) checkResp {
	p := gate.Probe{Host: req.Host, Port: req.Port, Tunnel: req.Via == "tunnel", ProbeKnown: req.UI}
	public, _, _, nat := natFacts(ctx, false)
	p.PublicIP, p.NAT = public, nat
	p.PublicDNS, p.PublicDNSErr = publicLookup(ctx, req.Host)
	if addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", req.Host); err == nil {
		p.LocalDNS = unmapAll(addrs)
	}
	target := netip.Addr{}
	if len(p.PublicDNS) > 0 {
		target = p.PublicDNS[0]
	} else if len(p.LocalDNS) > 0 {
		target = p.LocalDNS[0]
	}
	out := checkResp{Host: req.Host, NAT: nat}
	if public.IsValid() {
		out.PublicIP = public.String()
	}
	if !target.IsValid() {
		p.TCP = "skipped"
		out.Verdict, out.Findings = gate.Diagnose(p)
		return out
	}
	addr := netip.AddrPortFrom(target, uint16(req.Port)).String()
	out.Address = addr
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp4", addr)
	p.TCP = tcpError(err)
	if err != nil {
		out.Verdict, out.Findings = gate.Diagnose(p)
		return out
	}
	conn.Close()
	scheme := "https"
	if req.HTTP {
		scheme, p.TLS = "http", "skipped"
	} else {
		tc, err := (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: req.Host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp4", addr)
		p.TLS = tlsError(err)
		if err == nil {
			tc.Close()
		}
	}
	if p.TLS == "" || p.TLS == "skipped" || p.TLS == "untrusted" || p.TLS == "expired" || p.TLS == "name" {
		// Go on past a certificate problem to see who answers.
		nonce := token()[:32]
		tr := &http.Transport{
			DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "tcp4", addr) },
			TLSClientConfig: &tls.Config{ServerName: req.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // verified above
			Proxy:           nil,
		}
		defer tr.CloseIdleConnections()
		cl := &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		u := scheme + "://" + net.JoinHostPort(req.Host, strconv.Itoa(req.Port)) + "/api/remote/probe?n=" + nonce
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		hreq.Header.Set("User-Agent", "StrataOS reachability check")
		if resp, err := cl.Do(hreq); err == nil {
			p.HTTPStatus = resp.StatusCode
			var j struct {
				Proof string `json:"proof"`
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if json.Unmarshal(b, &j) == nil && hmac.Equal([]byte(j.Proof), []byte(probeProof(nonce))) {
				p.ThisNAS = true
			}
		}
	}
	out.Verdict, out.Findings = gate.Diagnose(p)
	return out
}

// situation gathers what the assistant needs to recommend a method.
func (s *Server) situation(ctx context.Context, fresh bool) (gate.Situation, map[string]any) {
	var tun store.CloudflareTunnel
	var wg, proxyOn bool
	var domains []string
	s.store.Read(func(st *store.State) {
		tun, wg, proxyOn = st.Remote.Tunnel, st.VPN.Enabled, st.Remote.Proxy.Enabled
		for _, d := range st.Remote.Proxy.Domains {
			if !proxy.LocalName(d.Host) && !d.LANOnly {
				domains = append(domains, d.Host)
			}
		}
		if st.DDNS.Provider != "" && st.DDNS.Hostname != "" {
			domains = append(domains, st.DDNS.Hostname)
		}
	})
	public, router, method, nat := natFacts(ctx, fresh)
	ts := remote.TailscaleStatus(ctx)
	tunnel := remote.Tunnel(ctx, tun)
	sit := gate.Situation{
		CGNAT: nat == gate.NATCarrier, CGNATKnown: nat != gate.NATUnknown,
		Tunnel: tunnel.Connected, Tailscale: ts.LoggedIn, WireGuard: wg, Domain: proxyOn && len(domains) > 0,
	}
	facts := map[string]any{
		"nat": nat, "natMethod": method, "tunnel": tunnel, "tailscale": map[string]any{"installed": ts.Installed, "loggedIn": ts.LoggedIn, "dnsName": ts.DNSName},
		"wireguard": wg, "proxy": proxyOn, "domains": domains,
	}
	if public.IsValid() {
		facts["publicIp"] = public.String()
	}
	if router.IsValid() {
		facts["routerIp"] = router.String()
	}
	return sit, facts
}

func (s *Server) remoteCheckRoutes() {
	// Answered on every address of the interface: proves to the
	// reachability test that it reached this NAS. The nonce is echoed only
	// as an HMAC, so nothing about the NAS is revealed.
	s.mux.Handle("GET /api/remote/probe", linkRate(probeLimit, s.wrap(func(r *http.Request) (any, error) {
		n := r.URL.Query().Get("n")
		if len(n) < 16 || len(n) > 64 {
			return nil, badRequest("invalid probe")
		}
		return map[string]string{"proof": probeProof(n)}, nil
	})))

	s.admin("GET /api/remote/assistant", func(r *http.Request) (any, error) {
		var goals []string
		for _, g := range strings.Split(r.URL.Query().Get("goals"), ",") {
			for _, k := range gate.Goals {
				if g == k {
					goals = append(goals, g)
				}
			}
		}
		sit, facts := s.situation(r.Context(), r.URL.Query().Get("refresh") == "1")
		return map[string]any{"goals": goals, "options": gate.Recommend(goals, sit), "facts": facts}, nil
	})

	s.admin("POST /api/remote/check", func(r *http.Request) (any, error) {
		var req checkReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		host, err := proxy.ValidHost(req.Host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		req.Host = host
		if req.Port == 0 {
			req.Port = 443
			if req.HTTP {
				req.Port = 80
			}
		}
		if req.Port < 1 || req.Port > 65535 {
			return nil, badRequest("invalid port")
		}
		if req.Via != "tunnel" {
			req.Via = "domain"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		out := s.reachCheck(ctx, req)
		s.audit(r, "remote", "Reachability test of "+host+": "+out.Verdict)
		return out, nil
	})
}
