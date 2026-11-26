package sys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJSONKeepsDamagedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	var m map[string]int
	if err := LoadJSON(p, &m); err != nil || m != nil {
		t.Fatalf("missing file: %v %v", err, m)
	}
	os.WriteFile(p, []byte(`{"a":1}`), 0o600)
	if err := LoadJSON(p, &m); err != nil || m["a"] != 1 {
		t.Fatalf("good file: %v %v", err, m)
	}
	os.WriteFile(p, []byte(`{"a":`), 0o600)
	if err := LoadJSON(p, &m); err == nil {
		t.Fatal("damaged file accepted")
	}
	if c, _ := filepath.Glob(p + ".corrupt-*"); len(c) != 1 {
		t.Fatalf("no copy kept: %v", c)
	}
}
