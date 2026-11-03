package storage

import "testing"

func TestParseZCacheDevs(t *testing.T) {
	st := `  pool: tank
 state: ONLINE
config:

	NAME                STATE     READ WRITE CKSUM
	tank                ONLINE       0     0     0
	  mirror-0          ONLINE       0     0     0
	    /dev/sda1       ONLINE       0     0     0
	    /dev/sdb1       ONLINE       0     0     0
	logs
	  mirror-1          ONLINE       0     0     0
	    /dev/nvme0n1p1  ONLINE       0     0     0
	    /dev/nvme1n1p1  ONLINE       0     0     0
	cache
	  /dev/nvme2n1      ONLINE       0     0     0
	spares
	  /dev/sdc1         AVAIL

errors: No known data errors
`
	got := parseZCacheDevs("tank", st)
	want := []ZCacheDev{{"tank", "slog", "/dev/nvme0n1p1"}, {"tank", "slog", "/dev/nvme1n1p1"}, {"tank", "l2arc", "/dev/nvme2n1"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestParseARCStats(t *testing.T) {
	h, m, ok := parseARCStats("name type data\nhits 4 100\nl2_hits 4 30\nl2_misses 4 70\n")
	if !ok || h != 30 || m != 70 {
		t.Errorf("got %d %d %v", h, m, ok)
	}
	if _, _, ok := parseARCStats("hits 4 1\n"); ok {
		t.Error("no l2 counters must not be ok")
	}
}

func TestCacheDataDevice(t *testing.T) {
	rows := []map[string]string{
		{"lv_name": "data", "vg_name": "vg0", "pool_lv": "[data_ssd_cpool]", "devices": "data_corig(0)"},
		{"lv_name": "[data_ssd_cpool]", "vg_name": "vg0", "pool_lv": "", "devices": "data_ssd_cpool_cdata(0)"},
		{"lv_name": "[data_ssd_cpool_cdata]", "vg_name": "vg0", "pool_lv": "", "devices": "/dev/nvme0n1p1(0)"},
		{"lv_name": "[data_corig]", "vg_name": "vg0", "pool_lv": "", "devices": "/dev/md0(0)"},
	}
	if d := cacheDataDevice(rows, "vg0", "data"); d != "/dev/nvme0n1p1" {
		t.Errorf("cache pool: %q", d)
	}
	vol := []map[string]string{
		{"lv_name": "data", "vg_name": "vg0", "pool_lv": "[fast_cvol]", "devices": "data_corig(0)"},
		{"lv_name": "[fast_cvol]", "vg_name": "vg0", "devices": "/dev/sdc(0)"},
	}
	if d := cacheDataDevice(vol, "vg0", "data"); d != "/dev/sdc" {
		t.Errorf("cache volume: %q", d)
	}
	if d := cacheDataDevice(vol, "vg0", "other"); d != "" {
		t.Errorf("no cache: %q", d)
	}
}
