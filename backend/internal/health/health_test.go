package health

import (
	"strataos/internal/protect"
	"strings"
	"testing"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
)

func i64(v int64) *int64 { return &v }

func TestCompact(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var pts []VolumePoint
	tf := func(p VolumePoint) int64 { return p.T }
	// Hourly points over 30 days, then one 3 years old.
	pts = append(pts, VolumePoint{T: now.AddDate(-3, 0, 0).Unix()})
	for h := 30 * 24; h >= 0; h-- {
		pts = append(pts, VolumePoint{T: now.Add(-time.Duration(h) * time.Hour).Unix(), Used: int64(h)})
	}
	out := compact(pts, tf, now)
	if out[0].T == pts[0].T {
		t.Error("a point older than two years was kept")
	}
	// 7 days hourly (169 points) + one point for each of the ~23 older days.
	if len(out) < 169+22 || len(out) > 169+24 {
		t.Errorf("unexpected number of points: %d", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i].T <= out[i-1].T {
			t.Fatal("points out of order")
		}
	}
	// At most hourly.
	p, rec := appendPoint(out, VolumePoint{T: now.Add(10 * time.Minute).Unix()}, tf, now)
	if rec || len(p) != len(out) {
		t.Error("a point 10 minutes after the last one was recorded")
	}
	if _, rec := appendPoint(out, VolumePoint{T: now.Add(time.Hour).Unix()}, tf, now.Add(time.Hour)); !rec {
		t.Error("a point an hour later was skipped")
	}
}

func TestTrends(t *testing.T) {
	base := map[string]int64{}
	if c := Trends(base, storage.SmartSample{Reallocated: i64(24), Pending: i64(2)}); len(c) != 0 {
		t.Errorf("first reading must only set the baseline: %v", c)
	}
	if c := Trends(base, storage.SmartSample{Reallocated: i64(24), Pending: i64(0)}); len(c) != 0 {
		t.Errorf("no change expected: %v", c)
	}
	c := Trends(base, storage.SmartSample{Reallocated: i64(30), Pending: i64(1)})
	if len(c) != 2 || c[0] != (Change{"re", 24, 30}) || c[1] != (Change{"pe", 0, 1}) {
		t.Errorf("changes: %v", c)
	}
	// Notified once per change.
	if c := Trends(base, storage.SmartSample{Reallocated: i64(30), Pending: i64(1)}); len(c) != 0 {
		t.Errorf("repeated: %v", c)
	}
	_, msg := ChangeMessage("/dev/sda", "WDC WD120", Change{"re", 24, 30})
	if msg != "Disk /dev/sda (WDC WD120) is getting worse: reallocated sectors went from 24 to 30. Check your backups" {
		t.Errorf("message: %q", msg)
	}

	// SSD wear crossing 80 then 90, spare going down.
	ssd := map[string]int64{}
	if c := Trends(ssd, storage.SmartSample{PercentUsed: i64(13), Spare: i64(100)}); len(c) != 0 {
		t.Errorf("new SSD: %v", c)
	}
	if c := Trends(ssd, storage.SmartSample{PercentUsed: i64(81), Spare: i64(90)}); len(c) != 2 || c[0].Field != "sp" || c[1].Field != "wear" {
		t.Errorf("80%%: %v", c)
	}
	if c := Trends(ssd, storage.SmartSample{PercentUsed: i64(85), Spare: i64(90)}); len(c) != 0 {
		t.Errorf("between the steps: %v", c)
	}
	c = Trends(ssd, storage.SmartSample{PercentUsed: i64(90), Spare: i64(90)})
	if lvl, _ := ChangeMessage("/dev/nvme0n1", "P300", c[0]); len(c) != 1 || lvl != "error" {
		t.Errorf("90%%: %v %s", c, lvl)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	now := time.Now()
	if _, rec := s.RecordDisk("WDC", "SER1", "/dev/sda", storage.SmartSample{Reallocated: i64(24)}, now); !rec {
		t.Fatal("first reading not recorded")
	}
	ch, _ := s.RecordDisk("WDC", "SER1", "/dev/sdb", storage.SmartSample{Reallocated: i64(30)}, now.Add(time.Hour))
	if len(ch) != 1 {
		t.Fatalf("changes: %v", ch)
	}
	// Read back from the files by another store (as after a restart).
	h := NewStore(dir).Disk("WDC", "SER1", "")
	if h == nil || len(h.Points) != 2 || h.Device != "/dev/sdb" {
		t.Fatalf("history: %+v", h)
	}
	if ch, _ := NewStore(dir).RecordDisk("WDC", "SER1", "/dev/sdb", storage.SmartSample{Reallocated: i64(30)}, now.Add(2*time.Hour)); len(ch) != 0 {
		t.Errorf("baseline lost after a restart: %v", ch)
	}
	s.RecordVolumes(map[string]VolumePoint{"data": {Used: 100, Total: 1000}}, now.Add(-8*24*time.Hour))
	s.RecordVolumes(map[string]VolumePoint{"data": {Used: 300, Total: 1000}}, now)
	if g, ok := Growth(NewStore(dir).Volume("data"), now.Add(-7*24*time.Hour)); !ok || g != 200 {
		t.Errorf("growth %d %v", g, ok)
	}
	s.RecordCache("vg0/data", CachePoint{T: now.Unix(), ReadHits: 10, ReadMisses: 10}, now)
	if len(NewStore(dir).Cache("vg0/data")) != 1 {
		t.Error("cache history not saved")
	}
}

func TestHitRatios(t *testing.T) {
	pts := []CachePoint{{T: 0, ReadHits: 0, ReadMisses: 0}, {T: 10, ReadHits: 30, ReadMisses: 70}, {T: 20, ReadHits: 5, ReadMisses: 5}, {T: 30, ReadHits: 95, ReadMisses: 15}}
	series, overall, reads := HitRatios(pts, 0)
	if len(series) != 2 || series[0].Ratio != 30 || series[1].Ratio != 90 {
		t.Errorf("series %v", series)
	}
	if reads != 200 || overall != 60 {
		t.Errorf("overall %v over %d reads", overall, reads)
	}
}

func TestWear(t *testing.T) {
	// A P300 at 13 % after 20 TB, writing 50 GB a day.
	now := int64(1_800_000_000)
	var pts []Point
	for d := int64(30); d >= 0; d-- {
		w := 20e12 - d*50e9
		pts = append(pts, Point{T: now - d*day, SmartSample: storage.SmartSample{PercentUsed: i64(13), Written: i64(w), Spare: i64(100)}})
	}
	w := WearOf(pts)
	if w.Endurance == nil || *w.Endurance != int64(20e12)*100/13 {
		t.Fatalf("endurance %v", w.Endurance)
	}
	if w.WriteRate == nil || *w.WriteRate != 50e9 || w.RateDays != 30 {
		t.Fatalf("rate %v over %v", w.WriteRate, w.RateDays)
	}
	// (153.8 - 20) TB / 50 GB a day ≈ 2676 days.
	if w.RemainingDays == nil || *w.RemainingDays < 2670 || *w.RemainingDays > 2680 {
		t.Fatalf("remaining %v", w.RemainingDays)
	}
	adv := strings.Join(CacheAdvice(w, "writeback", 45, 50000), "|")
	if !strings.Contains(adv, "lasts about 7 more years") || !strings.Contains(adv, "Write-back") {
		t.Errorf("advice: %s", adv)
	}
	// Heavy writes: a few months left.
	fast := []Point{
		{T: now - 10*day, SmartSample: storage.SmartSample{PercentUsed: i64(85), Written: i64(85e12)}},
		{T: now, SmartSample: storage.SmartSample{PercentUsed: i64(86), Written: i64(86e12)}},
	}
	w = WearOf(fast)
	adv = strings.Join(CacheAdvice(w, "writethrough", 10, 50000), "|")
	for _, want := range []string{"Plan to replace the SSD", "in about 4 months", "Only 10% of reads"} {
		if !strings.Contains(adv, want) {
			t.Errorf("missing %q in %s", want, adv)
		}
	}
	// No byte counter: the percentage over a year.
	pctOnly := []Point{
		{T: now - 200*day, SmartSample: storage.SmartSample{PercentUsed: i64(10)}},
		{T: now, SmartSample: storage.SmartSample{PercentUsed: i64(20)}},
	}
	if w := WearOf(pctOnly); w.RemainingDays == nil || *w.RemainingDays != 1600 {
		t.Errorf("percentage only: %v", w.RemainingDays)
	}
	if a := CacheAdvice(WearOf(pts[len(pts)-1:]), "", -1, 0); len(a) != 1 || !strings.Contains(a[0], "after a few days") {
		t.Errorf("young history: %v", a)
	}
}

func baseFacts() Facts {
	t := true
	return Facts{
		SSHActive: true, SSHRootLogin: "prohibit-password", SMBMinProto: "SMB2_10", HasSMBShares: true,
		SecurityUpdates: true, UpdatesKnown: true, GuardOn: true, RansomOn: true,
		Backups:          []BackupFact{{Name: "nightly", LastOK: time.Now().Add(-20 * time.Hour), Interval: 24 * time.Hour}},
		RestoreSupported: true, NTP: &t, NotifyConfigured: true, Now: time.Now(),
	}
}

func ids(items []store.AdvisorItem) string {
	var l []string
	for _, it := range items {
		l = append(l, it.ID)
	}
	return strings.Join(l, ",")
}

func TestAdviseClean(t *testing.T) {
	items := Advise(baseFacts(), nil)
	if len(items) != 0 || Score(items) != 100 {
		t.Errorf("a well configured NAS: %v", ids(items))
	}
}

func TestAdvise(t *testing.T) {
	f := baseFacts()
	off := false
	f.AdminsNo2FA = []string{"admin"}
	f.SSHRootLogin, f.SSHPassword = "yes", true
	f.SMBMinProto = "SMB2"
	f.GuestShares, f.EveryoneRW = []string{"public"}, []string{"family"}
	f.Exposure = []string{"app domains: photos.example.com"}
	f.CertProblems = []string{"photos.example.com"}
	f.SecurityUpdates, f.StrataUpdate, f.SystemUpdates = false, "0.9.2", 4
	f.GuardOn, f.RansomOn = false, false
	f.Backups = []BackupFact{{Name: "usb", LastFailed: true}, {Name: "cloud", LastOK: time.Now().Add(-10 * 24 * time.Hour), Interval: 24 * time.Hour}, {Name: "weekly", LastOK: time.Now().Add(-8 * 24 * time.Hour), Interval: 7 * 24 * time.Hour}}
	f.RestoreSupported, f.RestoreWhy = false, "dpkg-repack is not installed"
	f.FailingDisks, f.WeakDisks = []string{"/dev/sdc"}, []string{"/dev/sda (24 reallocated sector(s))"}
	f.NTP, f.NotifyConfigured = &off, false
	f.Privileged, f.HostNet = []string{"ha"}, []string{"plex"}
	items := Advise(f, []string{"docker-hostnet"})
	want := "admin-2fa,ssh-root,smb-guest,guard,backup-failed,disk-failing,certificate,security-updates,strata-update,ransomware,backup-old,disk-weak,notify,ssh-password,smb-proto,share-everyone,system-updates,restore-points,ntp,exposure,docker-privileged,docker-hostnet"
	if got := ids(items); got != want {
		t.Errorf("items\n got %s\nwant %s", got, want)
	}
	for _, it := range items {
		if it.ID == "guard" && it.Severity != "high" {
			t.Error("address blocking off on an exposed NAS must be high")
		}
		if it.ID == "backup-old" && it.Title != "Backups that have not succeeded for a while: cloud" {
			t.Errorf("a weekly backup 8 days old is not late: %q", it.Title)
		}
		if it.ID == "docker-hostnet" && !it.Ignored {
			t.Error("ignored item not marked")
		}
		if it.Title == "" || it.Detail == "" || it.Link == "" {
			t.Errorf("incomplete item %+v", it)
		}
	}
	if s := Score(items); s != 0 {
		t.Errorf("score %d", s)
	}
	f2 := baseFacts()
	f2.Backups = nil
	if items := Advise(f2, nil); ids(items) != "backup-none" || Score(items) != 80 {
		t.Errorf("no backup: %s %d", ids(items), Score(items))
	} else if items[0].Title != "No backup task" {
		t.Errorf("no backup: %q", items[0].Title)
	}
	// Tasks exist but are all disabled: say so rather than "no backup task".
	f2.DisabledBackups = []string{"usb", "cloud"}
	items = Advise(f2, nil)
	if ids(items) != "backup-none" || items[0].Title != "No active backup task" || !strings.Contains(items[0].Detail, "disabled") || !strings.Contains(items[0].Detail, "cloud, usb") {
		t.Errorf("disabled backups: %+v", items)
	}
	// An enabled task: the disabled ones do not matter.
	f2.Backups = baseFacts().Backups
	if items := Advise(f2, nil); len(items) != 0 {
		t.Errorf("one enabled backup: %s", ids(items))
	}
}

func TestAdvisorNews(t *testing.T) {
	high := store.AdvisorItem{ID: "backup-none", Severity: "high", Title: "No backup task"}
	low := store.AdvisorItem{ID: "ntp", Severity: "low", Title: "Time synchronization is off"}
	if _, ok := AdvisorNews(nil, store.AdvisorRun{Score: 97, Items: []store.AdvisorItem{low}}); ok {
		t.Error("first run without high items must stay quiet")
	}
	prev := &store.AdvisorRun{Score: 97, Items: []store.AdvisorItem{low}}
	msg, ok := AdvisorNews(prev, store.AdvisorRun{Score: 77, Items: []store.AdvisorItem{high, low}})
	if !ok || !strings.Contains(msg, "Security score: 77/100 (was 97)") || !strings.Contains(msg, "• No backup task") {
		t.Errorf("news: %v %q", ok, msg)
	}
	if _, ok := AdvisorNews(&store.AdvisorRun{Score: 77, Items: []store.AdvisorItem{high, low}}, store.AdvisorRun{Score: 77, Items: []store.AdvisorItem{high, low}}); ok {
		t.Error("nothing new")
	}
	if _, ok := AdvisorNews(prev, store.AdvisorRun{Score: 100}); ok {
		t.Error("a better score is not news")
	}
}

func TestAptInstalled(t *testing.T) {
	log := `
Start-Date: 2026-10-20  03:00:01
Commandline: apt-get -y upgrade
Upgrade: libc6:amd64 (2.41-12, 2.41-12+deb13u1), libc-bin:amd64 (2.41-12, 2.41-12+deb13u1)
End-Date: 2026-10-20  03:01:00

Start-Date: 2026-10-30  03:00:01
Commandline: apt-get -y install strataos
Install: zstd:amd64 (1.5.6, automatic)
Upgrade: strataos:amd64 (0.9.0, 0.9.1)
End-Date: 2026-10-30  03:01:00
`
	since := time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local)
	if n := AptInstalled(log, since); n != 2 {
		t.Errorf("week: %d", n)
	}
	if n := AptInstalled(log, since.AddDate(0, -1, 0)); n != 4 {
		t.Errorf("month: %d", n)
	}
}

func TestAdviseDataProtection(t *testing.T) {
	f := baseFacts()
	r := protect.Assess([]protect.Copy{{Name: "usb", Media: protect.MediaUSB, Healthy: true}})
	f.Rule321 = &r
	f.RestoreFailed = []string{"nightly"}
	f.RestoreTestOff = true
	items := Advise(f, nil)
	if got := ids(items); got != "restore-test-failed,backup-321,restore-test-off" {
		t.Fatalf("items %s", got)
	}
	if !strings.Contains(items[1].Title, "2 copies, 2 media, 0 off-site") {
		t.Errorf("title %q", items[1].Title)
	}
	ok := protect.Assess([]protect.Copy{{Media: protect.MediaUSB, Healthy: true}, {Media: protect.MediaCloud, Offsite: true, Healthy: true}})
	f = baseFacts()
	f.Rule321 = &ok
	if items := Advise(f, nil); len(items) != 0 {
		t.Errorf("3-2-1 met: %s", ids(items))
	}
}

func TestAdviseDatastores(t *testing.T) {
	f := baseFacts()
	now := f.Now
	f.Datastores = []DatastoreFact{
		{Name: "new", Created: now.Add(-time.Hour)},                                              // too young to flag
		{Name: "never", Created: now.Add(-72 * time.Hour)},                                       // never checked
		{Name: "ok", Created: now.Add(-900 * time.Hour), LastOK: now.Add(-24 * time.Hour)},       // fine
		{Name: "old", Created: now.Add(-900 * time.Hour), LastOK: now.Add(-40 * 24 * time.Hour)}, // stale
		{Name: "off", Created: now.Add(-900 * time.Hour), LastOK: now.Add(-time.Hour), Off: true},
		{Name: "bad", Created: now.Add(-900 * time.Hour), LastOK: now.Add(-48 * time.Hour), LastFailed: true},
	}
	items := Advise(f, nil)
	got := map[string]store.AdvisorItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	if it := got["datastore-verify-failed"]; it.Severity != "high" || !strings.Contains(it.Title, "bad") {
		t.Fatalf("failed: %+v", it)
	}
	it := got["datastore-unverified"]
	if it.Severity != "medium" || !strings.Contains(it.Title, "never, off, old") || strings.Contains(it.Title, "new") || strings.Contains(it.Title, "ok") {
		t.Fatalf("unverified: %+v", it)
	}
}
