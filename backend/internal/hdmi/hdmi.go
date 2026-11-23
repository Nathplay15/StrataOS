// Package hdmi drives the graphical menu StrataOS shows on a TV plugged to
// its HDMI output: detection of the connected display (DRM connectors), the
// kiosk token the local browser uses to sign in read-only, and the runner
// (strata hdmi, strataos-hdmi.service) that keeps a kiosk browser on the
// display while one is connected.
package hdmi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DRMRoot is where the kernel exposes the display connectors (variable so
// tests can point it at a fixture).
var DRMRoot = "/sys/class/drm"

// Display is a connected graphical output.
type Display struct {
	Connector string // e.g. "card1-HDMI-A-1"
	Mode      string // its preferred mode, e.g. "1920x1080"
}

// Connected returns the first connected HDMI or DisplayPort connector.
func Connected() (Display, bool) {
	ents, err := os.ReadDir(DRMRoot)
	if err != nil {
		return Display{}, false
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		// HDMI, DisplayPort (but not an embedded eDP panel): an external screen.
		if strings.Contains(n, "HDMI") || strings.Contains(n, "-DP-") || strings.Contains(n, "DisplayPort") {
			if st, err := os.ReadFile(filepath.Join(DRMRoot, n, "status")); err == nil && strings.TrimSpace(string(st)) == "connected" {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		d := Display{Connector: n}
		if modes, err := os.ReadFile(filepath.Join(DRMRoot, n, "modes")); err == nil {
			if m := strings.Fields(string(modes)); len(m) > 0 {
				d.Mode = m[0]
			}
		}
		return d, true
	}
	return Display{}, false
}

// TokenFile is the kiosk token of the NAS, inside the data directory.
func TokenFile(dataDir string) string { return filepath.Join(dataDir, "hdmi.token") }

// EnsureToken returns the kiosk token, creating it (32 random bytes, hex,
// 0600) when missing. The token authorizes the local kiosk browser only
// (loopback, read-only: see api/h_hdmi.go).
func EnsureToken(dataDir string) (string, error) {
	p := TokenFile(dataDir)
	if b, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// Token reads the kiosk token ("" when absent).
func Token(dataDir string) string {
	b, err := os.ReadFile(TokenFile(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Browser is the first kiosk-capable browser found on the system ("" if none;
// a variable so tests can stub it).
var Browser = func() string {
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome", "firefox"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

// KioskURL is the address the kiosk browser opens: the local web interface,
// /hdmi page, token in the fragment (never sent to the server).
func KioskURL(token string) string {
	return fmt.Sprintf("https://127.0.0.1:7443/hdmi#%s", token)
}
