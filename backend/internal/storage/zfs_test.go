package storage

import "testing"

func TestValidDS(t *testing.T) {
	for _, ok := range []string{"tank", "tank/media", "tank/media@first", "tank@auto-ab12-20261017-005400"} {
		if err := validDS(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "tank/", "tank/../x", "tank@", "tank@a@b", "tank@a/b", "-tank", "tank media"} {
		if validDS(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseProblems(t *testing.T) {
	status := `  pool: tank
 state: DEGRADED
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      DEGRADED     0     0     0
	  mirror-0                                DEGRADED     0     0     0
	    934868875068009117                    UNAVAIL      0     0     0  was /dev/disk/by-id/ata-HDD_4-part1
	    /dev/disk/by-id/ata-HDD_5-part1       ONLINE       0     0     0
	    /dev/disk/by-id/ata-HDD_7-part1       FAULTED      3     0     0  too many errors
	spares
	  /dev/disk/by-id/ata-HDD_6-part1         AVAIL

errors: No known data errors
`
	p := parseProblems(status)
	if len(p) != 2 || p[0].Device != "934868875068009117" || p[0].Was != "/dev/disk/by-id/ata-HDD_4-part1" || p[1].State != "FAULTED" {
		t.Fatalf("unexpected problems: %+v", p)
	}
}

func TestParseZScan(t *testing.T) {
	cases := []struct {
		line string
		want ZScan
	}{
		{"  scan: scrub in progress since Tue Oct 20 10:00:00 2026\n\t1.2G scanned at 100M/s, 42.10% done", ZScan{Kind: "scrub", Running: true}},
		{"  scan: resilver in progress since Tue Oct 20 10:00:00 2026", ZScan{Kind: "resilver", Running: true}},
		{"  scan: scrub paused since Tue Oct 20 10:00:00 2026", ZScan{Kind: "scrub", Running: true}},
		{"  scan: scrub repaired 0B in 00:00:01 with 0 errors on Tue Oct 20 10:00:00 2026", ZScan{Kind: "scrub"}},
		{"  scan: scrub repaired 12K in 01:02:03 with 3 errors on Tue Oct 20 10:00:00 2026", ZScan{Kind: "scrub", Errors: 3}},
		{"  scan: resilvered 1.50G in 00:01:02 with 0 errors on Tue Oct 20 10:00:00 2026", ZScan{Kind: "resilver"}},
		{"  scan: scrub canceled on Tue Oct 20 10:00:00 2026", ZScan{Kind: "scrub", Canceled: true}},
		{"  scan: none requested", ZScan{}},
		{"  pool: tank\n state: ONLINE", ZScan{}},
	}
	for _, c := range cases {
		if got := ParseZScan("  pool: tank\n state: ONLINE\n" + c.line + "\nconfig:\n"); got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.line, got, c.want)
		}
	}
}
