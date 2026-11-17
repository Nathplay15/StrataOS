package api

// Granular storage trends (admin): the space used per share and per account,
// its growth, and how long each volume still has before it fills up. The
// usage of the shares and of the accounts is walked once a day and recorded
// in the same history as the volumes (keys "share:<path>" and "user:<name>"),
// so the trends, the projections and the retention come for free.

import (
	"cmp"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"strataos/internal/health"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// shareUsageTick throttles the walk of the shares to once a day.
var shareUsageTick struct {
	sync.Mutex
	at time.Time
}

// recordShareUsage walks the shares once a day and records the space used by
// each share and each account in the volume history. The walk runs in the
// background: it can be long, the health watcher must not wait for it.
func (s *Server) recordShareUsage() {
	shareUsageTick.Lock()
	due := time.Since(shareUsageTick.at) >= 24*time.Hour
	if due {
		shareUsageTick.at = time.Now()
	}
	shareUsageTick.Unlock()
	if !due {
		return
	}
	go func() {
		var paths []string
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				paths = append(paths, sh.Path)
			}
		})
		usage := map[string]health.VolumePoint{}
		users := map[string]int64{}
		for _, root := range paths {
			var used int64
			filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.Type() != fs.FileMode(0) {
					return nil // symlinks and unreadable entries count for nothing
				}
				if !d.IsDir() {
					if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
						used += fi.Size()
						users[ownerNameOf(fi)] += fi.Size()
					}
				}
				return nil
			})
			usage["share:"+root] = health.VolumePoint{Used: used}
		}
		for name, used := range users {
			usage["user:"+name] = health.VolumePoint{Used: used}
		}
		s.hist.RecordVolumes(usage, time.Now())
	}()
}

// ownerNames caches the uid to account name resolution (the walk calls it a lot).
var ownerNames sync.Map

// ownerNameOf resolves the uid of a file to an account name, "uid:<n>" when unknown.
func ownerNameOf(fi os.FileInfo) string {
	uid := uint32(fi.Sys().(*syscall.Stat_t).Uid)
	if v, ok := ownerNames.Load(uid); ok {
		return v.(string)
	}
	name := "uid:" + strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil && u.Username != "" {
		name = u.Username
	}
	ownerNames.Store(uid, name)
	return name
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
		shares := []trendRow{}
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				shares = append(shares, trendRowOf(sh.Name, s.hist.Volume("share:"+sh.Path), now))
			}
		})
		users := []trendRow{}
		for name, pts := range s.hist.Series("user:") {
			users = append(users, trendRowOf(name, pts, now))
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
