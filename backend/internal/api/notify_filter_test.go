package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
)

func at(hhmm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-20 "+hhmm, time.Local)
	return t
}

func TestQuietHours(t *testing.T) {
	night := store.QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	day := store.QuietHours{Enabled: true, Start: "12:30", End: "14:00"}
	cases := []struct {
		q    store.QuietHours
		now  string
		want bool
	}{
		{night, "21:59", false}, {night, "22:00", true}, {night, "23:59", true}, {night, "00:00", true},
		{night, "06:59", true}, {night, "07:00", false}, {night, "12:00", false},
		{day, "12:29", false}, {day, "12:30", true}, {day, "13:59", true}, {day, "14:00", false}, {day, "23:00", false},
		{store.QuietHours{Start: "22:00", End: "07:00"}, "23:00", false}, // not enabled
		{store.QuietHours{Enabled: true, Start: "25:00", End: "07:00"}, "03:00", false},
	}
	for _, c := range cases {
		if got := inQuietHours(c.q, at(c.now)); got != c.want {
			t.Errorf("%s-%s at %s: got %v", c.q.Start, c.q.End, c.now, got)
		}
	}
	// During the window only errors go through, or nothing with SilenceAll;
	// explicit sends (test, report) always do.
	for _, c := range []struct {
		silence bool
		level   string
		want    bool
	}{{false, "info", false}, {false, "warning", false}, {false, "error", true}, {true, "error", false}, {true, "", true}} {
		q := night
		q.SilenceAll = c.silence
		if got := quietAllows(q, c.level, at("23:00")); got != c.want {
			t.Errorf("silenceAll=%v level %q: got %v", c.silence, c.level, got)
		}
		if !quietAllows(q, c.level, at("12:00")) {
			t.Errorf("level %q held back outside the window", c.level)
		}
	}
	for _, bad := range []store.QuietHours{{Enabled: true, Start: "7:00", End: "08:00"}, {Enabled: true, Start: "08:00", End: "08:00"}, {Enabled: true, Start: "08:00", End: "24:00"}} {
		if validQuietHours(bad) == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if validQuietHours(night) != nil || validQuietHours(store.QuietHours{Start: "x"}) != nil {
		t.Error("valid quiet hours refused")
	}
}

func TestChannelLevel(t *testing.T) {
	cases := []struct {
		c     store.NotifyChannel
		level string
		want  bool
	}{
		{store.NotifyChannel{}, "info", true}, // zero value (state from before 0.6.0): everything
		{store.NotifyChannel{}, "error", true},
		{store.NotifyChannel{MinLevel: "warning"}, "info", false},
		{store.NotifyChannel{MinLevel: "warning"}, "warning", true},
		{store.NotifyChannel{MinLevel: "warning"}, "error", true},
		{store.NotifyChannel{MinLevel: "error"}, "warning", false},
		{store.NotifyChannel{MinLevel: "error"}, "", true}, // explicit send
		{store.NotifyChannel{Disabled: true}, "error", false},
		{store.NotifyChannel{Disabled: true}, "", false},
	}
	for _, c := range cases {
		if got := channelAccepts(c.c, c.level); got != c.want {
			t.Errorf("%+v level %q: got %v", c.c, c.level, got)
		}
	}
	if validNotifyPrefs(store.Settings{NotifyChannels: store.NotifyChannels{Ntfy: store.NotifyChannel{MinLevel: "critical"}}}) == nil {
		t.Error("unknown level accepted")
	}
}

func TestAlertDedup(t *testing.T) {
	raid := func(pct float64) storage.RaidArray {
		return storage.RaidArray{Name: "md127", Label: "data", Health: "rebuilding", SyncAction: "recovery", SyncProgress: pct}
	}
	vol := func(used int64) volumeSummary {
		return volumeSummary{Name: "main", Health: "healthy", Usage: storage.Usage{Total: 100, Used: used}}
	}
	first := storageAlerts([]storage.RaidArray{raid(12.5)}, nil, []volumeSummary{vol(91)}, false)
	second := storageAlerts([]storage.RaidArray{raid(42.3)}, nil, []volumeSummary{vol(95)}, false)
	if first[0].Message == second[0].Message || !strings.Contains(second[0].Message, "42.3%") {
		t.Errorf("the dashboard must keep the live progress: %q", second[0].Message)
	}
	fresh, known := freshAlerts(map[string]bool{}, first)
	if len(fresh) != 1 || !strings.Contains(fresh[0].Message, "91% full") {
		t.Fatalf("first check: %+v (progress alerts are left to the tracker)", fresh)
	}
	if fresh, _ = freshAlerts(known, second); len(fresh) != 0 {
		t.Errorf("new percentages notified again: %+v", fresh)
	}
	// Gone, then back: notified again.
	_, known = freshAlerts(known, nil)
	if fresh, _ = freshAlerts(known, second); len(fresh) != 1 {
		t.Errorf("problem back: %+v", fresh)
	}
	// Weak disks: growing counts are the same problem, a new kind is not.
	k1 := weakDiskKey("/dev/sda", "S1", []string{"5 reallocated sector(s)", "high temperature (61 °C)"})
	k2 := weakDiskKey("/dev/sda", "S1", []string{"12 reallocated sector(s)", "high temperature (63 °C)"})
	k3 := weakDiskKey("/dev/sda", "S1", []string{"12 reallocated sector(s)", "3 unreadable sector(s)"})
	k4 := weakDiskKey("/dev/sdb", "S2", []string{"5 reallocated sector(s)", "high temperature (61 °C)"})
	if k1 != k2 || k1 == k3 || k1 == k4 {
		t.Errorf("weak disk keys: %q %q %q %q", k1, k2, k3, k4)
	}
}

func TestProgressNotifiedOnce(t *testing.T) {
	var tr progressTracker
	t0 := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	arr := func(health, action string, pct float64) []storage.RaidArray {
		return []storage.RaidArray{{Name: "md127", Label: "data", Health: health, SyncAction: action, SyncProgress: pct}}
	}
	step := func(raids []storage.RaidArray, scans map[string]storage.ZScan, after time.Duration) []progressEvent {
		return tr.update(append(raidOps(raids), zfsOps(scans)...), t0.Add(after), storageEnd(raids, scans))
	}
	ev := step(arr("rebuilding", "recovery (delayed)", 0), nil, 0)
	if len(ev) != 1 || ev[0].level != "info" || ev[0].msg != "RAID array data: rebuild started" {
		t.Fatalf("start: %+v", ev)
	}
	for i, pct := range []float64{1.5, 42.3, 99.9} {
		if ev := step(arr("rebuilding", "recovery", pct), nil, time.Duration(i+1)*5*time.Minute); len(ev) != 0 {
			t.Errorf("progress %.1f%% notified: %+v", pct, ev)
		}
	}
	ev = step(arr("healthy", "", 0), nil, 2*time.Hour+5*time.Minute)
	if len(ev) != 1 || ev[0].level != "info" || ev[0].msg != "RAID array data: rebuild finished in 2 h 05" {
		t.Fatalf("end: %+v", ev)
	}
	if ev := step(arr("healthy", "", 0), nil, 3*time.Hour); len(ev) != 0 {
		t.Errorf("end notified twice: %+v", ev)
	}
	// A rebuild that stops on a still degraded array failed.
	step(arr("rebuilding", "recovery", 3), nil, 4*time.Hour)
	if ev := step(arr("degraded", "", 0), nil, 4*time.Hour+10*time.Minute); len(ev) != 1 || ev[0].level != "error" || !strings.Contains(ev[0].msg, "rebuild failed after 10 min") {
		t.Errorf("failure: %+v", ev)
	}
	// ZFS scrub with errors, then a clean resilver.
	run := map[string]storage.ZScan{"tank": {Kind: "scrub", Running: true}}
	if ev := step(nil, run, 5*time.Hour); len(ev) != 1 || ev[0].msg != "ZFS pool tank: scrub started" {
		t.Errorf("scrub start: %+v", ev)
	}
	step(nil, run, 5*time.Hour+5*time.Minute)
	if ev := step(nil, map[string]storage.ZScan{"tank": {Kind: "scrub", Errors: 2}}, 5*time.Hour+30*time.Minute); len(ev) != 1 || ev[0].level != "error" || ev[0].msg != "ZFS pool tank: scrub finished in 30 min with 2 errors" {
		t.Errorf("scrub end: %+v", ev)
	}
	step(nil, map[string]storage.ZScan{"tank": {Kind: "scrub", Running: true}}, 6*time.Hour)
	if ev := step(nil, map[string]storage.ZScan{"tank": {Kind: "scrub", Canceled: true}}, 6*time.Hour+time.Minute); len(ev) != 1 || ev[0].level != "warning" {
		t.Errorf("scrub canceled: %+v", ev)
	}
}

func TestSendNotificationFilters(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		got = append(got, b["message"])
		mu.Unlock()
	}))
	defer hook.Close()
	s := newTestServer(t)
	setPrefs := func(f func(*store.Settings)) {
		_ = s.store.Update(func(st *store.State) error {
			st.Settings.WebhookURL = hook.URL
			f(&st.Settings)
			return nil
		})
	}
	send := func(level string) ([]string, error) { return s.sendNotification(level, "Alert", level+" event") }

	if _, err := send("info"); err == nil || !strings.Contains(err.Error(), "no notification channel") {
		t.Errorf("no channel: %v", err)
	}
	setPrefs(func(*store.Settings) {}) // as after an upgrade: no preferences
	if sent, err := send("info"); err != nil || len(sent) != 1 {
		t.Errorf("default channel: %v %v", sent, err)
	}
	setPrefs(func(st *store.Settings) { st.NotifyChannels.Webhook.MinLevel = "error" })
	if sent, err := send("warning"); err != nil || len(sent) != 0 {
		t.Errorf("warning below the minimum: %v %v", sent, err)
	}
	if sent, _ := send("error"); len(sent) != 1 {
		t.Errorf("error not sent: %v", sent)
	}
	now := time.Now()
	setPrefs(func(st *store.Settings) {
		st.NotifyChannels.Webhook.MinLevel = ""
		st.QuietHours = store.QuietHours{Enabled: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04")}
	})
	if sent, _ := send("warning"); len(sent) != 0 {
		t.Errorf("warning during quiet hours: %v", sent)
	}
	if sent, _ := send("error"); len(sent) != 1 {
		t.Errorf("error during quiet hours: %v", sent)
	}
	setPrefs(func(st *store.Settings) {
		st.QuietHours = store.QuietHours{}
		st.NotifyChannels.Webhook.Disabled = true
	})
	if _, err := send(""); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled channel: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "info event,error event,error event" {
		t.Errorf("delivered: %v", got)
	}
}
