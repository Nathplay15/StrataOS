package guard

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestEngine(t *testing.T) {
	c := Config{Enabled: true, NeverLAN: true}
	c.Defaults()
	e := NewEngine()
	now := time.Now()
	pub := netip.MustParseAddr("203.0.113.9")
	for i := 0; i < 4; i++ {
		if e.Fail(c, pub, now) {
			t.Fatal("blocked too early")
		}
	}
	if !e.Fail(c, pub, now) {
		t.Fatal("not blocked after 5 failures")
	}
	lan := netip.MustParseAddr("192.168.1.20")
	for i := 0; i < 10; i++ {
		if e.Fail(c, lan, now) {
			t.Fatal("LAN address blocked")
		}
	}
	// Old failures expire.
	other := netip.MustParseAddr("198.51.100.1")
	for i := 0; i < 4; i++ {
		e.Fail(c, other, now.Add(-time.Hour))
	}
	if e.Fail(c, other, now) {
		t.Fatal("expired failures counted")
	}
	if Duration(c, 1) != time.Hour || Duration(c, 3) != 4*time.Hour || Duration(c, 20) != 7*24*time.Hour {
		t.Error("durations")
	}
}

func TestParseIP(t *testing.T) {
	for in, want := range map[string]string{"ipv4:10.0.2.2:40922": "10.0.2.2", "::ffff:1.2.3.4": "1.2.3.4", "[2001:db8::1]:22": "2001:db8::1", "ipv6:2001:db8::5:445": "2001:db8::5"} {
		if a, ok := ParseIP(in); !ok || a.String() != want {
			t.Errorf("ParseIP(%q) = %v %v", in, a, ok)
		}
	}
}

func TestRender(t *testing.T) {
	c := Config{Enabled: true, NeverLAN: true, Allow: []string{"203.0.113.0/24"}, Geo: Geo{Enabled: true, Countries: []string{"fr"}}}
	c.Defaults()
	now := time.Now()
	out := Render(c, []Block{{IP: "198.51.100.7", Until: now.Add(time.Hour)}, {IP: "198.51.100.8", Until: now.Add(-time.Minute)}}, []string{"2.0.0.0/12"}, nil, now)
	for _, want := range []string{"198.51.100.7 timeout 3600s", "203.0.113.0/24", "2.0.0.0/12", "hook input", "hook forward", "ip saddr @geo4 accept"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "198.51.100.8") {
		t.Error("expired block rendered")
	}
	if (Config{MaxFailures: 5, WindowMin: 10, BlockMin: 60, Geo: Geo{Enabled: true}}).Validate() == nil {
		t.Error("geo without countries accepted")
	}
}
