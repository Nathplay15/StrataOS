package api

// Granular storage trends (admin): the space used per share and per account,
// its growth, and how long each volume still has before it fills up. Once a
// day the shares of each file system are measured together (one walk at low
// priority, each file once: a file hard-linked into several shares counts
// in one of them) and the accounts read from the user quotas; both go in
// the same history as the volumes (keys "share:<path>" and "user:<name>"),
// so the trends, the projections and the retention come for free.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"strataos/internal/health"
	"strataos/internal/storage"
	"strataos/internal/storagetools"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// shareUsageRunning keeps a single measurement at a time.
var shareUsageRunning atomic.Bool

const (
	shareUsageEvery   = 24 * time.Hour
	shareUsageTimeout = 6 * time.Hour // the whole measurement
	shareUsageDuMax   = 2 * time.Hour // one share
)

func (s *Server) shareUsagePath() string { return filepath.Join(s.dataDir, "storage-trends.json") }

// shareUsageDue tells whether a day passed since the last measurement,
// remembered on disk so that a restart does not start it over.
func (s *Server) shareUsageDue(now time.Time) bool {
	var last struct {
		At time.Time `json:"at"`
	}
	if b, err := os.ReadFile(s.shareUsagePath()); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	return now.Sub(last.At) >= shareUsageEvery || last.At.After(now.Add(time.Hour))
}

// recordShareUsage starts the daily measurement in the background (the health
// watcher must not wait for it). asleep are the disks found in standby by the
// SMART check just before: the volumes on them are left for another day.
func (s *Server) recordShareUsage(asleep []string) {
	if s.hist == nil || !s.shareUsageDue(time.Now()) || !shareUsageRunning.CompareAndSwap(false, true) {
		return
	}
	b, _ := json.Marshal(map[string]time.Time{"at": time.Now()})
	if err := sys.WriteFileAtomic(s.shareUsagePath(), b, 0o600); err != nil {
		shareUsageRunning.Store(false)
		return
	}
	s.bg.Go(func() {
		defer shareUsageRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), shareUsageTimeout)
		defer cancel()
		s.measureShareUsage(ctx, sleepingFileSystems(asleep))
	})
}

// measureShareUsage records the usage of the shares and the accounts, and
// forgets the histories of the shares and accounts that no longer exist.
func (s *Server) measureShareUsage(ctx context.Context, asleep map[string]bool) {
	var paths, users []string
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			paths = append(paths, filepath.Clean(sh.Path))
		}
		for _, u := range st.Users {
			users = append(users, u.Username)
		}
	})
	usage := map[string]health.VolumePoint{}
	// The shares of one file system are walked together: a hard link
	// between two shares is one file.
	byFS := map[string][]string{}
	var order []string
	for _, p := range paths {
		fsID := fileSystemOf(p)
		if len(asleep) > 0 && asleep[fsID] {
			continue // measured another day rather than waking the disks
		}
		key := fsID
		if key == "" {
			key = "path:" + p // unknown: on its own
		}
		if _, ok := byFS[key]; !ok {
			order = append(order, key)
		}
		byFS[key] = append(byFS[key], p)
	}
	for _, key := range order {
		if ctx.Err() != nil {
			break
		}
		c, cancel := context.WithTimeout(ctx, shareUsageDuMax*time.Duration(len(byFS[key])))
		got, err := storagetools.MeasureShares(c, byFS[key])
		cancel()
		if err != nil {
			slog.Warn("storage trends", "shares", byFS[key], "err", err)
		}
		for p, used := range got {
			usage["share:"+p] = health.VolumePoint{Used: used}
		}
	}
	// Accounts: what the user quotas already count, on every volume where
	// they are on. A volume asleep would make a sum too small: skipped then.
	var targets []storage.QuotaTarget
	partial := false
	for _, t := range s.quotaTargets() {
		if !t.Supported || !t.Enabled {
			continue
		}
		if len(asleep) > 0 && asleep[fileSystemOf(t.MountPoint)] {
			partial = true
		}
		targets = append(targets, t)
	}
	if len(targets) > 0 && !partial && ctx.Err() == nil {
		for _, u := range users {
			var used int64
			for _, t := range targets {
				n, _ := storage.GetUserQuota(t, u)
				used += n
			}
			usage["user:"+u] = health.VolumePoint{Used: used} // 0 included
		}
	}
	s.hist.RecordVolumes(usage, time.Now())
	keep := map[string]bool{}
	for _, p := range paths {
		keep["share:"+p] = true
	}
	for _, u := range users {
		keep["user:"+u] = true
	}
	s.hist.Forget(func(key string) bool {
		return (strings.HasPrefix(key, "share:") || strings.HasPrefix(key, "user:")) && !keep[key]
	})
}

// lsblkNode is a block device of `lsblk -J`, with what it holds.
type lsblkNode struct {
	FSType   string      `json:"fstype"`
	UUID     string      `json:"uuid"`
	Label    string      `json:"label"`
	Children []lsblkNode `json:"children"`
}

// fsIdentities lists the file systems held by a disk (lsblk -J of that
// disk): "uuid:<uuid>" for a file system on it, through partitions, RAID,
// LVM or encryption, and "zfs:<pool>" for a ZFS pool it belongs to.
func fsIdentities(lsblkJSON []byte) []string {
	var j struct {
		Blockdevices []lsblkNode `json:"blockdevices"`
	}
	if json.Unmarshal(lsblkJSON, &j) != nil {
		return nil
	}
	var out []string
	var walk func(n lsblkNode)
	walk = func(n lsblkNode) {
		switch {
		case n.FSType == "zfs_member" && n.Label != "":
			out = append(out, "zfs:"+n.Label)
		case n.UUID != "" && n.FSType != "" && !strings.HasSuffix(n.FSType, "_member") && n.FSType != "crypto_LUKS":
			out = append(out, "uuid:"+n.UUID)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, n := range j.Blockdevices {
		walk(n)
	}
	return out
}

// sleepingFileSystems are the file systems that sit on the given disks.
func sleepingFileSystems(disks []string) map[string]bool {
	out := map[string]bool{}
	for _, d := range disks {
		b, err := sys.Run("lsblk", "-J", "-p", "-o", "NAME,FSTYPE,UUID,LABEL", d)
		if err != nil {
			continue
		}
		for _, id := range fsIdentities([]byte(b)) {
			out[id] = true
		}
	}
	return out
}

// fileSystemOf identifies the file system holding p like fsIdentities
// ("" when unknown); findmnt reads the mount table only, no disk.
func fileSystemOf(p string) string {
	out, err := sys.Run("findmnt", "-n", "-o", "FSTYPE,SOURCE,UUID", "-T", p)
	if err != nil {
		return ""
	}
	return fsIdentityOf(out)
}

func fsIdentityOf(findmnt string) string {
	f := strings.Fields(findmnt)
	if len(f) < 2 {
		return ""
	}
	if f[0] == "zfs" {
		pool, _, _ := strings.Cut(f[1], "/")
		return "zfs:" + pool
	}
	if len(f) < 3 {
		return ""
	}
	return "uuid:" + f[2]
}

// trendRow is one line of the granular trends: a volume, a share or an
// account, its current use and its growth (bytes over the period).
type trendRow struct {
	Name      string  `json:"name"`
	Path      string  `json:"path,omitempty"`
	Used      int64   `json:"used"`
	Total     int64   `json:"total,omitempty"`
	Growth7d  int64   `json:"growth7d,omitempty"`
	Growth30d int64   `json:"growth30d,omitempty"`
	FullIn    float64 `json:"fullIn,omitempty"` // days before the volume is full
}

func trendRowOf(name string, pts []health.VolumePoint, now time.Time) (t trendRow) {
	t.Name = name
	if n := len(pts); n > 0 {
		t.Used = pts[n-1].Used
	}
	t.Growth7d, _ = growthOf(pts, 7*24*time.Hour, now)
	t.Growth30d, _ = growthOf(pts, 30*24*time.Hour, now)
	return t
}

// growthOf is health.Growth over a period, 0 when the history is too short.
func growthOf(pts []health.VolumePoint, period time.Duration, now time.Time) (int64, bool) {
	d, ok := health.Growth(pts, now.Add(-period))
	if !ok {
		return 0, false
	}
	return d, true
}

// trendsRoutes gives the granular trends of the storage (admin).
func (s *Server) trendsRoutes() {
	s.admin("GET /api/storage/trends", func(r *http.Request) (any, error) {
		if s.hist == nil {
			return nil, notFound("history not found")
		}
		now := time.Now()
		raids, _ := storage.ListRaid()
		pools, _ := storage.ListZPools(false)
		volumes := []trendRow{}
		for _, v := range s.volumeSummaries(raids, pools) {
			t := trendRow{Path: v.MountPoint, Used: v.Used, Total: v.Total}
			t.Name = v.Name
			if v.Total > 0 {
				t.Used, t.Total = v.Used, v.Total
			}
			h := s.hist.Volume(v.Name)
			t.Growth7d, _ = growthOf(h, 7*24*time.Hour, now)
			t.Growth30d, _ = growthOf(h, 30*24*time.Hour, now)
			if days, ok := health.FullIn(h, v.Total, now); ok {
				t.FullIn = days
			}
			volumes = append(volumes, t)
		}
		var list []store.Share
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				list = append(list, *sh)
			}
		})
		shares := []trendRow{}
		for _, sh := range list {
			if pts := s.hist.Volume("share:" + filepath.Clean(sh.Path)); len(pts) > 0 {
				row := trendRowOf(sh.Name, pts, now)
				row.Path = sh.Path
				shares = append(shares, row)
			}
		}
		users := []trendRow{}
		for key, pts := range s.hist.Series("user:") {
			users = append(users, trendRowOf(strings.TrimPrefix(key, "user:"), pts, now))
		}
		sortTrends(shares)
		sortTrends(users)
		return map[string]any{"volumes": volumes, "shares": shares, "users": users}, nil
	})

	// The usage history of a volume or a share (the chart of the trends).
	s.admin("GET /api/storage/trends/history", func(r *http.Request) (any, error) {
		name := r.URL.Query().Get("name")
		if name == "" {
			return nil, badRequest("choose a volume or a share")
		}
		if s.hist == nil {
			return nil, notFound("history not found")
		}
		pts := s.hist.Volume(name)
		if pts == nil {
			return nil, notFound("history not found")
		}
		return map[string]any{"points": pts}, nil
	})
}

// sortTrends puts the biggest users first.
func sortTrends(rows []trendRow) {
	slices.SortFunc(rows, func(a, b trendRow) int { return cmp.Compare(b.Used, a.Used) })
}
