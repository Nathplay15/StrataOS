package health

import (
	"testing"
	"time"
)

func TestFullIn(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	total := int64(1000)

	// A steady growth of 10 bytes a day for 14 days: 140 bytes used, so
	// 860 bytes left, about 86 days before the volume is full.
	var pts []VolumePoint
	for d := 14; d >= 0; d-- {
		pts = append(pts, VolumePoint{T: now.Add(-time.Duration(d) * 24 * time.Hour).Unix(), Used: int64(14-d) * 10})
	}
	days, ok := FullIn(pts, total, now)
	if !ok {
		t.Fatal("a growing volume got no projection")
	}
	if days < 80 || days > 92 {
		t.Errorf("unexpected projection: %v days", days)
	}

	// A shrinking usage is not a projection.
	for i := range pts {
		pts[i].Used = int64(14-i%15) * 0
	}
	down := []VolumePoint{
		{T: now.Add(-14 * 24 * time.Hour).Unix(), Used: 700},
		{T: now.Add(-7 * 24 * time.Hour).Unix(), Used: 600},
		{T: now.Unix(), Used: 500},
	}
	if _, ok := FullIn(down, total, now); ok {
		t.Error("a shrinking volume got a projection")
	}

	// A history too short (less than 3 days) is not usable.
	short := []VolumePoint{
		{T: now.Add(-24 * time.Hour).Unix(), Used: 100},
		{T: now.Unix(), Used: 200},
	}
	if _, ok := FullIn(short, total, now); ok {
		t.Error("a two-point one-day history got a projection")
	}

	// Already full, or no volume size: no projection.
	full := []VolumePoint{
		{T: now.Add(-7 * 24 * time.Hour).Unix(), Used: 900},
		{T: now.Unix(), Used: 1000},
	}
	if _, ok := FullIn(full, total, now); ok {
		t.Error("a full volume got a projection")
	}
	if _, ok := FullIn(pts, 0, now); ok {
		t.Error("a volume without a known size got a projection")
	}

	// Old points beyond 14 days are ignored: a volume that grew fast a month
	// ago but is stable now has no projection.
	old := []VolumePoint{
		{T: now.Add(-30 * 24 * time.Hour).Unix(), Used: 0},
		{T: now.Add(-29 * 24 * time.Hour).Unix(), Used: 200},
		{T: now.Add(-2 * 24 * time.Hour).Unix(), Used: 200},
		{T: now.Unix(), Used: 200},
	}
	if _, ok := FullIn(old, total, now); ok {
		t.Error("a stable volume got a projection from month-old growth")
	}
}

func TestSeries(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.RecordVolumes(map[string]VolumePoint{
		"vol:pool":     {Used: 100},
		"share:/media": {Used: 50},
		"user:alice":   {Used: 30},
	}, time.Now())

	shares := s.Series("share:")
	if len(shares) != 1 || len(shares["share:/media"]) != 1 {
		t.Fatalf("unexpected shares: %v", shares)
	}
	users := s.Series("user:")
	if len(users) != 1 || users["user:alice"][0].Used != 30 {
		t.Fatalf("unexpected users: %v", users)
	}
	if got := s.Series("share:"); len(got["share:/media"]) != 1 {
		t.Error("Series must return a copy")
	}
}
