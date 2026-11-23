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
