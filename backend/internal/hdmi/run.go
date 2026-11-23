package hdmi

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run keeps a kiosk browser on the TV plugged to the HDMI output
// (strataos-hdmi.service → "strata hdmi"): it polls the DRM connectors and,
// while one is connected, runs the first installed browser in kiosk mode on
// the local /hdmi page (signed in read-only with the kiosk token). When the
// display goes away the browser is stopped. Best effort: without a display or
// a browser it just waits and logs.
func Run(dataDir string) error {
	tok, err := EnsureToken(dataDir)
	if err != nil {
		return fmt.Errorf("kiosk token: %w", err)
	}
	url := KioskURL(tok)
	var cmd *exec.Cmd
	exited := make(chan struct{}, 1)
	for {
		d, ok := Connected()
		if ok && cmd == nil {
			browser := Browser()
			switch {
			case browser == "":
				slog.Warn("hdmi: display connected but no kiosk browser installed (chromium or firefox)")
			default:
				display, err := ensureDisplayServer(d)
				if err != nil {
					slog.Warn("hdmi: no display server", "err", err)
				} else {
					cmd = kioskCmd(browser, url, display)
					if err := cmd.Start(); err != nil {
						slog.Warn("hdmi: cannot start the kiosk browser", "browser", browser, "err", err)
						cmd = nil
					} else {
						slog.Info("hdmi: kiosk on the TV", "connector", d.Connector, "mode", d.Mode, "browser", browser, "display", display)
						go func(c *exec.Cmd) { _ = c.Wait(); exited <- struct{}{} }(cmd)
					}
				}
			}
		}
		if !ok && cmd != nil {
			slog.Info("hdmi: display disconnected, stopping the kiosk")
			_ = cmd.Process.Kill()
			<-exited
			cmd = nil
		}
		select {
		case <-exited:
			cmd = nil
		case <-time.After(5 * time.Second):
		}
	}
}

// kioskCmd builds the browser command line for the kiosk page.
func kioskCmd(browser, url, display string) *exec.Cmd {
	env := append(os.Environ(), "DISPLAY="+display)
	profile := filepath.Join(os.TempDir(), "strata-hdmi-profile")
	_ = os.MkdirAll(profile, 0o700)
	var c *exec.Cmd
	if strings.Contains(filepath.Base(browser), "firefox") {
		c = exec.Command(browser, "--kiosk", "--new-instance", "--profile", profile, url)
	} else {
		c = exec.Command(browser,
			"--kiosk", "--no-first-run", "--no-default-browser-check", "--disable-infobars",
			"--ignore-certificate-errors", "--autoplay-policy=no-user-gesture-required",
			"--user-data-dir="+profile, url)
	}
	c.Env = env
	return c
}

// ensureDisplayServer returns the X display to use: the one of the session
// when there is one, else a private Xorg started for the TV (best effort; the
// modesetting driver picks the connected output by itself).
func ensureDisplayServer(d Display) (string, error) {
	if v := os.Getenv("DISPLAY"); v != "" {
		return v, nil
	}
	xorg, err := exec.LookPath("Xorg")
	if err != nil {
		return "", fmt.Errorf("no DISPLAY and no Xorg: install a graphical session or Xorg")
	}
	dir := "/run/strata"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	mode := d.Mode
	if mode == "" {
		mode = "1920x1080"
	}
	conf := filepath.Join(dir, "xorg-hdmi.conf")
	content := fmt.Sprintf(`Section "Device"
  Identifier "card"
  Driver "modesetting"
EndSection
Section "Monitor"
  Identifier "tv"
EndSection
Section "Screen"
  Identifier "screen"
  Device "card"
  Monitor "tv"
  DefaultDepth 24
  SubSection "Display"
    Depth 24
    Modes "%s"
  EndSubSection
EndSection
`, mode)
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		return "", err
	}
	c := exec.Command(xorg, ":61", "-config", conf, "-noreset", "-nolisten", "tcp", "vt7")
	c.Env = os.Environ()
	if err := c.Start(); err != nil {
		return "", err
	}
	go func() { _ = c.Wait() }()
	for i := 0; i < 20; i++ {
		if _, err := os.Stat("/tmp/.X11-unix/X61"); err == nil {
			return ":61", nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ":61", nil
}
