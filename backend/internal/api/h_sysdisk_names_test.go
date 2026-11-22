package api

import "testing"

func TestSameNames(t *testing.T) {
	for _, c := range []struct {
		typed, want string
		ok          bool
	}{
		{"sdb, sda", "sda, sdb", true}, {"sda,sdb", "sda, sdb", true}, {"sda", "sda, sdb", false}, {"", "", false}, {"vda", "vda", true}, {"sda, sda", "sda, sdb", false},
	} {
		if sameNames(c.typed, c.want) != c.ok {
			t.Errorf("%q vs %q", c.typed, c.want)
		}
	}
}
