package storage

import (
	"regexp"
	"strconv"
	"strings"
)

// ZLeaf is a disk (or partition) of a ZFS vdev as `zpool status -P` lists it.
type ZLeaf struct {
	Name  string `json:"name"` // device path, or GUID when the device is missing
	State string `json:"state"`
	Was   string `json:"was,omitempty"` // former path of a missing device
	// Replacing: the leaf is one side of a "replacing-N" pair (resilver running).
	Replacing bool `json:"replacing,omitempty"`
}

// ZVdev is a top-level data vdev of a pool (log, cache and spare devices are left out).
type ZVdev struct {
	Name   string  `json:"name"` // mirror-0, raidz1-1, or the leaf name of a single-disk vdev
	Type   string  `json:"type"` // mirror | raidz1 | raidz2 | raidz3 | stripe
	State  string  `json:"state"`
	Leaves []ZLeaf `json:"leaves"`
}

var zVdevTypeRe = regexp.MustCompile(`^(mirror|raidz[123]?|draid[123]?[^-]*)-\d+$`)

// ParseZTopology reads the data vdevs of a pool from `zpool status -P` output.
func ParseZTopology(status string) []ZVdev {
	var out []ZVdev
	inConfig, skip := false, false
	pool := ""
	var cur *ZVdev
	for _, l := range strings.Split(status, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "NAME") && strings.Contains(t, "STATE") {
			inConfig = true
			continue
		}
		if !inConfig {
			continue
		}
		if t == "" || strings.HasPrefix(t, "errors:") {
			if strings.HasPrefix(t, "errors:") {
				break
			}
			continue
		}
		// Config lines start with a tab, then two spaces per level.
		body := strings.TrimPrefix(l, "\t")
		depth := (len(body) - len(strings.TrimLeft(body, " "))) / 2
		f := strings.Fields(t)
		name, state := f[0], ""
		if len(f) > 1 {
			state = f[1]
		}
		was := ""
		if i := strings.Index(t, "was "); i >= 0 {
			was = strings.TrimSpace(t[i+4:])
		}
		switch {
		case depth == 0:
			if pool == "" {
				pool = name
				skip = false
			} else {
				// logs, cache, spares, special, dedup: not data vdevs.
				skip = true
			}
			cur = nil
		case skip:
		case depth == 1:
			switch m := zVdevTypeRe.FindStringSubmatch(name); {
			case m != nil:
				typ := m[1]
				if typ == "raidz" {
					typ = "raidz1"
				}
				out = append(out, ZVdev{Name: name, Type: typ, State: state})
			case strings.HasPrefix(name, "replacing-"):
				// A single-disk vdev being replaced: its leaves follow at depth 2.
				out = append(out, ZVdev{Name: name, Type: "stripe", State: state})
			default:
				out = append(out, ZVdev{Name: name, Type: "stripe", State: state, Leaves: []ZLeaf{{Name: name, State: state, Was: was}}})
			}
			cur = &out[len(out)-1]
		case depth == 2 && cur != nil:
			if strings.HasPrefix(name, "replacing-") || strings.HasPrefix(name, "spare-") {
				continue // its children (depth 3) are the leaves
			}
			cur.Leaves = append(cur.Leaves, ZLeaf{Name: name, State: state, Was: was, Replacing: strings.HasPrefix(cur.Name, "replacing-")})
		case depth == 3 && cur != nil:
			cur.Leaves = append(cur.Leaves, ZLeaf{Name: name, State: state, Was: was, Replacing: true})
		}
	}
	return out
}

var (
	zDoneRe = regexp.MustCompile(`([0-9.]+)% done`)
	zToGoRe = regexp.MustCompile(`(?:(\d+) days? )?(\d+):(\d+):(\d+) to go`)
)

// ParseZScanProgress reads the percentage and the time left (seconds, 0 when
// unknown) of a running scrub or resilver from `zpool status` output.
func ParseZScanProgress(status string) (pct float64, eta int64) {
	if m := zDoneRe.FindStringSubmatch(status); m != nil {
		pct, _ = strconv.ParseFloat(m[1], 64)
	}
	if m := zToGoRe.FindStringSubmatch(status); m != nil {
		d, _ := strconv.ParseInt(m[1], 10, 64)
		h, _ := strconv.ParseInt(m[2], 10, 64)
		mi, _ := strconv.ParseInt(m[3], 10, 64)
		s, _ := strconv.ParseInt(m[4], 10, 64)
		eta = d*86400 + h*3600 + mi*60 + s
	}
	return
}

// ParseMdFinish converts the "finish=" value of /proc/mdstat (12.3min, 45sec,
// 2.5hour…) to seconds.
func ParseMdFinish(s string) int64 {
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	switch unit := s[i:]; {
	case strings.HasPrefix(unit, "min"):
		n *= 60
	case strings.HasPrefix(unit, "h"):
		n *= 3600
	case strings.HasPrefix(unit, "d"):
		n *= 86400
	}
	return int64(n)
}
