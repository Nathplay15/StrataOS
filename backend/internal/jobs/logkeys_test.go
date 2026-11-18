package jobs

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestMakeLogKey(t *testing.T) {
	for _, c := range []struct {
		format string
		args   []any
		key    string
		out    []string
		ok     bool
	}{
		{"Copying %s to %s", []any{"a", "b"}, "Copying {0} to {1}", []string{"a", "b"}, true},
		{"%d packages to upgrade (%.1f%% done): %v", []any{3, 12.34, errors.New("x")}, "{0} packages to upgrade ({1}% done): {2}", []string{"3", "12.3", "x"}, true},
		{"Disk %q is busy", []any{"sda"}, "Disk {0} is busy", []string{`"sda"`}, true},
		{"$ %s %s", []any{"ls", "-l"}, "", nil, false},  // no words
		{"Two %[1]s %[1]s", []any{"x"}, "", nil, false}, // explicit indexes
		{"Missing %s %s", []any{"x"}, "", nil, false},
		{"Plain line", nil, "Plain line", nil, true},
	} {
		key, out, ok := MakeLogKey(c.format, c.args)
		if key != c.key || !slices.Equal(out, c.out) || ok != c.ok {
			t.Errorf("%q: %q %q %v", c.format, key, out, ok)
		}
	}
}

func TestLogfKeys(t *testing.T) {
	j := &Job{}
	j.Logf("Starting %s", "backup")
	j.Write([]byte("raw output without newline"))
	j.Logf("Two lines:\n%s", "second")
	j.Write([]byte("more\n"))
	j.Logf("$ %s", "cmd")
	s := j.snapshot(true)
	lines := strings.Split(s.Log, "\n")
	if len(s.LogKeys) != 2 {
		t.Fatalf("keys %+v", s.LogKeys)
	}
	k := s.LogKeys[1]
	if k.Line != 2 || k.Lines != 2 || lines[k.Line] != "Two lines:" || lines[k.Line+1] != "second" || k.Key != "Two lines:\n{0}" {
		t.Fatalf("second key %+v in %q", k, s.Log)
	}
	if s.LogKeys[0].Line != 0 || lines[0] != "Starting backup" {
		t.Fatalf("first key %+v", s.LogKeys[0])
	}
}
