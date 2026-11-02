package storage

import (
	"strings"
	"testing"
)

func TestSmartWarnings(t *testing.T) {
	ok := &SmartInfo{Temperature: 35, Attributes: []SmartAttr{{ID: 5, Raw: "0"}, {ID: 197, Raw: "0"}, {ID: 1, Name: "Raw_Read_Error_Rate", Value: 100, Thresh: 6}}}
	if w := SmartWarnings(ok); len(w) != 0 {
		t.Errorf("healthy disk: %v", w)
	}
	weak := &SmartInfo{Temperature: 62, Attributes: []SmartAttr{{ID: 5, Raw: "8"}, {ID: 197, Raw: "2 (0 0)"}, {ID: 3, Name: "Spin_Up_Time", Value: 25, Thresh: 21}}}
	w := strings.Join(SmartWarnings(weak), "|")
	for _, want := range []string{"8 reallocated sector(s)", "2 sector(s) pending", "Spin_Up_Time close", "high temperature (62"} {
		if !strings.Contains(w, want) {
			t.Errorf("missing %q in %s", want, w)
		}
	}
	nvme := &SmartInfo{nvmeLog: nvmeHealth{present: true, PercentageUsed: 93, AvailableSpare: 5, SpareThreshold: 10, MediaErrors: 1}}
	w = strings.Join(SmartWarnings(nvme), "|")
	for _, want := range []string{"SSD wear at 93", "spare cells exhausted", "1 media error(s)"} {
		if !strings.Contains(w, want) {
			t.Errorf("missing %q in %s", want, w)
		}
	}
}

func TestSmartWarningsNVMeSpare(t *testing.T) {
	cases := []struct {
		name               string
		crit, spare, thres int
		want               bool
	}{
		{"spare equal to threshold (Patriot P300)", 0, 10, 10, false},
		{"spare above threshold", 0, 100, 10, false},
		{"spare below threshold", 0, 9, 10, true},
		{"critical warning bit 0", 1, 100, 10, true},
		{"no threshold reported", 0, 0, 0, false},
		{"other critical bit only", 4, 100, 10, false},
	}
	for _, c := range cases {
		info := &SmartInfo{nvmeLog: nvmeHealth{present: true, CriticalWarning: c.crit, AvailableSpare: c.spare, SpareThreshold: c.thres}}
		w := strings.Join(SmartWarnings(info), "|")
		if got := strings.Contains(w, "spare cells exhausted"); got != c.want {
			t.Errorf("%s: spare warning %v, want %v (%s)", c.name, got, c.want, w)
		}
		if got := strings.Contains(w, "NVMe critical warning"); got != (c.crit&^1 != 0) {
			t.Errorf("%s: critical warning text %v (%s)", c.name, got, w)
		}
	}
}
