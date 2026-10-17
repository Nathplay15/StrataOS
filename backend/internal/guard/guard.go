// Package guard blocks the addresses that fail to sign in too often (web,
// WebDAV, share links, SSH/SFTP, SMB, FTP) and can restrict access to some
// countries, with an nftables table of its own (inet strata_guard).
package guard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

// Config is the protection set by the administrator.
type Config struct {
	Enabled     bool     `json:"enabled"`
	MaxFailures int      `json:"maxFailures"` // failures …
	WindowMin   int      `json:"windowMin"`   // … within this many minutes
	BlockMin    int      `json:"blockMin"`    // first block; doubled for repeat offenders (7 days at most)
	NeverLAN    bool     `json:"neverLan"`    // private and link-local addresses are never blocked
	Allow       []string `json:"allow,omitempty"`
	Geo         Geo      `json:"geo"`
}

// Geo limits access from the Internet to some countries.
type Geo struct {
	Enabled   bool      `json:"enabled"`
	Countries []string  `json:"countries,omitempty"` // ISO codes, lower case
	Updated   time.Time `json:"updated,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Block is an address refused until a date.
type Block struct {
	IP      string    `json:"ip"`
	Until   time.Time `json:"until"`
	At      time.Time `json:"at"`
	Service string    `json:"service"`
	User    string    `json:"user,omitempty"`
	Count   int       `json:"count"` // times blocked (escalation)
	Manual  bool      `json:"manual,omitempty"`
}

// Defaults fills unset values.
func (c *Config) Defaults() {
	if c.MaxFailures <= 0 {
		c.MaxFailures = 5
	}
	if c.WindowMin <= 0 {
		c.WindowMin = 10
	}
	if c.BlockMin <= 0 {
		c.BlockMin = 60
	}
}

var ccRe = regexp.MustCompile(`^[a-z]{2}$`)

// Validate checks the settings.
func (c Config) Validate() error {
	if c.MaxFailures < 1 || c.MaxFailures > 1000 || c.WindowMin < 1 || c.WindowMin > 1440 || c.BlockMin < 1 || c.BlockMin > 10080 {
		return fmt.Errorf("invalid limits")
	}
	for _, a := range c.Allow {
		if _, err := netip.ParsePrefix(cidr(a)); err != nil {
			return fmt.Errorf("invalid address %q", a)
		}
	}
	for _, cc := range c.Geo.Countries {
		if !ccRe.MatchString(cc) {
			return fmt.Errorf("invalid country %q", cc)
		}
	}
	if c.Geo.Enabled && len(c.Geo.Countries) == 0 {
		return fmt.Errorf("choose at least one country")
	}
	return nil
}

// cidr turns a bare address into a single-address prefix.
func cidr(a string) string {
	a = strings.TrimSpace(a)
	if strings.Contains(a, "/") {
		return a
	}
	if strings.Contains(a, ":") {
		return a + "/128"
	}
	return a + "/32"
}

var private = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("::1/128"),
}

// Private reports whether an address belongs to a local network.
func Private(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range private {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParseIP accepts "1.2.3.4", "::ffff:1.2.3.4", "[::1]:22", "ipv4:1.2.3.4:445".
func ParseIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "ipv4:"); ok {
		s = rest
	} else if rest, ok := strings.CutPrefix(s, "ipv6:"); ok {
		// Samba writes ipv6:ADDR:PORT: the port is always last.
		if i := strings.LastIndex(rest, ":"); i > 0 {
			if a, err := netip.ParseAddr(strings.Trim(rest[:i], "[]")); err == nil {
				return a.Unmap(), true
			}
		}
		s = rest
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	// ipv4:1.2.3.4:445 or ipv6:fe80::1:445 (Samba)
	if i := strings.LastIndex(s, ":"); i > 0 {
		if a, err := netip.ParseAddr(strings.Trim(s[:i], "[]")); err == nil {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// Engine counts the failures and decides the blocks.
type Engine struct {
	mu       sync.Mutex
	failures map[netip.Addr][]time.Time
}

func NewEngine() *Engine { return &Engine{failures: map[netip.Addr][]time.Time{}} }

// Exempt tells whether an address must never be blocked.
func Exempt(c Config, a netip.Addr) bool {
	if c.NeverLAN && Private(a) {
		return true
	}
	if a.IsLoopback() {
		return true
	}
	for _, s := range c.Allow {
		if p, err := netip.ParsePrefix(cidr(s)); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// Fail records a failure; it returns true when the address must be blocked.
func (e *Engine) Fail(c Config, a netip.Addr, now time.Time) bool {
	if !c.Enabled || Exempt(c, a) {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.failures) > 50000 {
		e.failures = map[netip.Addr][]time.Time{}
	}
	cut := now.Add(-time.Duration(c.WindowMin) * time.Minute)
	l := slices.DeleteFunc(e.failures[a], func(t time.Time) bool { return t.Before(cut) })
	l = append(l, now)
	if len(l) >= c.MaxFailures {
		delete(e.failures, a)
		return true
	}
	e.failures[a] = l
	return false
}

// Forget clears the failures of an address.
func (e *Engine) Forget(a netip.Addr) {
	e.mu.Lock()
	delete(e.failures, a)
	e.mu.Unlock()
}

// Duration of the n-th block (1, 2, …): doubled each time, 7 days at most.
func Duration(c Config, n int) time.Duration {
	d := time.Duration(c.BlockMin) * time.Minute
	for i := 1; i < n && d < 7*24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 7*24*time.Hour)
}

// ---- nftables ----

// Render returns the nftables table for the active blocks and countries.
func Render(c Config, blocks []Block, geo4, geo6 []string, now time.Time) string {
	var b strings.Builder
	b.WriteString("table inet strata_guard\ndelete table inet strata_guard\ntable inet strata_guard {\n")
	set := func(name, typ string, flags string, elems []string) {
		fmt.Fprintf(&b, "  set %s {\n    type %s\n    flags %s\n", name, typ, flags)
		if len(elems) > 0 {
			fmt.Fprintf(&b, "    elements = { %s }\n", strings.Join(elems, ", "))
		}
		b.WriteString("  }\n")
	}
	var b4, b6 []string
	for _, bl := range blocks {
		a, ok := ParseIP(bl.IP)
		left := bl.Until.Sub(now).Truncate(time.Second)
		if !ok || left <= 0 {
			continue
		}
		el := fmt.Sprintf("%s timeout %ds", a, int(left.Seconds()))
		if a.Is4() {
			b4 = append(b4, el)
		} else {
			b6 = append(b6, el)
		}
	}
	set("blocked4", "ipv4_addr", "timeout", b4)
	set("blocked6", "ipv6_addr", "timeout", b6)
	var a4, a6 []string
	for _, s := range c.Allow {
		if p, err := netip.ParsePrefix(cidr(s)); err == nil {
			if p.Addr().Is4() {
				a4 = append(a4, p.Masked().String())
			} else {
				a6 = append(a6, p.Masked().String())
			}
		}
	}
	set("allow4", "ipv4_addr", "interval", a4)
	set("allow6", "ipv6_addr", "interval", a6)
	var l4, l6 []string
	for _, p := range private {
		if p.Addr().Is4() {
			l4 = append(l4, p.String())
		} else {
			l6 = append(l6, p.String())
		}
	}
	set("lan4", "ipv4_addr", "interval", l4)
	set("lan6", "ipv6_addr", "interval", l6)
	geo := c.Geo.Enabled && len(geo4)+len(geo6) > 0
	if geo {
		set("geo4", "ipv4_addr", "interval", geo4)
		set("geo6", "ipv6_addr", "interval", geo6)
	}
	// Same rules for the NAS itself (input) and its containers (forward).
	for _, hook := range []string{"input", "forward"} {
		fmt.Fprintf(&b, "  chain %s {\n    type filter hook %s priority filter - 5; policy accept;\n    iif \"lo\" accept\n    ip saddr @allow4 accept\n    ip6 saddr @allow6 accept\n    ct state established,related accept\n", hook, hook)
		if c.NeverLAN {
			b.WriteString("    ip saddr @lan4 accept\n    ip6 saddr @lan6 accept\n")
		}
		b.WriteString("    ip saddr @blocked4 drop\n    ip6 saddr @blocked6 drop\n")
		if geo {
			b.WriteString("    ip saddr @lan4 accept\n    ip6 saddr @lan6 accept\n    ip saddr @geo4 accept\n    ip6 saddr @geo6 accept\n    meta l4proto { tcp, udp, icmp, ipv6-icmp } counter drop\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// Apply loads the table, or removes it when the protection is off.
func Apply(c Config, blocks []Block, geo4, geo6 []string) error {
	if !sys.Has("nft") {
		return fmt.Errorf("nftables is not installed")
	}
	if !c.Enabled {
		_, _ = sys.Run("nft", "delete", "table", "inet", "strata_guard")
		return nil
	}
	_, err := sys.RunInput(Render(c, blocks, geo4, geo6, time.Now()), "nft", "-f", "-")
	return err
}

// AddBlock adds one address to the running table.
func AddBlock(a netip.Addr, d time.Duration) error {
	set := "blocked4"
	if a.Is6() {
		set = "blocked6"
	}
	_, err := sys.Run("nft", "add", "element", "inet", "strata_guard", set, fmt.Sprintf("{ %s timeout %ds }", a, int(d.Seconds())))
	return err
}

// RemoveBlock takes an address out of the running table.
func RemoveBlock(a netip.Addr) {
	set := "blocked4"
	if a.Is6() {
		set = "blocked6"
	}
	_, _ = sys.Run("nft", "delete", "element", "inet", "strata_guard", set, fmt.Sprintf("{ %s }", a))
}

// ---- Country ranges ----

// GeoSources are the lists of address ranges per country (aggregated zones).
var GeoSources = [2]string{
	"https://www.ipdeny.com/ipblocks/data/aggregated/{cc}-aggregated.zone",
	"https://www.ipdeny.com/ipv6/ipaddresses/aggregated/{cc}-aggregated.zone",
}

// GeoDir caches the downloaded lists.
var GeoDir = "/var/lib/strata/geo"

func readRanges(r io.Reader, v6 bool) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		p, err := netip.ParsePrefix(l)
		if err != nil || p.Addr().Is6() != v6 {
			continue
		}
		out = append(out, p.Masked().String())
	}
	return out
}

// FetchCountries downloads (or reads from the cache when fresh) the ranges.
func FetchCountries(ctx context.Context, countries []string, refresh bool) (v4, v6 []string, err error) {
	_ = os.MkdirAll(GeoDir, 0o700)
	cl := &http.Client{Timeout: 60 * time.Second}
	sources := GeoSources
	if env := os.Getenv("STRATA_GEO_SOURCES"); env != "" { // tests: "v4-template|v6-template"
		if a, b, ok := strings.Cut(env, "|"); ok {
			sources = [2]string{a, b}
		}
	}
	for _, cc := range countries {
		for i, tpl := range sources {
			file := filepath.Join(GeoDir, fmt.Sprintf("%s.v%d", cc, 4+2*i))
			if fi, e := os.Stat(file); e != nil || refresh || time.Since(fi.ModTime()) > 7*24*time.Hour {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.ReplaceAll(tpl, "{cc}", cc), nil)
				resp, e := cl.Do(req)
				if e == nil && resp.StatusCode == http.StatusOK {
					body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
					resp.Body.Close()
					if len(readRanges(strings.NewReader(string(body)), i == 1)) > 0 {
						_ = sys.WriteFileAtomic(file, body, 0o600)
					}
				} else {
					if resp != nil {
						resp.Body.Close()
					}
					if i == 0 { // IPv4 is required, IPv6 lists may not exist
						if _, statErr := os.Stat(file); statErr != nil {
							return nil, nil, fmt.Errorf("cannot download the address ranges of %s", strings.ToUpper(cc))
						}
					}
				}
			}
			f, e := os.Open(file)
			if e != nil {
				continue
			}
			if i == 0 {
				v4 = append(v4, readRanges(f, false)...)
			} else {
				v6 = append(v6, readRanges(f, true)...)
			}
			f.Close()
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return slices.Compact(v4), slices.Compact(v6), nil
}

// Valid reports whether s is a usable IP address (for manual blocks).
func Valid(s string) bool { return net.ParseIP(strings.TrimSpace(s)) != nil }
