package energy

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func paris(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no time zone database")
	}
	return loc
}

func TestRuleNext(t *testing.T) {
	loc := paris(t)
	every := []int{0, 1, 2, 3, 4, 5, 6}
	r := PowerRule{ID: "a", Enabled: true, Action: ActShutdown, Days: []int{1, 2, 3, 4, 5}, Time: "23:30"}
	fri := time.Date(2026, 10, 16, 22, 0, 0, 0, loc)
	if got := r.Next(fri); !got.Equal(time.Date(2026, 10, 16, 23, 30, 0, 0, loc)) {
		t.Errorf("friday: %v", got)
	}
	if got := r.Next(time.Date(2026, 10, 16, 23, 30, 0, 0, loc)); !got.Equal(time.Date(2026, 10, 19, 23, 30, 0, 0, loc)) {
		t.Errorf("strictly after, weekend skipped: %v", got)
	}
	// Clocks go forward on 2026-03-29 at 02:00: 02:30 does not exist, it fires once.
	sp := PowerRule{ID: "s", Enabled: true, Action: ActWake, Days: every, Time: "02:30"}
	first := sp.Next(time.Date(2026, 3, 29, 0, 0, 0, 0, loc))
	if first.Day() != 29 || first.Hour() != 3 || first.Minute() != 30 {
		t.Errorf("spring forward: %v", first)
	}
	if next := sp.Next(first); next.Day() != 30 || next.Hour() != 2 {
		t.Errorf("after spring forward: %v", next)
	}
	// Clocks go back on 2026-10-25 at 03:00: 02:30 happens twice, it fires once.
	fa := PowerRule{ID: "f", Enabled: true, Action: ActShutdown, Days: every, Time: "02:30"}
	first = fa.Next(time.Date(2026, 10, 25, 0, 0, 0, 0, loc))
	if first.Day() != 25 || first.Hour() != 2 {
		t.Errorf("fall back: %v", first)
	}
	if next := fa.Next(first); next.Day() != 26 {
		t.Errorf("fired twice on fall back: %v then %v", first, next)
	}
	// 24 h apart on the wall clock, 25 h in real time across the change.
	a := fa.Next(time.Date(2026, 10, 24, 0, 0, 0, 0, loc))
	if b := fa.Next(a); b.Sub(a) != 25*time.Hour {
		t.Errorf("fall back gap %v", b.Sub(a))
	}
	if (PowerRule{Enabled: false, Days: every, Time: "10:00"}).Next(fri).IsZero() == false {
		t.Error("disabled rule fires")
	}
}

func TestNextActionOverlap(t *testing.T) {
	loc := paris(t)
	every := []int{0, 1, 2, 3, 4, 5, 6}
	rules := []PowerRule{
		{ID: "r", Enabled: true, Action: ActRestart, Days: every, Time: "03:00"},
		{ID: "s", Enabled: true, Action: ActShutdown, Days: every, Time: "03:00"},
		{ID: "w1", Enabled: true, Action: ActWake, Days: every, Time: "03:01"},
		{ID: "w2", Enabled: true, Action: ActWake, Days: every, Time: "07:00"},
	}
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, loc)
	at, r := NextAction(rules, now)
	if r.ID != "s" || at.Hour() != 3 {
		t.Errorf("same time: %v %v", r.ID, at)
	}
	// The 03:01 power-on is too close to the shutdown: the 07:00 one is used.
	if w := WakeAfterShutdown(rules, at); w.Hour() != 7 || w.Day() != 14 {
		t.Errorf("wake %v", w)
	}
	if w := NextWake(rules, now); w.Hour() != 3 || w.Minute() != 1 {
		t.Errorf("next wake %v", w)
	}
}

func TestDecide(t *testing.T) {
	due := time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)
	g := PowerGuard{Users: true, RetryMin: 10, MaxMin: 30}
	if v, _ := Decide(g, due, due, "", ""); v != Run {
		t.Error("idle not run")
	}
	if v, why := Decide(g, due, due, "Backup photos", ""); v != Postpone || why != "Backup photos" {
		t.Errorf("job: %s %s", v, why)
	}
	if v, _ := Decide(g, due, due.Add(20*time.Minute), "", "alice (SMB)"); v != Postpone {
		t.Error("user within the delay not postponed")
	}
	if v, _ := Decide(g, due, due.Add(21*time.Minute), "x", ""); v != Skip {
		t.Error("past the longest delay not skipped")
	}
	if v, _ := Decide(PowerGuard{}, due, due, "", "alice"); v != Run {
		t.Error("users ignored by default")
	}
	if (PowerGuard{RetryMin: 30, MaxMin: 10}).Validate() == nil || (PowerGuard{}).Validate() != nil {
		t.Error("guard validation")
	}
	if (PowerGuard{}).Warn() != 10*time.Minute || (PowerGuard{WarnMin: -1}).Warn() != 0 {
		t.Error("warn")
	}
}

func TestMigrateSchedule(t *testing.T) {
	r := MigrateSchedule(Schedule{Enabled: true, Days: []int{5, 6}, Shutdown: "23:30", Wake: "07:00"})
	if len(r) != 2 || r[0].Action != ActShutdown || !slices.Equal(r[1].Days, []int{0, 6}) || r[1].Time != "07:00" {
		t.Errorf("next-day wake: %+v", r)
	}
	r = MigrateSchedule(Schedule{Enabled: true, Days: []int{1}, Shutdown: "12:00", Wake: "14:00"})
	if !slices.Equal(r[1].Days, []int{1}) {
		t.Errorf("same-day wake: %+v", r)
	}
	if MigrateSchedule(Schedule{}) != nil {
		t.Error("disabled schedule migrated")
	}
	if err := ValidateRules(r); err != nil {
		t.Error(err)
	}
	if ValidateRules([]PowerRule{{ID: "a", Action: "explode", Days: []int{1}, Time: "10:00"}}) == nil ||
		ValidateRules([]PowerRule{{ID: "a", Action: ActWake, Days: []int{1}, Time: "10:00"}, {ID: "a", Action: ActWake, Days: []int{1}, Time: "11:00"}}) == nil {
		t.Error("bad rules accepted")
	}
}

func TestPersistWoL(t *testing.T) {
	old := LinkDir
	LinkDir = t.TempDir()
	defer func() { LinkDir = old }()
	if err := PersistWoL(map[string]string{"enp1s0": "AA:BB:CC:DD:EE:01", "enp2s0": "aa:bb:cc:dd:ee:02"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(LinkDir, "50-strata-wol-enp1s0.link"))
	for _, want := range []string{"PermanentMACAddress=aa:bb:cc:dd:ee:01", "WakeOnLan=magic", "NamePolicy=keep kernel database onboard slot path", "Name=enp1s0"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in\n%s", want, b)
		}
	}
	if err := PersistWoL(map[string]string{"enp2s0": "aa:bb:cc:dd:ee:02"}); err != nil {
		t.Fatal(err)
	}
	if got := PersistedWoL(); !slices.Equal(got, []string{"enp2s0"}) {
		t.Errorf("after turning enp1s0 off: %v", got)
	}
	if PersistWoL(map[string]string{"../x": "aa:bb:cc:dd:ee:02"}) == nil {
		t.Error("bad interface name accepted")
	}
}

func TestBroadcasts(t *testing.T) {
	var nets []*net.IPNet
	for _, c := range []string{"192.168.1.20/24", "10.0.0.5/8", "192.168.1.30/24", "127.0.0.1/8", "172.16.0.1/32", "fd00::1/64"} {
		ip, n, _ := net.ParseCIDR(c)
		n.IP = ip
		nets = append(nets, n)
	}
	var got []string
	for _, b := range Broadcasts(nets) {
		got = append(got, b.String())
	}
	if !slices.Equal(got, []string{"192.168.1.255", "10.255.255.255"}) {
		t.Errorf("got %v", got)
	}
}
