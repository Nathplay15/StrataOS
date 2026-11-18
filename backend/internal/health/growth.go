package health

import (
	"path/filepath"
	"time"
)

// FullIn projects when a volume runs out of space: a least-squares line over
// the points of the last 14 days gives the days left before it is full. ok is
// false without a usable history (too short, not growing, or already full).
func FullIn(pts []VolumePoint, total int64, now time.Time) (days float64, ok bool) {
	if total <= 0 || len(pts) < 2 {
		return 0, false
	}
	since := now.Add(-14 * 24 * time.Hour).Unix()
	recent := pts[:0:0]
	for _, p := range pts {
		if p.T >= since {
			recent = append(recent, p)
		}
	}
	if len(recent) >= 2 {
		pts = recent
	}
	if pts[len(pts)-1].T-pts[0].T < 3*86400 {
		return 0, false
	}
	var used int64
	var n float64
	var sx, sy, sxx, sxy float64
	for _, p := range pts {
		x, y := float64(p.T)/86400, float64(p.Used)
		n++
		used = p.Used
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	slope := (n*sxy - sx*sy) / den
	if slope <= 0 || used >= total {
		return 0, false
	}
	return float64(total-used) / slope, true
}

// Series returns the usage histories whose key starts with prefix (the
// shares are kept as "share:<path>", the accounts as "user:<name>").
func (s *Store) Series(prefix string) map[string][]VolumePoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadVolumes()
	out := map[string][]VolumePoint{}
	for k, pts := range s.volumes {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = append([]VolumePoint(nil), pts...)
		}
	}
	return out
}

// Forget drops the usage histories whose key drop accepts (shares and
// accounts that no longer exist).
func (s *Store) Forget(drop func(key string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadVolumes()
	changed := false
	for k := range s.volumes {
		if drop(k) {
			delete(s.volumes, k)
			changed = true
		}
	}
	if changed {
		_ = s.save(filepath.Join(s.dir, "volumes.json"), s.volumes)
	}
}
