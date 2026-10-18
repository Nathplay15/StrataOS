package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNotifyLangMigration(t *testing.T) {
	// A state file of StrataOS 0.4: notifications were in French by default.
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "state.json"), []byte(`{"users":[{"username":"admin"}],"settings":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(old)
	if err != nil {
		t.Fatal(err)
	}
	if s.st.Settings.NotifyLang != "fr" {
		t.Errorf("0.4 state: notifyLang = %q, want fr", s.st.Settings.NotifyLang)
	}

	// A new installation keeps English across restarts.
	dir := t.TempDir()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error { st.Users = append(st.Users, &User{Username: "admin"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	if s.st.Settings.NotifyLang != "" {
		t.Errorf("new state: notifyLang = %q after a restart, want empty (English)", s.st.Settings.NotifyLang)
	}
}
