package firewall

import (
	"fmt"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// Apply loads both tables in one nftables transaction: either everything is
// replaced or nothing changes.
func Apply(rs Ruleset) error {
	if !sys.Has("nft") {
		if rs.Bridge == "" && rs.Host == "" {
			return nil
		}
		return fmt.Errorf("nftables is not installed")
	}
	if rs.Bridge != "" {
		// Connection tracking of bridged traffic (ct state in the bridge table).
		_, _ = sys.Run("modprobe", "nf_conntrack_bridge")
	}
	if out, err := sys.RunInput(Script(rs), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("nftables: %s", strings.TrimSpace(out+" "+err.Error()))
	}
	return nil
}

// Present reports whether the tables of a ruleset are loaded (another tool
// may have flushed the whole ruleset, e.g. nftables.service).
func Present(rs Ruleset) bool {
	if !sys.Has("nft") {
		return true
	}
	out, err := sys.Run("nft", "list", "tables")
	if err != nil {
		return true
	}
	has := func(fam, name string) bool {
		return strings.Contains(out, "table "+fam+" "+name+"\n") || strings.HasSuffix(out, "table "+fam+" "+name)
	}
	return (rs.Bridge == "" || has("bridge", BridgeTable)) && (rs.Host == "" || has("inet", HostTable))
}

// LogEntry is a packet dropped and logged by the firewall.
type LogEntry struct {
	Time  string `json:"time"`
	Scope string `json:"scope"` // host, vm.web, lxc.ha, docker.nginx, vnet.iot
	Dir   string `json:"dir,omitempty"`
	In    string `json:"in,omitempty"`
	Out   string `json:"out,omitempty"`
	Src   string `json:"src,omitempty"`
	Dst   string `json:"dst,omitempty"`
	Proto string `json:"proto,omitempty"`
	SPort int    `json:"sport,omitempty"`
	DPort int    `json:"dport,omitempty"`
}

// ParseLog reads kernel log lines (journalctl -k -o short-iso) and returns
// the firewall's, newest first.
func ParseLog(text string, limit int) []LogEntry {
	lines := strings.Split(text, "\n")
	var out []LogEntry
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		l := lines[i]
		at := strings.Index(l, LogPrefix)
		if at < 0 {
			continue
		}
		var e LogEntry
		if f := strings.Fields(l[:at]); len(f) > 0 {
			e.Time = f[0]
		}
		rest := l[at+len(LogPrefix):]
		tag, kv, _ := strings.Cut(rest, " ")
		if i := strings.LastIndex(tag, ":"); i > 0 {
			e.Scope, e.Dir = tag[:i], tag[i+1:]
		} else {
			e.Scope = tag
		}
		for _, f := range strings.Fields(kv) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch k {
			case "IN":
				e.In = v
			case "OUT":
				e.Out = v
			case "SRC":
				e.Src = v
			case "DST":
				e.Dst = v
			case "PROTO":
				e.Proto = v
			case "SPT":
				e.SPort, _ = strconv.Atoi(v)
			case "DPT":
				e.DPort, _ = strconv.Atoi(v)
			}
		}
		out = append(out, e)
	}
	return out
}

// ReadLog returns the last drops logged by the firewall.
func ReadLog(limit int) []LogEntry {
	out, err := sys.Run("journalctl", "-k", "-o", "short-iso", "--no-pager", "-n", "5000")
	if err != nil {
		return []LogEntry{}
	}
	l := ParseLog(out, limit)
	if l == nil {
		l = []LogEntry{}
	}
	return l
}
