package system

import (
	"strings"
	"testing"
)

func TestRewriteSourcesDeb822(t *testing.T) {
	in := `Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates trixie-backports
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
Suites: trixie-security
URIs: http://security.debian.org/debian-security
Components: main

# a comment: Suites: trixie
Types: deb
URIs: https://pkgs.tailscale.com/stable/debian
Suites: trixie
Components: main

Types: deb
URIs: https://download.docker.com/linux/debian
Suites: trixie
Components: stable

Types: deb
URIs: https://nathplay15.github.io/StrataOS/apt
Suites: stable
Components: main
`
	out, ch := RewriteSources(in, true, "trixie", "forky", func(uri string) bool { return strings.Contains(uri, "tailscale") })
	for _, want := range []string{"Suites: forky forky-updates forky-backports", "Suites: forky-security", "URIs: https://pkgs.tailscale.com/stable/debian\nSuites: forky", "URIs: https://download.docker.com/linux/debian\nSuites: trixie", "Suites: stable", "# a comment: Suites: trixie"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	kept := 0
	for _, c := range ch {
		if c.Kept {
			kept++
			if !strings.Contains(c.URI, "docker") {
				t.Error("kept", c)
			}
		}
	}
	if len(ch) != 6 || kept != 1 {
		t.Fatalf("changes %+v", ch)
	}
}

func TestRewriteSourcesList(t *testing.T) {
	in := "deb http://ftp.fr.debian.org/debian/ trixie main\n" +
		"deb [signed-by=/usr/share/keyrings/x.gpg arch=amd64] http://security.debian.org/debian-security trixie-security main\n" +
		"deb-src http://ftp.fr.debian.org/debian/ trixie-updates main\n" +
		"deb https://archive.raspberrypi.com/debian/ trixie main\n" +
		"# deb http://deb.debian.org/debian trixie main\n" +
		"deb https://pkg.cloudflare.com/cloudflared any main\n"
	out, ch := RewriteSources(in, false, "trixie", "forky", func(string) bool { return false })
	want := "deb http://ftp.fr.debian.org/debian/ forky main\n" +
		"deb [signed-by=/usr/share/keyrings/x.gpg arch=amd64] http://security.debian.org/debian-security forky-security main\n" +
		"deb-src http://ftp.fr.debian.org/debian/ forky-updates main\n" +
		"deb https://archive.raspberrypi.com/debian/ trixie main\n" +
		"# deb http://deb.debian.org/debian trixie main\n" +
		"deb https://pkg.cloudflare.com/cloudflared any main\n"
	if out != want {
		t.Fatalf("got\n%s", out)
	}
	if len(ch) != 4 || !ch[3].Kept {
		t.Fatalf("%+v", ch)
	}
}

func TestReleaseHelpers(t *testing.T) {
	if NextRelease("trixie") != "forky" || NextRelease("sid") != "" || ReleaseNumber("forky") != "14" {
		t.Fatal("releases")
	}
	if ReleaseCodename("Origin: Debian\nSuite: stable\nCodename: forky\nVersion: 14.0\n") != "forky" || ReleaseCodename("Codename: x y") != "" {
		t.Fatal("codename")
	}
	for uri, deb := range map[string]bool{"http://deb.debian.org/debian": true, "http://mirror.example.net/debian/": false, "https://pkgs.tailscale.com/stable/debian": false, "https://download.docker.com/linux/ubuntu": false, "http://security.debian.org/debian-security": true} {
		if IsDebianURI(uri) != deb {
			t.Error(uri)
		}
	}
}

func TestUpgradeChecks(t *testing.T) {
	good := UpgradeFacts{From: "trixie", To: "forky", Moved: 4, FreeRoot: 10 * GiB, FreeVar: 10 * GiB, FreeBoot: -1, RestoreMode: "btrfs", ZFS: true}
	if c := UpgradeChecks(good); Blocked(c) {
		t.Fatalf("%+v", c)
	}
	for name, f := range map[string]func(*UpgradeFacts){
		"space":   func(f *UpgradeFacts) { f.FreeRoot = 2 * GiB },
		"boot":    func(f *UpgradeFacts) { f.FreeBoot = 100 << 20 },
		"jobs":    func(f *UpgradeFacts) { f.Jobs = []string{"Backup x"} },
		"dpkg":    func(f *UpgradeFacts) { f.DpkgAudit = "The following packages are only half configured" },
		"updates": func(f *UpgradeFacts) { f.PendingUpdates = 3 },
		"ceph":    func(f *UpgradeFacts) { f.CephNodes = 3 },
		"sources": func(f *UpgradeFacts) { f.Moved = 0 },
	} {
		x := good
		f(&x)
		if !Blocked(UpgradeChecks(x)) {
			t.Error("not blocked:", name)
		}
	}
	x := good
	x.Vendors = []SourceChange{{URI: "https://download.docker.com/linux/debian", From: "trixie", To: "trixie", Kept: true}}
	x.RestoreMode = ""
	if c := UpgradeChecks(x); Blocked(c) || !strings.Contains(c[len(c)-2].Text+c[len(c)-1].Text, "docker") {
		t.Fatalf("%+v", c)
	}
}

func TestAptCounter(t *testing.T) {
	if PlanCount("Inst a [1] (2 Debian:14)\nConf a (2)\nInst b (1)\n") != 2 {
		t.Fatal("plan")
	}
	c := AptCounter{Total: 2}
	c.Line("Unpacking a (2) over (1) ...")
	c.Line("Get:1 http://deb.debian.org …")
	if p := c.Line("Setting up a (2) ..."); p != 50 {
		t.Fatal(p)
	}
}

func TestMirrorListURI(t *testing.T) {
	read := func(p string) string {
		if p == "/etc/apt/mirrors/debian.list" {
			return "# mirrors\nhttps://deb.debian.org/debian\nhttps://other.example/debian\n"
		}
		return ""
	}
	if got := MirrorListURI("mirror+file:///etc/apt/mirrors/debian.list", read); got != "https://deb.debian.org/debian" {
		t.Fatal(got)
	}
	for _, same := range []string{"http://deb.debian.org/debian", "mirror+file:///nope"} {
		if MirrorListURI(same, read) != same {
			t.Fatal(same)
		}
	}
}

func TestNewestKernel(t *testing.T) {
	if got := NewestKernel([]string{"6.12.117+deb13-amd64", "7.2.15+deb14-amd64", "6.12.48+deb13-amd64"}); got != "7.2.15+deb14-amd64" {
		t.Fatal(got)
	}
	if got := NewestKernel([]string{"6.12.9+deb13-amd64", "6.12.48+deb13-amd64"}); got != "6.12.48+deb13-amd64" {
		t.Fatal(got)
	}
	if NewestKernel(nil) != "" {
		t.Fatal("empty")
	}
}
