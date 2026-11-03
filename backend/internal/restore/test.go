package restore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"time"

	"strataos/internal/protect"
	"strataos/internal/store"
)

// Restore tests: a sample of files of the newest point is restored to a
// scratch folder, then each one is read back and compared with the
// original when the original has not changed since (same size and date).

// TestDir holds the scratch folders of the restore tests.
var TestDir = "/var/tmp"

const (
	testFiles  = 12
	testBudget = 128 << 20 // bytes restored per test at most
)

func hashFile(p string) ([]byte, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return h.Sum(nil), n, err
}

// RunTest restores a sample of the newest point of src and checks it.
func RunTest(ctx context.Context, src Source, p Progress) store.RestoreTest {
	res := store.RestoreTest{Time: time.Now(), Status: "failed"}
	fail := func(format string, a ...any) store.RestoreTest {
		res.Message = fmt.Sprintf(format, a...)
		p.Log("%s", res.Message)
		return res
	}
	points, err := src.Points(ctx)
	if err != nil {
		return fail("Cannot read the backup: %v", err)
	}
	if len(points) == 0 {
		return fail("The backup has no version to restore yet")
	}
	pt := points[0]
	for _, x := range points {
		if x.Time.After(pt.Time) {
			pt = x
		}
	}
	p.Log("Version of %s", stamp(pt.Time))
	var files []protect.Entry
	for _, root := range pt.Roots {
		list, _, err := src.List(ctx, pt.ID, root, true)
		if err != nil {
			return fail("Cannot list %s in the backup: %v", root, err)
		}
		files = append(files, list...)
	}
	sample := protect.Sample(files, testFiles, testBudget, rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x5eed)))
	if len(sample) == 0 {
		return fail("No file to test in the backup (%d entries)", len(files))
	}
	scratch, err := os.MkdirTemp(TestDir, "strata-restore-test-")
	if err != nil {
		return fail("%v", err)
	}
	defer os.RemoveAll(scratch)
	stage := filepath.Join(scratch, "stage")
	out := filepath.Join(scratch, "out")
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fail("%v", err)
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return fail("%v", err)
	}
	loc, err := src.Stage(ctx, pt.ID, sample, stage, Progress{Set: func(float64) {}, Log: p.Log})
	if err != nil {
		return fail("Restore failed: %v", err)
	}
	r, err := Apply(ctx, loc, Plan{Items: sample, DstDir: out, Dest: func(abs string) string { return path.Join(out, abs) }, Policy: PolicyOverwrite}, Progress{Log: p.Log})
	if err != nil {
		return fail("Restore failed: %v", err)
	}
	if r.Errors > 0 {
		return fail("Restore failed for %d file(s): %s", r.Errors, r.First[0])
	}
	var bad []string
	for _, f := range sample {
		got := path.Join(out, f.Path)
		sum, n, err := hashFile(got)
		if err != nil || n != f.Size {
			bad = append(bad, fmt.Sprintf("%s (restored %d bytes of %d)", f.Path, n, f.Size))
			continue
		}
		res.Files++
		res.Bytes += n
		// Compared with the original only when it has not changed since.
		if fi, err := os.Lstat(f.Path); err == nil && fi.Mode().IsRegular() && fi.Size() == f.Size && sameSecond(fi.ModTime(), f.MTime) {
			orig, _, err := hashFile(f.Path)
			if err == nil && string(orig) != string(sum) {
				bad = append(bad, f.Path+" (content differs from the original)")
				continue
			}
			if err == nil {
				res.Verified++
			}
		}
	}
	if len(bad) > 0 {
		return fail("%d of %d file(s) did not come back intact: %s", len(bad), len(sample), bad[0])
	}
	res.Status = "success"
	res.Message = fmt.Sprintf("%d file(s) restored from the version of %s, %d compared with the original", res.Files, stamp(pt.Time), res.Verified)
	p.Log("%s", res.Message)
	return res
}

func sameSecond(a, b time.Time) bool {
	d := a.Sub(b)
	return d < time.Second && d > -time.Second
}
