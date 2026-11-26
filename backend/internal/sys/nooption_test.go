package sys

import "testing"

func TestNoOption(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"alice", true},
		{"a-b", true},
		{"/srv/strata/x", true},
		{"", true},
		{"-x", false},
		{"--help", false},
		{"-", false},
		{"-oProxyCommand=id", false},
	}
	for _, c := range cases {
		if err := NoOption(c.in); (err == nil) != c.ok {
			t.Errorf("NoOption(%q) = %v, want ok=%v", c.in, err, c.ok)
		}
	}
	if err := NoOptions("a", "b"); err != nil {
		t.Errorf("NoOptions valid: %v", err)
	}
	if err := NoOptions("a", "-b"); err == nil {
		t.Error("NoOptions must reject -b")
	}
}
