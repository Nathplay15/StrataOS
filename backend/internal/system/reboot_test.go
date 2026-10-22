package system

import "testing"

func TestNewerKernel(t *testing.T) {
	for _, c := range []struct {
		running   string
		installed []string
		want      bool
	}{
		{"6.12.43+deb13-amd64", []string{"6.12.43+deb13-amd64"}, false},
		{"6.12.43+deb13-amd64", []string{"6.12.43+deb13-amd64", "6.12.48+deb13-amd64"}, true},
		{"6.12.48+deb13-amd64", []string{"6.12.9+deb13-amd64"}, false},
		{"6.12.9+deb13-amd64", []string{"6.12.10+deb13-amd64"}, true},
		{"6.12.43+deb13-arm64", []string{"6.1.0-30-arm64"}, false},
		{"6.1.0-30-amd64", []string{"6.1.0-31-amd64"}, true},
		{"", []string{"6.1.0-31-amd64"}, false},
		{"6.1.0-31-amd64", nil, false},
	} {
		if got := NewerKernel(c.running, c.installed); got != c.want {
			t.Errorf("NewerKernel(%q, %v) = %v, want %v", c.running, c.installed, got, c.want)
		}
	}
}
