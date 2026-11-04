package restore

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/protect"
)

func write(t *testing.T, p, data string, mt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// A snapshot tree of /v/Docs and the live folder it came from.
func setup(t *testing.T) (snap, live string, loc Locator) {
	dir := t.TempDir()
	snap, live = filepath.Join(dir, "snap"), filepath.Join(dir, "live")
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	write(t, snap+"/Docs/a.txt", "version A", old)
	write(t, snap+"/Docs/sub/b.txt", "version B", old)
	write(t, snap+"/Docs/c.txt", "version C", old)
	write(t, snap+"/.snapshots/x/never.txt", "no", old)
	write(t, live+"/Docs/a.txt", "version A", old)            // same
	write(t, live+"/Docs/c.txt", "changed later", time.Now()) // conflict
	tr := tree{Root: snap, Base: "/v"}
	return snap, live, func(string) (tree, bool) { return tr, true }
}

func run(t *testing.T, loc Locator, live, policy string) Result {
	t.Helper()
	items := []protect.Entry{{Path: "/v/Docs", Dir: true}}
	r, err := Apply(context.Background(), loc, Plan{Items: items, DstDir: live, Dest: func(abs string) string { return live + strings.TrimPrefix(abs, "/v") }, Policy: policy}, Progress{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "missing"
	}
	return string(b)
}

func TestApplySkip(t *testing.T) {
	_, live, loc := setup(t)
	r := run(t, loc, live, PolicySkip)
	if r.Files != 1 || r.Same != 1 || r.Skipped != 1 || r.Errors != 0 {
		t.Fatalf("result %+v", r)
	}
	if read(t, live+"/Docs/sub/b.txt") != "version B" || read(t, live+"/Docs/c.txt") != "changed later" {
		t.Fatal("skip policy")
	}
	fi, _ := os.Stat(live + "/Docs/sub/b.txt")
	if time.Since(fi.ModTime()) < 47*time.Hour {
		t.Fatal("date not restored")
	}
}

func TestApplyOverwriteAndRename(t *testing.T) {
	_, live, loc := setup(t)
	r := run(t, loc, live, PolicyRename)
	if r.Renamed != 1 || read(t, live+"/Docs/c.txt") != "changed later" {
		t.Fatalf("rename: %+v", r)
	}
	ents, _ := os.ReadDir(live + "/Docs")
	found := false
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "c (restored ") {
			found = read(t, path.Join(live, "Docs", e.Name())) == "version C"
		}
		if strings.Contains(e.Name(), "strata-restore-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
	if !found {
		t.Fatal("renamed copy missing")
	}
	r = run(t, loc, live, PolicyOverwrite)
	if read(t, live+"/Docs/c.txt") != "version C" || r.Errors != 0 {
		t.Fatalf("overwrite: %+v", r)
	}
	if _, err := os.Stat(live + "/.snapshots"); err == nil {
		t.Fatal(".snapshots restored")
	}
}

func TestPreviewAndList(t *testing.T) {
	_, live, loc := setup(t)
	tr, _ := loc("")
	list, truncated, err := tr.list(context.Background(), "/v", true, 100)
	if err != nil || truncated {
		t.Fatal(err)
	}
	for _, e := range list {
		if strings.Contains(e.Path, ".snapshots") {
			t.Fatalf("listed %s", e.Path)
		}
	}
	docs, _, _ := tr.list(context.Background(), "/v/Docs", true, 100)
	pv := Preview([]protect.Entry{{Path: "/v/Docs", Dir: true}}, docs, func(abs string) string { return live + strings.TrimPrefix(abs, "/v") })
	if pv.Files != 3 || pv.New != 1 || pv.Same != 1 || pv.Conflicts != 1 {
		t.Fatalf("preview %+v", pv)
	}
	if _, _, err := tr.list(context.Background(), "/v", true, 2); err != nil {
		t.Fatal(err)
	}
	if _, trunc, _ := tr.list(context.Background(), "/v", true, 2); !trunc {
		t.Fatal("not truncated")
	}
}
