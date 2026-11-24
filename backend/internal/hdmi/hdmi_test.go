package hdmi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnected(t *testing.T) {
	root := t.TempDir()
	mk := func(name, status, modes string) {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "status"), []byte(status), 0o644)
		if modes != "" {
			os.WriteFile(filepath.Join(d, "modes"), []byte(modes), 0o644)
		}
	}
	old := DRMRoot
	DRMRoot = root
	t.Cleanup(func() { DRMRoot = old })

	if _, ok := Connected(); ok {
		t.Error("empty DRM root: should be no display")
	}
	mk("card1-HDMI-A-1", "disconnected\n", "")
	mk("card1-eDP-1", "connected\n", "1366x768\n") // internal panel: not a TV
	if _, ok := Connected(); ok {
		t.Error("only eDP connected: should be no external display")
	}
	mk("card1-HDMI-A-1", "connected\n", "3840x2160\n1920x1080\n")
	d, ok := Connected()
	if !ok || d.Connector != "card1-HDMI-A-1" || d.Mode != "3840x2160" {
		t.Errorf("HDMI: %+v %v", d, ok)
	}
	// A DisplayPort screen works too.
	mk("card1-DP-1", "connected\n", "2560x1440\n")
	d, ok = Connected()
	if !ok || !strings.Contains(d.Connector, "DP") || d.Mode != "2560x1440" {
		t.Errorf("DP: %+v %v", d, ok)
	}
}

func TestToken(t *testing.T) {
	dir := t.TempDir()
	if Token(dir) != "" {
		t.Error("no token file yet: should be empty")
	}
	tok, err := EnsureToken(dir)
	if err != nil || len(tok) != 64 {
		t.Fatalf("EnsureToken: %q %v", tok, err)
	}
	// Stable, and the file is private.
	tok2, _ := EnsureToken(dir)
	if tok2 != tok || Token(dir) != tok {
		t.Errorf("token not stable: %q vs %q", tok, tok2)
	}
	fi, _ := os.Stat(TokenFile(dir))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode: %o", fi.Mode().Perm())
	}
	if u := KioskURL(tok); !strings.Contains(u, "/hdmi#"+tok) || !strings.HasPrefix(u, "https://127.0.0.1:7443") {
		t.Errorf("KioskURL: %q", u)
	}
}

func TestKioskCommandHasNoToken(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	page := StartPage(tok)
	if !strings.Contains(string(page), "/hdmi#"+tok) {
		t.Fatalf("start page: %s", page)
	}
	for _, b := range []string{"/usr/bin/chromium", "/usr/bin/firefox"} {
		c := kioskCmd(b, "file:///var/lib/strata-kiosk/start.html", "/var/lib/strata-kiosk/profile")
		if strings.Contains(strings.Join(c.Args, " "), tok) || strings.Contains(strings.Join(c.Args, " "), "no-sandbox") {
			t.Errorf("%s: %v", b, c.Args)
		}
	}
}

func TestPrepareHomeIsPrivate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "kiosk")
	h, page, err := prepareHome(home, os.Getuid(), os.Getgid(), "tok")
	if err != nil || h != home {
		t.Fatal(h, err)
	}
	for p, want := range map[string]os.FileMode{home: 0o700, page: 0o600} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v", p, st.Mode(), err)
		}
	}
	// A planted link where the start page goes is replaced, not followed.
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Remove(page)
	os.Symlink(victim, page)
	if _, _, err := prepareHome(home, os.Getuid(), os.Getgid(), "tok"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("victim written: %q", b)
	}
}

func TestXAuthority(t *testing.T) {
	b := XAuthority("61", []byte("0123456789abcdef"))
	// family 0xffff, empty address, "61", name, 16-byte cookie.
	if len(b) != 2+2+0+2+2+2+18+2+16 || b[0] != 0xff || b[1] != 0xff || string(b[6:8]) != "61" {
		t.Fatalf("xauth: %x", b)
	}
}
