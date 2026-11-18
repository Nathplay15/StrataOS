package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/health"
	"strataos/internal/store"
)

func TestFsIdentities(t *testing.T) {
	// A disk with an ext4 partition, a RAID member holding LVM and an
	// encrypted Btrfs, and a ZFS member.
	js := `{"blockdevices":[{"name":"/dev/sdb","fstype":null,"uuid":null,"label":null,"children":[
	 {"name":"/dev/sdb1","fstype":"ext4","uuid":"e1","label":null},
	 {"name":"/dev/sdb2","fstype":"linux_raid_member","uuid":"r1","label":"nas:0","children":[
	  {"name":"/dev/md0","fstype":"LVM2_member","uuid":"pv","label":null,"children":[
	   {"name":"/dev/mapper/vg-data","fstype":"crypto_LUKS","uuid":"luks","label":null,"children":[
	    {"name":"/dev/mapper/data","fstype":"btrfs","uuid":"b1","label":"data"}]}]}]},
	 {"name":"/dev/sdb3","fstype":"zfs_member","uuid":"z","label":"tank"}]}]}`
	got := fsIdentities([]byte(js))
	want := []string{"uuid:e1", "uuid:b1", "zfs:tank"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for in, want := range map[string]string{
		"zfs tank/media\n":                "zfs:tank",
		"btrfs /dev/mapper/data[/sub] b1": "uuid:b1",
		"ext4 /dev/sdb1 e1\n":             "uuid:e1",
		"tmpfs tmpfs":                     "",
	} {
		if got := fsIdentityOf(in); got != want {
			t.Errorf("fsIdentityOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDuArgs(t *testing.T) {
	all := []string{"/srv/strata/v/A", "/srv/strata/v/A/B", "/srv/strata/v/AB", "/srv/strata/v/A/[x]*"}
	nested := nestedShares("/srv/strata/v/A", all)
	if !slices.Equal(nested, []string{"/srv/strata/v/A/B", "/srv/strata/v/A/[x]*"}) {
		t.Fatalf("nested: %v", nested)
	}
	got := strings.Join(duArgs("/srv/strata/v/A", nested), " ")
	want := `-n 19 ionice -c3 du -sbx --exclude=.recycle --exclude=.snapshots --exclude=/srv/strata/v/A/B --exclude=/srv/strata/v/A/\[x\]\* -- /srv/strata/v/A`
	if got != want {
		t.Fatalf("du args:\n%s\nwant\n%s", got, want)
	}
}

// One measurement: the shares (with du, nested shares, bins and snapshots
// left out), every account, and the histories of what is gone forgotten.
func TestMeasureShareUsage(t *testing.T) {
	for _, c := range []string{"du", "nice", "ionice"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip(c + " missing")
		}
	}
	s := newTestServer(t)
	s.hist = health.NewStore(t.TempDir())
	dir := t.TempDir()
	a, b := filepath.Join(dir, "A"), filepath.Join(dir, "A", "B")
	for f, n := range map[string]int{"A/one": 1000, "A/B/two": 300, "A/.recycle/alice/old": 5000, "A/.snapshots/x/old": 7000} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(a, "one"), filepath.Join(a, "one-again")); err != nil {
		t.Fatal(err)
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Shares = append(st.Shares, &store.Share{Name: "A", Path: a}, &store.Share{Name: "B", Path: b})
		st.Users = append(st.Users, &store.User{Username: "alice"})
		return nil
	})
	s.hist.RecordVolumes(map[string]health.VolumePoint{"share:/gone": {Used: 1}, "user:bob": {Used: 1}, "pool": {Used: 1}}, time.Now().Add(-2*time.Hour))
	s.measureShareUsage(context.Background(), nil)
	used := func(k string) int64 {
		pts := s.hist.Volume(k)
		if len(pts) == 0 {
			return -1
		}
		return pts[len(pts)-1].Used
	}
	// du counts the folders too: compare with a margin of a few blocks.
	if ua := used("share:" + a); ua < 1000 || ua > 1000+2*4096 {
		t.Errorf("A: %d (hard link once, B, .recycle and .snapshots left out)", ua)
	}
	if ub := used("share:" + b); ub < 300 || ub > 300+2*4096 {
		t.Errorf("B: %d", ub)
	}
	if used("share:/gone") != -1 || used("user:bob") != -1 || used("pool") != 1 {
		t.Errorf("forgotten: gone %d, bob %d, pool %d", used("share:/gone"), used("user:bob"), used("pool"))
	}
}

func TestShareUsageDue(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	if !s.shareUsageDue(now) {
		t.Fatal("never measured: due")
	}
	s.hist = nil
	s.recordShareUsage(nil) // no history: nothing starts, no panic
	if _, err := os.Stat(s.shareUsagePath()); err == nil {
		t.Fatal("started without a history")
	}
	if err := os.WriteFile(s.shareUsagePath(), []byte(`{"at":"`+now.Add(-time.Hour).Format(time.RFC3339)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.shareUsageDue(now) {
		t.Error("measured an hour ago (before a restart): not due")
	}
	if !s.shareUsageDue(now.Add(24 * time.Hour)) {
		t.Error("a day later: due")
	}
	// A run in progress is never started twice.
	s.hist = health.NewStore(t.TempDir())
	shareUsageRunning.Store(true)
	defer shareUsageRunning.Store(false)
	_ = os.Remove(s.shareUsagePath())
	s.recordShareUsage(nil)
	if _, err := os.Stat(s.shareUsagePath()); err == nil {
		t.Error("a second run started")
	}
}
