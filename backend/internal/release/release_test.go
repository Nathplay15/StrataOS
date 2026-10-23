package release

import (
	"slices"
	"testing"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		tag, version, file string
		pre                bool
		suites             []string
	}{
		{"v0.7.0", "0.7.0", "0.7.0", false, []string{"stable", "testing"}},
		{"v0.8.0-beta.1", "0.8.0~beta.1", "0.8.0-beta.1", true, []string{"testing"}},
		{"v0.8.0-rc.2", "0.8.0~rc.2", "0.8.0-rc.2", true, []string{"testing"}},
		{"v1.10.3-alpha.12", "1.10.3~alpha.12", "1.10.3-alpha.12", true, []string{"testing"}},
	} {
		v, err := FromTag(c.tag)
		if err != nil || v != c.version {
			t.Errorf("FromTag(%s) = %q, %v", c.tag, v, err)
		}
		if Tag(c.version) != c.tag || FileVersion(c.version) != c.file || Prerelease(c.version) != c.pre || !slices.Equal(Suites(c.version), c.suites) {
			t.Errorf("%s: tag %s file %s pre %v suites %v", c.version, Tag(c.version), FileVersion(c.version), Prerelease(c.version), Suites(c.version))
		}
	}
	for _, bad := range []string{"0.8.0", "v0.8", "v0.8.0-beta", "v0.8.0-beta.0", "v0.8.0-preview.1", "v0.8.0~beta.1", "v01.2.3", "v0.8.0-beta.1-x", "v0.8.0+1"} {
		if v, err := FromTag(bad); err == nil {
			t.Errorf("FromTag(%s) accepted: %s", bad, v)
		}
	}
	for _, bad := range []string{"0.8.0-beta.1", "0.8", "0.8.0~beta", "0.8.0~Beta.1", ""} {
		if Valid(bad) == nil {
			t.Errorf("Valid(%q) accepted", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.7.0", "0.7.0", 0},
		{"0.8.0~beta.1", "0.8.0", -1},
		{"0.8.0~beta.1", "0.8.0~rc.1", -1},
		{"0.8.0~alpha.3", "0.8.0~beta.1", -1},
		{"0.8.0~beta.10", "0.8.0~beta.9", 1},
		{"0.8.0~beta.1", "0.7.9", 1},
		{"0.10.0", "0.9.0", 1},
		{"1:0.1", "0.9", 1},
		{"1.0-2", "1.0-10", -1},
		{"1.0", "1.0-0", 0},
		{"1.0~", "1.0", -1},
		{"1.0a", "1.0", 1},
		{"1.0+b1", "1.0a", 1},
		{"2.41-12+deb13u1", "2.41-12", 1},
	} {
		if got := Compare(c.a, c.b); sign(got) != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); sign(got) != -c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
