package lxc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"strataos/internal/sys"
)

// Unprivileged containers: root of the NAS owns a range of subordinate ids
// (/etc/subuid, /etc/subgid) that container ids are shifted into. One range
// is shared by all containers, like Proxmox does: a process escaping its
// container is still an unprivileged user of the NAS. The range is chosen
// after every range already given out (useradd gives one to each new account
// from 100000 up), so it never overlaps one.

var (
	SubUIDFile = "/etc/subuid"
	SubGIDFile = "/etc/subgid"
	subMu      sync.Mutex
)

type subRange struct {
	Owner        string
	Start, Count int
}

func parseSubIDs(content string) []subRange {
	var out []subRange
	for _, l := range strings.Split(content, "\n") {
		f := strings.Split(strings.TrimSpace(l), ":")
		if len(f) != 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || start < 0 || count <= 0 {
			continue
		}
		out = append(out, subRange{f[0], start, count})
	}
	return out
}

// pickBase returns root's existing range when it is big enough, or the first
// free start after every range of both files (aligned on 65536).
func pickBase(uids, gids []subRange, count int) int {
	for _, r := range uids {
		if r.Owner == "root" && r.Count >= count && r.Start >= 65536 {
			for _, g := range gids {
				if g.Owner == "root" && g.Start == r.Start && g.Count >= count {
					return r.Start
				}
			}
		}
	}
	end := 100000
	for _, list := range [][]subRange{uids, gids} {
		for _, r := range list {
			if r.Count >= 65536 || r.Start >= 100000 {
				end = max(end, r.Start+r.Count)
			}
		}
	}
	if end%65536 != 0 {
		end += 65536 - end%65536
	}
	return end
}

// hasRange reports whether owner holds [start, start+count).
func hasRange(list []subRange, owner string, start, count int) bool {
	for _, r := range list {
		if r.Owner == owner && r.Start <= start && start+count <= r.Start+r.Count {
			return true
		}
	}
	return false
}

// EnsureIDMap returns the id mapping for a new unprivileged container and
// records root's ranges (plus the passthrough ids) in /etc/subuid and /etc/subgid.
func EnsureIDMap(uids, gids []int) (IDMap, error) {
	subMu.Lock()
	defer subMu.Unlock()
	u, _ := os.ReadFile(SubUIDFile)
	g, _ := os.ReadFile(SubGIDFile)
	ur, gr := parseSubIDs(string(u)), parseSubIDs(string(g))
	m := IDMap{Base: pickBase(ur, gr, DefaultIDCount), Count: DefaultIDCount, UIDs: uids, GIDs: gids}
	if err := m.validate(); err != nil {
		return m, err
	}
	return m, recordIDs(m)
}

// recordIDs adds the missing root ranges of a mapping to the subid files.
func recordIDs(m IDMap) error {
	for _, f := range []struct {
		path string
		ids  []int
	}{{SubUIDFile, m.UIDs}, {SubGIDFile, m.GIDs}} {
		b, err := os.ReadFile(f.path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		content := string(b)
		ranges := parseSubIDs(content)
		var add []string
		if !hasRange(ranges, "root", m.Base, m.Count) {
			add = append(add, fmt.Sprintf("root:%d:%d", m.Base, m.Count))
		}
		for _, id := range f.ids {
			if !hasRange(ranges, "root", id, 1) {
				add = append(add, fmt.Sprintf("root:%d:1", id))
			}
		}
		if len(add) == 0 {
			continue
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += strings.Join(add, "\n") + "\n"
		if err := sys.WriteFileAtomic(f.path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
