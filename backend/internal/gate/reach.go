package gate

import (
	"net/netip"
	"slices"
	"sort"
)

// ---- Remote access advice ----

// Goals the remote access assistant asks about.
const (
	GoalUI     = "ui"     // the web interface from outside
	GoalFiles  = "files"  // files (web file manager, SMB, sync apps)
	GoalPhotos = "photos" // photos and the phone app
	GoalLinks  = "links"  // share links opened by anyone
	GoalPhones = "phones" // phones and laptops of the family
)

var Goals = []string{GoalUI, GoalFiles, GoalPhotos, GoalLinks, GoalPhones}

// Methods the assistant can set up (existing building blocks).
const (
	MethodTunnel    = "tunnel"    // Cloudflare Tunnel
	MethodTailscale = "tailscale" // Tailscale
	MethodWireGuard = "wireguard" // WireGuard server of the NAS
	MethodDomain    = "domain"    // domain + port forwarding + Let's Encrypt
)

// Situation is what the NAS knows about its network and setup.
type Situation struct {
	CGNAT      bool // no usable public address (carrier-grade NAT, double NAT)
	CGNATKnown bool
	Tunnel     bool // Cloudflare Tunnel configured and connected
	Tailscale  bool // signed in to Tailscale
	WireGuard  bool // WireGuard server enabled
	Domain     bool // app domains served with a public name
}

// Option is one way to reach the NAS, ranked for the goals.
type Option struct {
	Method      string   `json:"method"`
	Score       int      `json:"score"`
	Recommended bool     `json:"recommended"`
	Suitable    bool     `json:"suitable"`
	Configured  bool     `json:"configured"`
	Covers      []string `json:"covers"`  // goals it serves
	Missing     []string `json:"missing"` // goals it does not serve
	Notes       []string `json:"notes"`   // codes the interface explains
}

// covers lists which goals each method serves. Tailscale and WireGuard
// only let your own devices in, so they cannot serve public share links.
var covers = map[string][]string{
	MethodTunnel:    {GoalUI, GoalFiles, GoalPhotos, GoalLinks, GoalPhones},
	MethodTailscale: {GoalUI, GoalFiles, GoalPhotos, GoalPhones},
	MethodWireGuard: {GoalUI, GoalFiles, GoalPhotos, GoalPhones},
	MethodDomain:    {GoalUI, GoalFiles, GoalPhotos, GoalLinks, GoalPhones},
}

var notes = map[string][]string{
	MethodTunnel:    {"no-port", "account-cloudflare", "domain-needed", "public", "web-only", "upload-limit"},
	MethodTailscale: {"no-port", "account-tailscale", "private", "app-per-device", "smb-ok"},
	MethodWireGuard: {"port-udp", "no-account", "private", "app-per-device", "smb-ok"},
	MethodDomain:    {"port-tcp", "no-account", "domain-needed", "public", "web-only"},
}

// Recommend ranks the methods for the goals. With carrier-grade NAT, the
// methods that need a port forwarded cannot work.
func Recommend(goals []string, s Situation) []Option {
	var out []Option
	for _, m := range []string{MethodTailscale, MethodTunnel, MethodWireGuard, MethodDomain} {
		o := Option{Method: m, Suitable: true, Covers: []string{}, Missing: []string{}, Notes: slices.Clone(notes[m])}
		for _, g := range goals {
			if slices.Contains(covers[m], g) {
				o.Covers = append(o.Covers, g)
				o.Score += 10
			} else {
				o.Missing = append(o.Missing, g)
			}
		}
		needsPort := m == MethodWireGuard || m == MethodDomain
		if needsPort && s.CGNAT {
			o.Suitable = false
			o.Notes = append(o.Notes, "cgnat-blocks")
		} else if needsPort && !s.CGNATKnown {
			o.Notes = append(o.Notes, "cgnat-unknown")
		}
		if len(o.Covers) == 0 {
			o.Suitable = false
		}
		// Ease of setup, then fewer things exposed to the Internet.
		switch m {
		case MethodTailscale:
			o.Score += 4
		case MethodTunnel:
			o.Score += 3
		case MethodWireGuard:
			o.Score += 2
		}
		o.Configured = map[string]bool{MethodTunnel: s.Tunnel, MethodTailscale: s.Tailscale, MethodWireGuard: s.WireGuard, MethodDomain: s.Domain}[m]
		if o.Configured {
			o.Score += 5 // already working: no need for another one
		}
		if len(o.Missing) > 0 {
			o.Score -= 15 // a goal left out weighs more than ease
		}
		if !o.Suitable {
			o.Score = -100 + o.Score
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 0 && out[0].Suitable && len(goals) > 0 {
		out[0].Recommended = true
	}
	return out
}

// ---- Network facts ----

var (
	cgnatRange = netip.MustParsePrefix("100.64.0.0/10")
)

// IsCGNAT reports an address of the carrier-grade NAT range (RFC 6598).
func IsCGNAT(a netip.Addr) bool { return cgnatRange.Contains(a.Unmap()) }

// NotPublic reports an address that the Internet cannot reach directly.
func NotPublic(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsValid() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || IsCGNAT(a)
}

// NAT verdicts.
const (
	NATDirect  = "direct"  // the router holds the public address: port forwarding works
	NATCarrier = "cgnat"   // the provider shares the address: port forwarding cannot work
	NATDouble  = "double"  // a second router in front: forward on both or use bridge mode
	NATUnknown = "unknown" // the router did not say (no UPnP/NAT-PMP)
)

// ClassifyNAT compares the external address the router reports (UPnP or
// NAT-PMP) with the public address seen from the Internet.
func ClassifyNAT(router, public netip.Addr) string {
	router, public = router.Unmap(), public.Unmap()
	switch {
	case !router.IsValid():
		return NATUnknown
	case IsCGNAT(router):
		return NATCarrier
	case router.IsPrivate():
		return NATDouble
	case public.IsValid() && router != public:
		// The router has a public address but the Internet sees another:
		// the provider translates again.
		return NATCarrier
	}
	return NATDirect
}

// ---- Reachability diagnostics ----

// Probe holds what a reachability test observed, step by step.
type Probe struct {
	Host string
	Port int
	// DNS: the addresses given by public resolvers and by the NAS's own.
	PublicDNS    []netip.Addr
	PublicDNSErr bool // the public resolvers could not be asked
	LocalDNS     []netip.Addr
	PublicIP     netip.Addr // as seen from the Internet (may be unknown)
	Tunnel       bool       // a Cloudflare Tunnel is expected to serve the name
	NAT          string
	// Connection: "" ok, "refused", "timeout", "unreachable", "skipped".
	TCP string
	// TLS: "" ok, "untrusted", "expired", "name", "error", "skipped".
	TLS        string
	HTTPStatus int
	ThisNAS    bool // the answer carries this NAS's probe token
	ProbeKnown bool // the target is expected to answer the probe (StrataOS interface)
}

// Finding is one line of the diagnosis; Code is explained by the interface.
type Finding struct {
	Step   string `json:"step"`  // dns | nat | tcp | tls | http
	Level  string `json:"level"` // ok | warn | fail | info
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Verdicts of a reachability test.
const (
	Reachable   = "reachable"
	Unreachable = "unreachable"
	Partial     = "partial" // reached, with a problem (certificate…)
	Unknown     = "unknown" // could not be tested from the NAS itself
)

func addrList(l []netip.Addr) string {
	s := ""
	for i, a := range l {
		if i > 0 {
			s += ", "
		}
		s += a.String()
	}
	return s
}

// Diagnose turns the observations into findings and a verdict. The steps
// after a DNS problem still run (they tell whether the service answers), but
// a name the Internet cannot resolve is never reported reachable.
func Diagnose(p Probe) (string, []Finding) {
	verdict, f := diagnose(p)
	for _, x := range f {
		switch {
		case x.Code == "dns-local-only" || x.Code == "dns-private" || x.Code == "nat-cgnat":
			verdict = Unreachable
		case x.Code == "dns-public-unchecked" && verdict == Reachable:
			verdict = Unknown
		}
	}
	return verdict, f
}

func diagnose(p Probe) (string, []Finding) {
	var f []Finding
	add := func(step, level, code, detail string) {
		f = append(f, Finding{Step: step, Level: level, Code: code, Detail: detail})
	}
	switch {
	case len(p.PublicDNS) > 0:
		add("dns", "ok", "dns-ok", addrList(p.PublicDNS))
		if !p.Tunnel && p.PublicIP.IsValid() && !slices.Contains(p.PublicDNS, p.PublicIP) {
			add("dns", "warn", "dns-other-ip", addrList(p.PublicDNS)+" ≠ "+p.PublicIP.String())
		}
		if slices.ContainsFunc(p.PublicDNS, NotPublic) {
			add("dns", "fail", "dns-private", addrList(p.PublicDNS))
		}
	case len(p.LocalDNS) > 0 && !p.PublicDNSErr:
		add("dns", "fail", "dns-local-only", addrList(p.LocalDNS))
	case p.PublicDNSErr && len(p.LocalDNS) > 0:
		add("dns", "warn", "dns-public-unchecked", addrList(p.LocalDNS))
	default:
		add("dns", "fail", "dns-missing", p.Host)
		return Unreachable, f
	}
	if !p.Tunnel {
		switch p.NAT {
		case NATCarrier:
			add("nat", "fail", "nat-cgnat", "")
		case NATDouble:
			add("nat", "warn", "nat-double", "")
		case NATDirect:
			add("nat", "ok", "nat-direct", "")
		default:
			add("nat", "info", "nat-unknown", "")
		}
	}
	// From inside, a name pointing at our own public address goes through
	// the router's hairpin NAT: a failure may only mean that the router
	// does not loop back, not that the Internet cannot come in.
	hairpin := !p.Tunnel && p.PublicIP.IsValid() && slices.Contains(p.PublicDNS, p.PublicIP)
	switch p.TCP {
	case "":
		add("tcp", "ok", "tcp-ok", "")
	case "refused":
		if hairpin {
			add("tcp", "warn", "tcp-hairpin", "")
			return Unknown, f
		}
		add("tcp", "fail", "tcp-refused", "")
		return Unreachable, f
	case "timeout", "unreachable":
		if hairpin {
			add("tcp", "warn", "tcp-hairpin", "")
			return Unknown, f
		}
		add("tcp", "fail", "tcp-timeout", "")
		return Unreachable, f
	default:
		add("tcp", "info", "tcp-skipped", "")
		return Unknown, f
	}
	verdict := Reachable
	switch p.TLS {
	case "":
		add("tls", "ok", "tls-ok", "")
	case "untrusted":
		add("tls", "warn", "tls-untrusted", "")
		verdict = Partial
	case "expired":
		add("tls", "fail", "tls-expired", "")
		verdict = Partial
	case "name":
		add("tls", "fail", "tls-name", "")
		verdict = Partial
	case "skipped":
	default:
		add("tls", "fail", "tls-error", "")
		return Unreachable, f
	}
	switch {
	case p.HTTPStatus == 0:
		add("http", "fail", "http-none", "")
		return Unreachable, f
	case p.HTTPStatus == 502 || p.HTTPStatus == 503 || p.HTTPStatus == 504 || p.HTTPStatus == 530:
		// Cloudflare answers 502/530 when the tunnel is down or points nowhere.
		add("http", "fail", "http-gateway", "")
		return Unreachable, f
	case p.ProbeKnown && !p.ThisNAS:
		add("http", "warn", "http-other", "")
		return Partial, f
	case p.ProbeKnown:
		add("http", "ok", "http-this-nas", "")
	case p.HTTPStatus >= 500:
		add("http", "warn", "http-error", "")
		verdict = Partial
	default:
		add("http", "ok", "http-ok", "")
	}
	if hairpin && verdict != Unreachable {
		add("tcp", "info", "tested-from-inside", "")
	}
	return verdict, f
}
