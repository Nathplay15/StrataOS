package health

import (
	"path/filepath"
	"time"
)

// PowerPoint is one power consumption reading of the NAS, in watts.
type PowerPoint struct {
	T     int64   `json:"t"`
	Watts float64 `json:"watts"`
}

// loadPower loads power.json once.
func (s *Store) loadPower() {
	if s.power == nil {
		s.power = []PowerPoint{}
		s.load(filepath.Join(s.dir, "power.json"), &s.power)
	}
}

// RecordPower adds a consumption point (at most one an hour, like volumes).
func (s *Store) RecordPower(watts float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadPower()
	p := PowerPoint{T: now.Unix(), Watts: watts}
	var rec bool
	s.power, rec = appendPoint(s.power, p, func(p PowerPoint) int64 { return p.T }, now)
	if rec {
		_ = s.save(filepath.Join(s.dir, "power.json"), s.power)
	}
}

// Power returns the consumption history, oldest first.
func (s *Store) Power() []PowerPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadPower()
	return append([]PowerPoint(nil), s.power...)
}
