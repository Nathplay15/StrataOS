package hdmi

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run keeps a kiosk browser on the TV plugged to the HDMI output
// (strataos-hdmi.service → "strata hdmi"): it polls the DRM connectors and,
// while one is connected, runs a private Xorg and the first installed
// browser in kiosk mode on the local /hdmi page. The browser runs as the
// unprivileged KioskUser with a private profile; the token reaches it
// through a start page readable by that account only. When the display goes
// away, or the service stops, the browser and Xorg are stopped. Best effort:
// without a display or a browser it just waits and logs.
func Run(dataDir string) error {
	tok, err := EnsureToken(dataDir)
	if err != nil {
		return fmt.Errorf("kiosk token: %w", err)
	}
	k := &kiosk{token: tok}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	for {
		d, ok := Connected()
		if ok && k.browser == nil {
			k.start(d)
		}
		if !ok && (k.browser != nil || k.xorg != nil) {
			slog.Info("hdmi: display disconnected, stopping the kiosk")
			k.stop()
		}
		select {
		case <-sig:
			k.stop()
			return nil
		case <-k.exited:
			k.browser = nil
		case <-time.After(5 * time.Second):
		}
	}
}

type kiosk struct {
	token   string
	browser *exec.Cmd
	exited  chan struct{}
	xorg    *exec.Cmd
	xorgEnd chan struct{}
	display string
	xauth   string
}

func (k *kiosk) start(d Display) {
	browser := Browser()
	if browser == "" {
		slog.Warn("hdmi: display connected but no kiosk browser installed (chromium or firefox)")
		return
	}
	u, err := user.Lookup(KioskUser)
	if err != nil {
		slog.Warn("hdmi: no kiosk account", "user", KioskUser, "err", err)
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := k.ensureDisplayServer(d, uid, gid); err != nil {
		slog.Warn("hdmi: no display server", "err", err)
		return
	}
	home, page, err := prepareHome(u.HomeDir, uid, gid, k.token)
	if err != nil {
		slog.Warn("hdmi: kiosk profile", "err", err)
		return
	}
	cmd := kioskCmd(browser, "file://"+page, filepath.Join(home, "profile"))
	cmd.Env = []string{"HOME=" + home, "USER=" + KioskUser, "DISPLAY=" + k.display, "XAUTHORITY=" + filepath.Join(home, ".Xauthority"),
		"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=" + os.Getenv("LANG")}
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: kioskGroups()}, Setsid: true}
	if err := cmd.Start(); err != nil {
		slog.Warn("hdmi: cannot start the kiosk browser", "browser", browser, "err", err)
		return
	}
	slog.Info("hdmi: kiosk on the TV", "connector", d.Connector, "mode", d.Mode, "browser", browser, "display", k.display)
	k.browser = cmd
	k.exited = make(chan struct{}, 1)
	go func(c *exec.Cmd, ch chan struct{}) { _ = c.Wait(); ch <- struct{}{} }(cmd, k.exited)
}

// stop ends the browser (its whole process group) and the Xorg started for it.
func (k *kiosk) stop() {
	if k.browser != nil {
		_ = syscall.Kill(-k.browser.Process.Pid, syscall.SIGTERM)
		select {
		case <-k.exited:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-k.browser.Process.Pid, syscall.SIGKILL)
			<-k.exited
		}
		k.browser = nil
	}
	if k.xorg != nil {
		_ = k.xorg.Process.Signal(syscall.SIGTERM)
		select {
		case <-k.xorgEnd:
		case <-time.After(5 * time.Second):
			_ = k.xorg.Process.Kill()
			<-k.xorgEnd
		}
		k.xorg = nil
	}
}

// kioskGroups: video and audio, so the browser can decode and play sound.
func kioskGroups() []uint32 {
	var out []uint32
	for _, g := range []string{"video", "audio", "render"} {
		if gr, err := user.LookupGroup(g); err == nil {
			if id, err := strconv.Atoi(gr.Gid); err == nil {
				out = append(out, uint32(id))
			}
		}
	}
	return out
}

// prepareHome makes the kiosk account's private folder (0700) and writes its
// start page (0600), both owned by that account.
func prepareHome(home string, uid, gid int, token string) (string, string, error) {
	if home == "" || home == "/" || home == "/nonexistent" {
		home = "/var/lib/" + KioskUser
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", "", err
	}
	st, err := os.Lstat(home)
	if err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("%s is not a folder", home)
	}
	if err := os.Lchown(home, uid, gid); err != nil {
		return "", "", err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return "", "", err
	}
	profile := filepath.Join(home, "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Lchown(profile, uid, gid); err != nil {
		return "", "", err
	}
	page := filepath.Join(home, "start.html")
	if err := writePrivate(page, StartPage(token), uid, gid); err != nil {
		return "", "", err
	}
	return home, page, nil
}

// writePrivate replaces path by a new 0600 file owned by uid:gid (never
// through an existing symlink).
func writePrivate(path string, data []byte, uid, gid int) error {
	tmp := path + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chown(uid, gid)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	return os.Rename(tmp, path)
}

// kioskCmd builds the browser command line for the kiosk page: no secret in it.
func kioskCmd(browser, url, profile string) *exec.Cmd {
	if strings.Contains(filepath.Base(browser), "firefox") {
		return exec.Command(browser, "--kiosk", "--new-instance", "--profile", profile, url)
	}
	return exec.Command(browser,
		"--kiosk", "--no-first-run", "--no-default-browser-check", "--disable-infobars",
		"--ignore-certificate-errors", "--autoplay-policy=no-user-gesture-required",
		"--allow-file-access-from-files", "--user-data-dir="+profile, url)
}

// xauthEntry is one Xauthority record (MIT-MAGIC-COOKIE-1) for display num.
func xauthEntry(family uint16, addr, num string, cookie []byte) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, family)
	for _, f := range [][]byte{[]byte(addr), []byte(num), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		b = binary.BigEndian.AppendUint16(b, uint16(len(f)))
		b = append(b, f...)
	}
	return b
}

// XAuthority returns an Xauthority file granting display num with cookie
// (any host: FamilyWild).
func XAuthority(num string, cookie []byte) []byte {
	return xauthEntry(0xFFFF, "", num, cookie)
}

// ensureDisplayServer starts, once, a private Xorg for the TV that only
// clients holding its cookie may use (no TCP, no abstract socket), and
// gives the cookie to the kiosk account.
func (k *kiosk) ensureDisplayServer(d Display, uid, gid int) error {
	if k.xorg != nil {
		return nil // still running: never a second one
	}
	xorg, err := exec.LookPath("Xorg")
	if err != nil {
		return fmt.Errorf("no Xorg: install xserver-xorg")
	}
	dir := "/run/strata-hdmi"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
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
		return err
	}
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	auth := filepath.Join(dir, "xauth")
	if err := writePrivate(auth, XAuthority("61", cookie), 0, 0); err != nil {
		return err
	}
	u, _ := user.LookupId(strconv.Itoa(uid))
	home := "/var/lib/" + KioskUser
	if u != nil && u.HomeDir != "" && u.HomeDir != "/nonexistent" {
		home = u.HomeDir
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(home, ".Xauthority"), XAuthority("61", cookie), uid, gid); err != nil {
		return err
	}
	c := exec.Command(xorg, ":61", "-config", conf, "-auth", auth, "-noreset", "-nolisten", "tcp", "-nolisten", "local", "vt7")
	c.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	if err := c.Start(); err != nil {
		return err
	}
	k.xorg, k.xorgEnd, k.display = c, make(chan struct{}), ":61"
	go func(c *exec.Cmd, end chan struct{}) { _ = c.Wait(); close(end) }(c, k.xorgEnd)
	for i := 0; i < 20; i++ {
		if _, err := os.Stat("/tmp/.X11-unix/X61"); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}
