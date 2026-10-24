package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

func hide(*exec.Cmd) {}

// Open opens a folder or a link with the desktop's default application.
func Open(target string) error { return start("xdg-open", target) }

// PickFolder asks for a folder (zenity or kdialog; "" when cancelled).
func PickFolder(title string) (string, error) {
	if _, err := exec.LookPath("zenity"); err == nil {
		out, err := output("zenity", "--file-selection", "--directory", "--title="+title)
		return strings.TrimSpace(out), ignoreCancel(err)
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		out, err := output("kdialog", "--getexistingdirectory", os.Getenv("HOME"), "--title", title)
		return strings.TrimSpace(out), ignoreCancel(err)
	}
	return "", errors.New("no folder chooser (install zenity): type the path")
}

func ignoreCancel(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}

// Notify shows a desktop notification (freedesktop.org notifications).
func Notify(title, body string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.Notify", 0, "StrataOS Sync", uint32(0), "folder-sync", title, body,
		[]string{}, map[string]dbus.Variant{}, int32(-1)).Err
}

func autostartFile() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "autostart", "strataos-sync.desktop")
}

// SetAutostart starts the app at login (XDG autostart) or stops doing so.
func SetAutostart(on bool) error {
	f := autostartFile()
	if !on {
		err := os.Remove(f)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	exe := Executable()
	content := "[Desktop Entry]\nType=Application\nName=StrataOS Sync\nComment=Synchronizes folders with the NAS\nExec=\"" + exe + "\" --background\nIcon=strataos-sync\nX-GNOME-Autostart-enabled=true\nTerminal=false\n"
	return os.WriteFile(f, []byte(content), 0o644)
}

// Autostart tells whether the app starts at login.
func Autostart() bool {
	_, err := os.Stat(autostartFile())
	return err == nil
}

// OSName names the system ("Ubuntu 24.04.1 LTS").
func OSName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	return "Linux"
}
