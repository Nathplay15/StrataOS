package system

import (
	"strings"
	"testing"
)

const strataSources = `# StrataOS updates
Types: deb
URIs: https://nathplay15.github.io/StrataOS/apt
Suites: stable
Components: main
Signed-By: /usr/share/keyrings/strataos-archive.gpg
`

func TestBetaSources(t *testing.T) {
	got, err := BetaSourcesFor(strataSources)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nTypes: deb\n", "\nURIs: https://nathplay15.github.io/StrataOS/apt\n", "\nSuites: testing\n", "\nComponents: main\n", "\nSigned-By: /usr/share/keyrings/strataos-archive.gpg\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "stable\n") && strings.Contains(got, "Suites: stable") {
		t.Errorf("stable suite kept:\n%s", got)
	}
	for _, bad := range []string{
		"",
		"Types: deb-src\nURIs: https://x\nSuites: stable\nComponents: main\n",
		"Types: deb\nURIs: https://x\nSuites: stable\nComponents: main\nEnabled: no\n",
		"Types: deb\nURIs: https://x;rm\nSuites: stable\nComponents: main\n",
	} {
		if out, err := BetaSourcesFor(bad); err == nil {
			t.Errorf("accepted %q:\n%s", bad, out)
		}
	}
}

func TestSecuritySources(t *testing.T) {
	files := map[string]string{
		"/etc/apt/sources.list": `# main archive
deb http://deb.debian.org/debian/ trixie main non-free-firmware
deb-src http://deb.debian.org/debian/ trixie main
deb [arch=amd64 signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://security.debian.org/debian-security trixie-security main non-free-firmware
deb http://deb.debian.org/debian/ trixie-updates main
`,
		"/etc/apt/sources.list.d/debian.sources": `Types: deb deb-src
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main

Types: deb
URIs: http://mirror.example/debian-security
Suites: trixie-security
Components: main contrib
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`,
		"/etc/apt/sources.list.d/strataos.sources": strataSources,
		"/etc/apt/sources.list.d/docker.list":      "deb [signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable\n",
	}
	got := SecuritySourcesFor(files, "trixie")
	if strings.Count(got, "Types: deb\n") != 2 {
		t.Fatalf("want 2 security entries:\n%s", got)
	}
	for _, want := range []string{
		"URIs: http://security.debian.org/debian-security\nSuites: trixie-security\nComponents: main non-free-firmware\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nArchitectures: amd64\n",
		"URIs: http://mirror.example/debian-security\nSuites: trixie-security\nComponents: main contrib\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"Suites: trixie\n", "trixie-updates", "strataos", "docker", "stable"} {
		if strings.Contains(got, bad) {
			t.Errorf("non-security source %q kept:\n%s", bad, got)
		}
	}

	// No security entry: Debian's security archive for the release.
	got = SecuritySourcesFor(map[string]string{"/etc/apt/sources.list": "deb http://deb.debian.org/debian trixie main\n"}, "trixie")
	if !strings.Contains(got, "URIs: http://security.debian.org/debian-security\nSuites: trixie-security\n") {
		t.Errorf("fallback:\n%s", got)
	}
}

func TestMadisonVersions(t *testing.T) {
	out := `  strataos | 0.8.0~beta.1 | https://nathplay15.github.io/StrataOS/apt testing/main amd64 Packages
  strataos |      0.7.1 | https://nathplay15.github.io/StrataOS/apt stable/main amd64 Packages
  strataos |      0.7.1 | https://nathplay15.github.io/StrataOS/apt testing/main amd64 Packages
  strataos |      0.7.0 | https://nathplay15.github.io/StrataOS/apt stable/main amd64 Packages
  strataos |     0.10.0 | file:/var/tmp/repo stable/main all Packages
`
	if got := Newest(MadisonVersions(out, "stable")); got != "0.10.0" {
		t.Errorf("stable: %s", got)
	}
	if got := Newest(MadisonVersions(out, "testing")); got != "0.8.0~beta.1" {
		t.Errorf("testing: %s", got)
	}
	if got := Newest(MadisonVersions("", "stable")); got != "" {
		t.Errorf("empty: %s", got)
	}
}
