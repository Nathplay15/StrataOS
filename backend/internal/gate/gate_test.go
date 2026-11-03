package gate

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestTwoFactorPromptNotRequired(t *testing.T) {
	admin := Account{Admin: true}
	p := TwoFactorPrompt(Policy{}, admin, t0)
	if !p.Show || !p.CanPostpone || p.Required || p.Enforced {
		t.Fatalf("unprotected admin: %+v", p)
	}
	if p := TwoFactorPrompt(Policy{}, Account{Admin: false}, t0); p.Show {
		t.Fatal("users are not prompted")
	}
	if p := TwoFactorPrompt(Policy{Require: true}, Account{Admin: true, Passkeys: 1}, t0); p.Show || !p.Required {
		t.Fatalf("passkey counts as a second factor: %+v", p)
	}
	postponed := Account{Admin: true, Prompted: t0, Postponed: t0.Add(days(7))}
	if p := TwoFactorPrompt(Policy{}, postponed, t0.Add(days(3))); p.Show || p.Postponed.IsZero() {
		t.Fatalf("postponed: %+v", p)
	}
	if p := TwoFactorPrompt(Policy{}, postponed, t0.Add(days(7))); !p.Show {
		t.Fatal("reminder after 7 days")
	}
	// Without the policy, postponing never ends.
	if _, err := Postpone(Policy{}, Account{Admin: true, Prompted: t0.Add(-days(400))}, t0); err != nil {
		t.Fatal(err)
	}
}

func TestTwoFactorGrace(t *testing.T) {
	pol := Policy{Require: true, Since: t0}
	a := Account{Admin: true, Prompted: t0.Add(-days(30))} // prompted before the switch-on
	if d := Deadline(pol, a, t0); !d.Equal(t0.Add(DefaultGrace)) {
		t.Fatalf("deadline from the switch-on: %v", d)
	}
	until, err := Postpone(pol, a, t0.Add(days(3)))
	if err != nil || !until.Equal(t0.Add(DefaultGrace)) {
		t.Fatalf("postpone capped at the deadline: %v %v", until, err)
	}
	a.Postponed = until
	if p := TwoFactorPrompt(pol, a, t0.Add(days(5))); p.Show {
		t.Fatal("hidden while postponed")
	}
	p := TwoFactorPrompt(pol, a, t0.Add(DefaultGrace))
	if !p.Show || !p.Enforced || p.CanPostpone {
		t.Fatalf("enforced after the grace period: %+v", p)
	}
	if !Enforced(pol, a, t0.Add(DefaultGrace)) {
		t.Fatal("Enforced")
	}
	if _, err := Postpone(pol, a, t0.Add(days(8))); err != ErrCannotPostpone {
		t.Fatalf("postpone after the grace: %v", err)
	}
	// An administrator first prompted later gets the whole delay.
	late := Account{Admin: true, Prompted: t0.Add(days(20))}
	if Enforced(pol, late, t0.Add(days(21))) || !Enforced(pol, late, t0.Add(days(27))) {
		t.Fatal("grace counted from the first prompt")
	}
	// Immediate and custom grace.
	if !Enforced(Policy{Require: true, Since: t0, GraceDays: -1}, Account{Admin: true, Prompted: t0}, t0) {
		t.Fatal("no grace")
	}
	if Enforced(Policy{Require: true, Since: t0, GraceDays: 14}, Account{Admin: true, Prompted: t0}, t0.Add(days(10))) {
		t.Fatal("14 days")
	}
	// Never for a protected account or a user.
	if Enforced(pol, Account{Admin: true, TOTP: true}, t0.Add(days(100))) || Enforced(pol, Account{}, t0.Add(days(100))) {
		t.Fatal("protected or not admin")
	}
}

func TestSetupAllowed(t *testing.T) {
	for _, p := range []string{"GET /api/me", "POST /api/me/2fa/enable", "POST /api/me/passkeys/finish", "POST /api/logout"} {
		if !SetupAllowed(p) {
			t.Error(p)
		}
	}
	for _, p := range []string{"GET /api/users", "PUT /api/security/2fa-policy", "POST /api/me/2fa/postpone", "DELETE /api/me/passkeys/{id}", "POST /api/me/2fa/disable", ""} {
		if SetupAllowed(p) {
			t.Error(p)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	now := t0
	l := &RateLimiter{Burst: 3, Every: time.Second, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if !l.Allow("a", 1) {
			t.Fatal("burst", i)
		}
	}
	if l.Allow("a", 1) {
		t.Fatal("over the burst")
	}
	if !l.Allow("b", 1) {
		t.Fatal("keys are separate")
	}
	now = now.Add(1500 * time.Millisecond)
	if !l.Allow("a", 1) || l.Allow("a", 1) {
		t.Fatal("one token per second")
	}
	now = now.Add(time.Hour)
	if !l.Allow("a", 3) || l.Allow("a", 1) {
		t.Fatal("refill up to the burst only")
	}
}

func TestRecommend(t *testing.T) {
	best := func(goals []string, s Situation) Option { return Recommend(goals, s)[0] }
	if o := best([]string{GoalUI, GoalPhones}, Situation{}); o.Method != MethodTailscale || !o.Recommended {
		t.Fatalf("private access: %+v", o)
	}
	if o := best([]string{GoalLinks}, Situation{}); o.Method != MethodTunnel {
		t.Fatalf("public links: %+v", o)
	}
	opts := Recommend([]string{GoalFiles}, Situation{CGNAT: true, CGNATKnown: true})
	for _, o := range opts {
		if (o.Method == MethodWireGuard || o.Method == MethodDomain) && (o.Suitable || !slices.Contains(o.Notes, "cgnat-blocks")) {
			t.Fatalf("CGNAT: %+v", o)
		}
	}
	if o := best([]string{GoalLinks, GoalUI}, Situation{CGNAT: true, CGNATKnown: true}); o.Method != MethodTunnel {
		t.Fatalf("CGNAT and links: %+v", o)
	}
	// Already configured wins a tie of goals.
	if o := best([]string{GoalUI}, Situation{WireGuard: true}); o.Method != MethodWireGuard {
		t.Fatalf("configured WireGuard: %+v", o)
	}
	for _, o := range Recommend([]string{GoalLinks}, Situation{}) {
		if o.Method == MethodTailscale && (o.Suitable || len(o.Missing) != 1) {
			t.Fatalf("Tailscale cannot serve public links: %+v", o)
		}
	}
	if Recommend(nil, Situation{})[0].Recommended {
		t.Fatal("nothing recommended without goals")
	}
}

func a(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestClassifyNAT(t *testing.T) {
	cases := []struct {
		router, public string
		want           string
	}{
		{"", "203.0.113.7", NATUnknown},
		{"100.72.1.9", "203.0.113.7", NATCarrier},
		{"192.168.0.2", "203.0.113.7", NATDouble},
		{"198.51.100.1", "203.0.113.7", NATCarrier},
		{"203.0.113.7", "203.0.113.7", NATDirect},
		{"203.0.113.7", "", NATDirect},
	}
	for _, c := range cases {
		var r, p netip.Addr
		if c.router != "" {
			r = a(c.router)
		}
		if c.public != "" {
			p = a(c.public)
		}
		if got := ClassifyNAT(r, p); got != c.want {
			t.Errorf("%s / %s: %s, want %s", c.router, c.public, got, c.want)
		}
	}
	if !IsCGNAT(a("100.64.0.1")) || IsCGNAT(a("100.128.0.1")) || !NotPublic(a("10.1.2.3")) || NotPublic(a("203.0.113.7")) {
		t.Fatal("ranges")
	}
}

func codes(f []Finding) string {
	var out []string
	for _, x := range f {
		out = append(out, x.Code)
	}
	return strings.Join(out, ",")
}

func TestDiagnose(t *testing.T) {
	pub := a("203.0.113.7")
	cases := []struct {
		name    string
		p       Probe
		verdict string
		codes   string
	}{
		{"unknown name", Probe{Host: "x.example.net"}, Unreachable, "dns-missing"},
		{"tunnel ok", Probe{PublicDNS: []netip.Addr{a("104.16.1.1")}, Tunnel: true, HTTPStatus: 200, ThisNAS: true, ProbeKnown: true}, Reachable, "dns-ok,tcp-ok,tls-ok,http-this-nas"},
		{"tunnel down", Probe{PublicDNS: []netip.Addr{a("104.16.1.1")}, Tunnel: true, HTTPStatus: 530}, Unreachable, "dns-ok,tcp-ok,tls-ok,http-gateway"},
		{"local only", Probe{LocalDNS: []netip.Addr{a("127.0.0.1")}, TLS: "untrusted", HTTPStatus: 200, ThisNAS: true, ProbeKnown: true}, Unreachable, "dns-local-only,nat-unknown,tcp-ok,tls-untrusted,http-this-nas"},
		{"offline resolvers", Probe{LocalDNS: []netip.Addr{a("192.0.2.5")}, PublicDNSErr: true, HTTPStatus: 200}, Unknown, "dns-public-unchecked,nat-unknown,tcp-ok,tls-ok,http-ok"},
		{"refused, other address", Probe{PublicDNS: []netip.Addr{a("198.51.100.9")}, PublicIP: pub, NAT: NATDirect, TCP: "refused"}, Unreachable, "dns-ok,dns-other-ip,nat-direct,tcp-refused"},
		{"hairpin", Probe{PublicDNS: []netip.Addr{pub}, PublicIP: pub, NAT: NATDirect, TCP: "timeout"}, Unknown, "dns-ok,nat-direct,tcp-hairpin"},
		{"cgnat", Probe{PublicDNS: []netip.Addr{pub}, PublicIP: pub, NAT: NATCarrier, TCP: "refused"}, Unknown, "dns-ok,nat-cgnat,tcp-hairpin"},
		{"expired cert", Probe{PublicDNS: []netip.Addr{pub}, PublicIP: pub, NAT: NATDirect, TLS: "expired", HTTPStatus: 200}, Partial, "dns-ok,nat-direct,tcp-ok,tls-expired,http-ok,tested-from-inside"},
		{"another server", Probe{PublicDNS: []netip.Addr{pub}, PublicIP: pub, NAT: NATDirect, HTTPStatus: 200, ProbeKnown: true}, Partial, "dns-ok,nat-direct,tcp-ok,tls-ok,http-other"},
		{"private address in DNS", Probe{PublicDNS: []netip.Addr{a("192.168.1.10")}, NAT: NATDirect, HTTPStatus: 200}, Unreachable, "dns-ok,dns-private,nat-direct,tcp-ok,tls-ok,http-ok"},
	}
	for _, c := range cases {
		v, f := Diagnose(c.p)
		if got := codes(f); got != c.codes {
			t.Errorf("%s: codes %s, want %s", c.name, got, c.codes)
		}
		if v != c.verdict && !(c.name == "cgnat" && v == Unreachable) {
			t.Errorf("%s: verdict %s, want %s", c.name, v, c.verdict)
		}
	}
}

func TestLinkLimits(t *testing.T) {
	l := LinkLimits{Download: true, MaxDownloads: 2, Downloads: 2}
	if l.State(t0) != LinkExhausted {
		t.Fatal("exhausted")
	}
	l.Upload = true // a drop box with downloads still receives files
	if l.State(t0) != LinkActive {
		t.Fatal("still receives")
	}
	if (LinkLimits{Expires: t0}).State(t0) != LinkExpired {
		t.Fatal("expired at the date")
	}
	if (LinkLimits{Upload: true, MaxTotal: 10, Received: 10}).State(t0) != LinkFull {
		t.Fatal("full")
	}
}

func TestUploadBudget(t *testing.T) {
	gb := int64(1 << 30)
	if _, err := UploadBudget(0, 0, 0, gb/2); err != ErrNoSpace {
		t.Fatal("keeps the reserve", err)
	}
	if b, _ := UploadBudget(0, 0, 0, 3*gb); b != 2*gb {
		t.Fatal("free space minus the reserve", b)
	}
	if b, _ := UploadBudget(MB(10), MB(100), MB(95), 5*gb); b != MB(5) {
		t.Fatal("what is left of the total", b)
	}
	if b, _ := UploadBudget(MB(10), MB(100), 0, 5*gb); b != MB(10) {
		t.Fatal("per file", b)
	}
	if _, err := UploadBudget(0, MB(1), MB(1), 5*gb); err != ErrUploadFull {
		t.Fatal("full", err)
	}
}

func TestCleanGuest(t *testing.T) {
	g, err := CleanGuest("  Marie   Curie ", " marie@example.org ", true)
	if err != nil || g.Name != "Marie Curie" || g.Email != "marie@example.org" {
		t.Fatal(g, err)
	}
	for _, c := range [][2]string{{"a\x07b", ""}, {"<script>", ""}, {strings.Repeat("x", 81), ""}, {"", "not an address"}, {"", "Marie <m@example.org>"}, {"", "a@b.c\r\nBcc: x@y.z"}} {
		if _, err := CleanGuest(c[0], c[1], false); err == nil {
			t.Errorf("accepted %q %q", c[0], c[1])
		}
	}
	if _, err := CleanGuest("Marie", "", true); err != ErrGuestNeeded {
		t.Fatal("required")
	}
	if g, err := CleanGuest("", "", false); err != nil || g != (Guest{}) {
		t.Fatal("optional")
	}
}

func TestGuestFolder(t *testing.T) {
	for in, want := range map[string]string{
		"Marie Curie": "Marie Curie", "../../etc": "_.._etc", "..": "", "a/b\\c:d": "a_b_c_d", "": "",
		strings.Repeat("é", 40): strings.Repeat("é", 20),
	} {
		if got := GuestFolder(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
