package backup

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"strataos/internal/store"
)

// With restic and qemu-nbd installed: two similar disks backed up to one
// repository share their blocks, and a disk reads back identical.
func TestImageRoundTrip(t *testing.T) {
	for _, c := range []string{"restic", "qemu-nbd", "qemu-img"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip(c + " not installed")
		}
	}
	dir := t.TempDir()
	NBDRun = filepath.Join(dir, "run")
	task := &store.BackupTask{ID: "it", Kind: "restic", DestType: "local", DestPath: filepath.Join(dir, "repo"), Password: "test-password"}
	ctx := context.Background()
	p := Progress{Set: func(float64) {}, Log: func(f string, a ...any) { t.Logf(f, a...) }}
	if err := ensureRepo(ctx, task, p); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 6<<20)
	rand.Read(data)
	mk := func(name string, extra string) string {
		raw := filepath.Join(dir, name+".raw")
		d := append([]byte(nil), data...)
		copy(d[3<<20:], extra)
		if err := os.WriteFile(raw, append(make([]byte, 2<<20), d...), 0o600); err != nil {
			t.Fatal(err)
		}
		img := filepath.Join(dir, name+".qcow2")
		if out, err := exec.Command("qemu-img", "convert", "-O", "qcow2", raw, img).CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
		return img
	}
	at := time.Now().Truncate(time.Second)
	var ids []string
	for _, name := range []string{"a", "b"} {
		img := mk(name, "different "+name)
		n, done, err := openImage(ctx, img, "qcow2", false)
		if err != nil {
			t.Fatal(err)
		}
		sum, err := backupStream(ctx, task, &nbdReader{n: n}, "vda.raw", at, []string{"sg-vm-" + name, "sp-disk-vda"}, p)
		done()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes, %d added", name, sum.TotalBytes, sum.DataAddedPacked)
		if name == "b" && sum.DataAddedPacked > 4<<20 {
			t.Errorf("the second disk shares little: %d bytes added", sum.DataAddedPacked)
		}
		ids = append(ids, sum.SnapshotID)
	}
	ds := &store.Datastore{DestType: "local", DestPath: task.DestPath, Password: task.Password}
	st, err := Stats(ctx, ds)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ratio <= 1.3 || st.Logical != 16<<20 {
		t.Fatalf("dedup: %+v", st)
	}
	out := filepath.Join(dir, "restored.qcow2")
	if err := writeDisk(ctx, task, ids[1], "vda", out, 8<<20, p); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("qemu-img", "compare", "-q", filepath.Join(dir, "b.qcow2"), out).CombinedOutput(); err != nil {
		t.Fatalf("restored disk differs: %s", o)
	}
	run := Verify(ctx, ds, "all", p)
	if run.Status != "success" {
		t.Fatalf("verify: %+v", run)
	}
}
