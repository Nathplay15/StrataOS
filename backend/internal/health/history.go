// Package health keeps the health history of the NAS (SMART readings of the
// disks, SSD cache counters, volume usage), turns it into trends and wear
// estimates, and evaluates the security advisor checks. Everything here is
// pure or file-based so the logic can be tested with fake inputs.
package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"strataos/internal/storage"
	"strataos/internal/sys"
)

// History resolution: one point an hour at most, hourly points for a week,
// then one point a day, for two years.
const (
	MinInterval = 55 * time.Minute
	hourlyFor   = 7 * 24 * time.Hour
	keepFor     = 731 * 24 * time.Hour
)

// Point is a SMART reading of a disk.
type Point struct {
	T int64 `json:"t"` // Unix seconds
	storage.SmartSample
}

// DiskHistory is the history of one disk, kept by serial number.
type DiskHistory struct {
	Key    string  `json:"key"`
	Model  string  `json:"model,omitempty"`
	Serial string  `json:"serial,omitempty"`
	Device string  `json:"device,omitempty"` // last path seen
	Points []Point `json:"points"`
	// Baseline holds the counter values already known (notified or first
	// seen): a trend alert is sent once per change.
	Baseline map[string]int64 `json:"baseline,omitempty"`
}

// CachePoint holds the counters of an SSD cache (cumulative since the cache
// was activated or the system started).
type CachePoint struct {
	T           int64 `json:"t"`
	ReadHits    int64 `json:"rh"`
	ReadMisses  int64 `json:"rm"`
	WriteHits   int64 `json:"wh,omitempty"`
	WriteMisses int64 `json:"wm,omitempty"`
}

// VolumePoint is the space used on a volume.
type VolumePoint struct {
	T     int64 `json:"t"`
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
}

// compact keeps the points of the last week, then the last point of each
// day, and nothing older than two years. Points are in time order.
func compact[P any](pts []P, t func(P) int64, now time.Time) []P {
	hourly := now.Add(-hourlyFor).Unix()
	oldest := now.Add(-keepFor).Unix()
	out := pts[:0:0]
	for i, p := range pts {
		ts := t(p)
		if ts < oldest {
			continue
		}
		if ts < hourly && i+1 < len(pts) && t(pts[i+1]) < hourly && t(pts[i+1])/86400 == ts/86400 {
			continue // a later point of the same day follows
		}
		out = append(out, p)
	}
	return out
}

// appendPoint adds p unless the last point is less than MinInterval old.
func appendPoint[P any](pts []P, p P, t func(P) int64, now time.Time) ([]P, bool) {
	if n := len(pts); n > 0 && t(p)-t(pts[n-1]) < int64(MinInterval/time.Second) {
		return pts, false
	}
	return compact(append(pts, p), t, now), true
}

// Store keeps the histories in files under a folder of the data directory.
type Store struct {
	dir     string
	mu      sync.Mutex
	disks   map[string]*DiskHistory
	caches  map[string][]CachePoint
	volumes map[string][]VolumePoint
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, disks: map[string]*DiskHistory{}, caches: map[string][]CachePoint{}}
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func fileName(key string) string {
	n := unsafeRe.ReplaceAllString(key, "_")
	if len(n) > 100 {
		n = n[:100]
	}
	if n == "" || n[0] == '.' {
		n = "_" + n
	}
	return n + ".json"
}

func (s *Store) load(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, v)
	}
}

func (s *Store) save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sys.WriteFileAtomic(path, b, 0o600)
}

// DiskKey identifies a disk across reboots: its serial number when it has one.
func DiskKey(model, serial, device string) string {
	if serial != "" {
		return serial
	}
	return model + "-" + filepath.Base(device)
}

func (s *Store) disk(key string) *DiskHistory {
	h := s.disks[key]
	if h == nil {
		h = &DiskHistory{Key: key}
		s.load(filepath.Join(s.dir, "disks", fileName(key)), h)
		h.Key = key
		s.disks[key] = h
	}
	return h
}

// RecordDisk adds a reading (at most one an hour) and returns the changes
// worth a notification. recorded is false when the reading was skipped.
func (s *Store) RecordDisk(model, serial, device string, smp storage.SmartSample, now time.Time) (changes []Change, recorded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.disk(DiskKey(model, serial, device))
	h.Model, h.Serial, h.Device = model, serial, device
	h.Points, recorded = appendPoint(h.Points, Point{T: now.Unix(), SmartSample: smp}, func(p Point) int64 { return p.T }, now)
	if !recorded {
		return nil, false
	}
	if h.Baseline == nil {
		h.Baseline = map[string]int64{}
	}
	changes = Trends(h.Baseline, smp)
	_ = s.save(filepath.Join(s.dir, "disks", fileName(h.Key)), h)
	return changes, true
}

// Disk returns a copy of the history of a disk (nil when there is none).
func (s *Store) Disk(model, serial, device string) *DiskHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.disk(DiskKey(model, serial, device))
	if len(h.Points) == 0 {
		return nil
	}
	c := *h
	c.Points = append([]Point(nil), h.Points...)
	c.Baseline = nil
	return &c
}

// RecordCache adds the counters of an SSD cache (at most one point an hour).
func (s *Store) RecordCache(key string, p CachePoint, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "caches", fileName(key))
	pts, ok := s.caches[key]
	if !ok {
		s.load(path, &pts)
	}
	pts, rec := appendPoint(pts, p, func(p CachePoint) int64 { return p.T }, now)
	s.caches[key] = pts
	if rec {
		_ = s.save(path, pts)
	}
}

// Cache returns the counters recorded for an SSD cache.
func (s *Store) Cache(key string) []CachePoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	pts, ok := s.caches[key]
	if !ok {
		s.load(filepath.Join(s.dir, "caches", fileName(key)), &pts)
		s.caches[key] = pts
	}
	return append([]CachePoint(nil), pts...)
}

func (s *Store) loadVolumes() {
	if s.volumes == nil {
		s.volumes = map[string][]VolumePoint{}
		s.load(filepath.Join(s.dir, "volumes.json"), &s.volumes)
	}
}

// RecordVolumes adds the usage of the volumes (at most one point an hour).
func (s *Store) RecordVolumes(usage map[string]VolumePoint, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadVolumes()
	changed := false
	for name, p := range usage {
		p.T = now.Unix()
		var rec bool
		s.volumes[name], rec = appendPoint(s.volumes[name], p, func(p VolumePoint) int64 { return p.T }, now)
		changed = changed || rec
	}
	if changed {
		_ = s.save(filepath.Join(s.dir, "volumes.json"), s.volumes)
	}
}

// Volume returns the usage history of a volume.
func (s *Store) Volume(name string) []VolumePoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadVolumes()
	return append([]VolumePoint(nil), s.volumes[name]...)
}

// HitRatio is the share of reads served by a cache between two points
// (-1 when the counters went back: reboot or new cache).
type HitRatio struct {
	T     int64   `json:"t"`
	Ratio float64 `json:"ratio"`
	Reads int64   `json:"reads"`
}

// HitRatios turns cumulative counters into the ratio of each interval, and
// the ratio over the points newer than since (-1 when no read happened).
func HitRatios(pts []CachePoint, since int64) (series []HitRatio, overall float64, reads int64) {
	var hits int64
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		dh, dm := b.ReadHits-a.ReadHits, b.ReadMisses-a.ReadMisses
		if dh < 0 || dm < 0 {
			continue // counters restarted
		}
		r := HitRatio{T: b.T, Ratio: -1, Reads: dh + dm}
		if dh+dm > 0 {
			r.Ratio = float64(dh) * 100 / float64(dh+dm)
		}
		series = append(series, r)
		if b.T > since {
			hits += dh
			reads += dh + dm
		}
	}
	overall = -1
	if reads > 0 {
		overall = float64(hits) * 100 / float64(reads)
	}
	return series, overall, reads
}

// Growth is the change of the space used on a volume since a date (ok is
// false without a point old enough).
func Growth(pts []VolumePoint, since time.Time) (delta int64, ok bool) {
	if len(pts) < 2 {
		return 0, false
	}
	ref := -1
	for i, p := range pts {
		if p.T <= since.Unix()+3600 {
			ref = i
		}
	}
	if ref < 0 {
		// History shorter than the period: from its first point, when it
		// covers most of it.
		last := pts[len(pts)-1].T
		if float64(last-pts[0].T) < 0.8*float64(last-since.Unix()) {
			return 0, false
		}
		ref = 0
	}
	return pts[len(pts)-1].Used - pts[ref].Used, true
}
