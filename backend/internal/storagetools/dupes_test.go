package storagetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindDuplicates(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	write(t, filepath.Join(a, "one.bin"), 300000)
	write(t, filepath.Join(b, "copy.bin"), 300000)
	write(t, filepath.Join(b, "deeper/copy2.bin"), 300000)
	// Same size and same ends, different middle: not a duplicate.
	data := make([]byte, 300000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	data[150000] ^= 0xff
	_ = os.WriteFile(filepath.Join(b, "almost.bin"), data, 0o644)
	// A hard link of one.bin is the same file, not a copy.
	_ = os.Link(filepath.Join(a, "one.bin"), filepath.Join(a, "link.bin"))
	// Two hard links alone are no duplicates at all.
	write(t, filepath.Join(a, "solo.dat"), 123456)
	_ = os.Link(filepath.Join(a, "solo.dat"), filepath.Join(b, "solo-link.dat"))
	// Too small, and inside a recycle bin: ignored.
	write(t, filepath.Join(a, "small1"), 10)
	write(t, filepath.Join(b, "small2"), 10)
	write(t, filepath.Join(b, ".recycle/admin/one.bin"), 300000)

	rep, err := FindDuplicates(context.Background(), []string{a, b, filepath.Join(b, "deeper")}, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Roots) != 2 {
		t.Fatalf("nested root kept: %v", rep.Roots)
	}
	if len(rep.Groups) != 1 {
		t.Fatalf("groups: %+v", rep.Groups)
	}
	g := rep.Groups[0]
	if len(g.Files) != 3 || g.Wasted != 600000 || rep.Wasted != 600000 {
		t.Fatalf("group: %+v", g)
	}
	for _, f := range g.Files {
		if strings.Contains(f.Path, "almost") || strings.Contains(f.Path, ".recycle") {
			t.Fatalf("unexpected member %s", f.Path)
		}
	}

	// Last-copy rule.
	all := []string{g.Files[0].Path, g.Files[1].Path, g.Files[2].Path}
	if _, err := PlanDeletion(rep, all); err == nil {
		t.Fatal("deleting every copy must be refused")
	}
	if _, err := PlanDeletion(rep, []string{filepath.Join(a, "nope.bin")}); err == nil {
		t.Fatal("unknown path must be refused")
	}
	plan, err := PlanDeletion(rep, all[1:])
	if err != nil || len(plan) != 2 || len(plan[0].Keeps) != 1 || plan[0].Keeps[0].Path != all[0] {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if !Unchanged(g.Files[1], g.Size) {
		t.Fatal("file reported changed")
	}
	_ = os.WriteFile(g.Files[1].Path, []byte("changed"), 0o644)
	if Unchanged(g.Files[1], g.Size) {
		t.Fatal("changed file not detected")
	}
	Forget(rep, map[string]bool{all[1]: true})
	if len(rep.Groups) != 1 || len(rep.Groups[0].Files) != 2 || rep.Wasted != 300000 {
		t.Fatalf("after forget: %+v", rep.Groups)
	}
	Forget(rep, map[string]bool{all[2]: true})
	if len(rep.Groups) != 0 || rep.Wasted != 0 {
		t.Fatalf("group with one copy left kept: %+v", rep.Groups)
	}
}

func TestCollapseLinks(t *testing.T) {
	got := collapseLinks([]candidate{{path: "b", dev: 1, ino: 7}, {path: "a", dev: 1, ino: 7}, {path: "c", dev: 2, ino: 7}})
	if len(got) != 2 || got[0].path != "a" || got[1].path != "c" {
		t.Fatalf("%+v", got)
	}
}
