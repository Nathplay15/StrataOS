package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A change that cannot be written is reported, and memory keeps what is on
// disk.
func TestUpdateWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error { st.Settings.Workgroup = "ONE"; return nil }); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	err = s.Update(func(st *State) error { st.Settings.Workgroup = "TWO"; return nil })
	if !errors.Is(err, ErrNotSaved) {
		t.Fatalf("err = %v", err)
	}
	s.Read(func(st *State) {
		if st.Settings.Workgroup != "ONE" {
			t.Fatalf("memory has %q, disk has ONE", st.Settings.Workgroup)
		}
	})
}

// A damaged state.json: the copy of the last start is used, the damaged
// file kept aside.
func TestDamagedStateUsesLastGoodCopy(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Update(func(st *State) error { st.Settings.Workgroup = "KEPT"; return nil })
	if _, err := Open(dir); err != nil { // a restart: state.json.prev written
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"settings":`), 0o600)
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2.Read(func(st *State) {
		if st.Settings.Workgroup != "KEPT" {
			t.Fatalf("workgroup %q", st.Settings.Workgroup)
		}
	})
	if c, _ := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*")); len(c) != 1 {
		t.Fatalf("damaged file not kept: %v", c)
	}
}
