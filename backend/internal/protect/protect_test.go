package protect

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// hourly snapshots going back n hours from t0.
func hourly(n int) []time.Time {
	var out []time.Time
	for i := range n {
		out = append(out, t0.Add(-time.Duration(i)*time.Hour))
	}
	return out
}

func kept(times []time.Time, p Policy) int {
	n := 0
	for _, k := range Keep(times, p, t0) {
		if k {
			n++
		}
	}
	return n
}

func TestKeepEmptyPolicyKeepsAll(t *testing.T) {
	if n := kept(hourly(50), Policy{}); n != 50 {
		t.Fatalf("kept %d, want 50", n)
	}
	if Expired(hourly(5), Policy{}, t0) != nil {
		t.Fatal("nothing expires without a policy")
	}
}

func TestKeepLast(t *testing.T) {
	times := hourly(10)
	keep := Keep(times, Policy{Last: 3}, t0)
	for i, k := range keep {
		if k != (i < 3) {
			t.Fatalf("snapshot %d kept=%v", i, k)
		}
	}
}

func TestKeepAlwaysNewest(t *testing.T) {
	// Older than the Days limit, yet the newest stays.
	times := []time.Time{t0.AddDate(0, 0, -40), t0.AddDate(0, 0, -50)}
	keep := Keep(times, Policy{Days: 7}, t0)
	if !keep[0] || keep[1] {
		t.Fatalf("got %v", keep)
	}
}

func TestKeepHourlyDaily(t *testing.T) {
	// 72 hourly snapshots: 24 hourly + 7 daily keeps the 24 newest plus the
	// newest of the 2 older days (the 3 days of the range, the first already kept).
	times := hourly(72)
	p := Policy{Hourly: 24, Daily: 7}
	if n := kept(times, p); n != 24+2 {
		t.Fatalf("kept %d, want 26", n)
	}
	// The daily ones are the newest of their day: 23:00 on Nov 1 and Oct 31.
	keep := Keep(times, p, t0)
	for i, k := range keep {
		if i >= 24 && k {
			h := times[i]
			if h.Hour() != 23 {
				t.Fatalf("daily snapshot at %v, want the last of its day", h)
			}
		}
	}
}

func TestKeepWeeklyMonthly(t *testing.T) {
	// One snapshot a day for a year.
	var times []time.Time
	for i := range 365 {
		times = append(times, t0.AddDate(0, 0, -i))
	}
	p := Policy{Daily: 7, Weekly: 4, Monthly: 6}
	keep := Keep(times, p, t0)
	n := 0
	months := map[string]bool{}
	for i, k := range keep {
		if k {
			n++
			months[times[i].Format("2006-01")] = true
		}
	}
	// 7 daily, weekly adds the weeks not covered (at most 4), monthly up to 6 months.
	if n < 7+2 || n > 7+4+6 {
		t.Fatalf("kept %d", n)
	}
	if len(months) != 6 {
		t.Fatalf("months covered: %d, want 6", len(months))
	}
	// Nothing older than 6 months survives.
	for i, k := range keep {
		if k && t0.Sub(times[i]) > 190*24*time.Hour {
			t.Fatalf("kept %v", times[i])
		}
	}
}

func TestKeepUnordered(t *testing.T) {
	times := []time.Time{t0.Add(-2 * time.Hour), t0, t0.Add(-1 * time.Hour)}
	keep := Keep(times, Policy{Last: 1}, t0)
	if !slices.Equal(keep, []bool{false, true, false}) {
		t.Fatalf("got %v", keep)
	}
	exp := Expired(times, Policy{Last: 1}, t0)
	if !slices.Equal(exp, []int{0, 2}) {
		t.Fatalf("expired %v, want oldest first [0 2]", exp)
	}
}

func TestPolicyValidate(t *testing.T) {
	if (Policy{Daily: -1}).Validate() == nil {
		t.Fatal("negative accepted")
	}
	if (Policy{Daily: 7}).Validate() != nil {
		t.Fatal("valid refused")
	}
	if s := (Policy{Last: 3, Daily: 7}).String(); s != "3 last, 7 daily" {
		t.Fatalf("String: %q", s)
	}
}

func TestAssess(t *testing.T) {
	r := Assess(nil)
	if r.Copies != 1 || r.Media != 1 || r.OK || len(r.Missing) != 3 {
		t.Fatalf("no backup: %+v", r)
	}
	usb := Copy{Name: "usb", Media: MediaUSB, Healthy: true}
	cloud := Copy{Name: "b2", Media: MediaCloud, Offsite: true, Healthy: true}
	r = Assess([]Copy{usb, cloud})
	if r.Copies != 3 || r.Media != 3 || r.Offsite != 1 || !r.OK {
		t.Fatalf("usb + cloud: %+v", r)
	}
	// A failing copy does not count.
	cloud.Healthy = false
	r = Assess([]Copy{usb, cloud})
	if r.OK || !slices.Equal(r.Missing, []string{"copies", "offsite"}) {
		t.Fatalf("failing cloud: %+v", r)
	}
	// Two copies on the NAS itself: 3 copies but one medium.
	r = Assess([]Copy{{Media: MediaNAS, Healthy: true}, {Media: MediaNAS, Healthy: true}})
	if r.Copies != 3 || r.Media != 1 || !slices.Equal(r.Missing, []string{"media", "offsite"}) {
		t.Fatalf("same box: %+v", r)
	}
}

func TestMediaOf(t *testing.T) {
	for _, c := range []struct {
		dest    string
		usb     bool
		media   string
		offsite bool
	}{
		{"local", false, MediaNAS, false},
		{"local", true, MediaUSB, false},
		{"s3", false, MediaCloud, true},
		{"strataos", false, MediaRemote, true},
		{"sftp", false, MediaRemote, true},
	} {
		m, o := MediaOf(c.dest, c.usb)
		if m != c.media || o != c.offsite {
			t.Errorf("%s usb=%v: %s %v", c.dest, c.usb, m, o)
		}
	}
	if !Overdue(time.Time{}, time.Hour, t0) || Overdue(t0.Add(-time.Hour), 24*time.Hour, t0) || !Overdue(t0.AddDate(0, 0, -4), 24*time.Hour, t0) {
		t.Fatal("Overdue")
	}
}

func TestCompare(t *testing.T) {
	old := t0.Add(-48 * time.Hour)
	src := []Entry{
		{Path: "/v/a/new.txt", Size: 10, MTime: old},
		{Path: "/v/a/same.txt", Size: 5, MTime: old},
		{Path: "/v/a/older.txt", Size: 7, MTime: old},
		{Path: "/v/a/changed.txt", Size: 8, MTime: old},
		{Path: "/v/a/sub", Dir: true},
	}
	cur := map[string]Entry{
		"/t/a/same.txt":    {Size: 5, MTime: old.Add(300 * time.Millisecond)},
		"/t/a/older.txt":   {Size: 3, MTime: old.Add(-time.Hour)},
		"/t/a/changed.txt": {Size: 9, MTime: t0},
	}
	dest := func(p string) string { return Destination(p, "/v", "/t") }
	p := Compare(src, dest, func(p string) (Entry, bool) { e, ok := cur[p]; return e, ok })
	if p.Files != 4 || p.Dirs != 1 || p.New != 1 || p.Same != 1 || p.Overwrite != 1 || p.Conflicts != 1 {
		t.Fatalf("preview %+v", p)
	}
	if p.Bytes != 30 || p.NewBytes != 10 || p.Changed != 15 {
		t.Fatalf("bytes %+v", p)
	}
	if len(p.Items) != 2 || p.Items[0].Action != ActConflict || p.Items[0].Path != "/t/a/changed.txt" {
		t.Fatalf("items %+v", p.Items)
	}
	if len(p.Sample) != 1 || p.Sample[0].Path != "/t/a/new.txt" {
		t.Fatalf("sample %+v", p.Sample)
	}
}

func TestDestinationAndRename(t *testing.T) {
	if d := Destination("/srv/strata/v/Docs/a/b.txt", "/srv/strata/v/Docs", ""); d != "/srv/strata/v/Docs/a/b.txt" {
		t.Fatal(d)
	}
	if d := Destination("/srv/strata/v/Docs/a/b.txt", "/srv/strata/v", "/srv/strata/w/restored"); d != "/srv/strata/w/restored/Docs/a/b.txt" {
		t.Fatal(d)
	}
	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	if r := RenamedPath("/x/report.pdf", at); r != "/x/report (restored 2026-11-03 14h05).pdf" {
		t.Fatal(r)
	}
	if r := RenamedPath("/x/.bashrc", at); r != "/x/.bashrc (restored 2026-11-03 14h05)" {
		t.Fatal(r)
	}
}

func TestSample(t *testing.T) {
	var files []Entry
	for i := range 100 {
		files = append(files, Entry{Path: string(rune('a'+i%26)) + "x", Size: int64(i)})
	}
	files = append(files, Entry{Path: "/big", Size: 1 << 40}, Entry{Path: "/d", Dir: true, Size: 1})
	s := Sample(files, 10, 300, rand.New(rand.NewPCG(1, 2)))
	if len(s) == 0 || len(s) > 10 {
		t.Fatalf("sample of %d", len(s))
	}
	var total int64
	for _, f := range s {
		if f.Size == 0 || f.Dir || f.Path == "/big" {
			t.Fatalf("picked %+v", f)
		}
		total += f.Size
	}
	if total > 300 {
		t.Fatalf("budget exceeded: %d", total)
	}
}
