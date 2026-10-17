package energy

import (
	"testing"
	"time"
)

func TestSpindownValue(t *testing.T) {
	for min, want := range map[int]int{0: 0, 1: 12, 20: 240, 21: 241, 30: 241, 31: 242, 60: 242, 330: 251, 999: 251} {
		if got := SpindownValue(min); got != want {
			t.Errorf("SpindownValue(%d) = %d, want %d", min, got, want)
		}
	}
}

func TestSchedule(t *testing.T) {
	loc := time.Local
	s := Schedule{Enabled: true, Days: []int{1, 2, 3, 4, 5}, Shutdown: "23:30", Wake: "07:00"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	fri := time.Date(2026, 10, 16, 22, 0, 0, 0, loc) // Friday
	if got := s.NextShutdown(fri); !got.Equal(time.Date(2026, 10, 16, 23, 30, 0, 0, loc)) {
		t.Errorf("next shutdown %v", got)
	}
	sat := time.Date(2026, 10, 17, 12, 0, 0, 0, loc) // Saturday: next is Monday
	if got := s.NextShutdown(sat); !got.Equal(time.Date(2026, 10, 19, 23, 30, 0, 0, loc)) {
		t.Errorf("next shutdown after Saturday %v", got)
	}
	if got := s.WakeAfter(time.Date(2026, 10, 19, 23, 30, 0, 0, loc)); !got.Equal(time.Date(2026, 10, 20, 7, 0, 0, 0, loc)) {
		t.Errorf("wake %v", got)
	}
	for _, bad := range []Schedule{{Enabled: true, Days: []int{1}, Shutdown: "25:00"}, {Enabled: true, Shutdown: "22:00"}, {Enabled: true, Days: []int{7}, Shutdown: "22:00"}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	hw, err := ParseMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	p := MagicPacket(hw)
	if len(p) != 102 || p[0] != 0xFF || p[6] != 0xAA || p[101] != 0xFF {
		t.Errorf("bad packet % x", p[:12])
	}
	if _, err := ParseMAC("zz"); err == nil {
		t.Error("bad MAC accepted")
	}
}
