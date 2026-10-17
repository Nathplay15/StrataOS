package accounts

import "testing"

func TestValidMemberGroup(t *testing.T) {
	for _, g := range []string{"users", "docker"} {
		if err := ValidMemberGroup(g); err != nil {
			t.Errorf("%q rejected: %v", g, err)
		}
	}
	for _, g := range []string{"sudo", "root", "disk", "shadow", "adm", "-x", "a,sudo", "strata-no-such-group"} {
		if ValidMemberGroup(g) == nil {
			t.Errorf("%q accepted", g)
		}
	}
}
