package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func hide(*exec.Cmd) {}

// Open opens a folder or a link.
func Open(target string) error { return start("open", target) }

// PickFolder asks for a folder ("" when cancelled).
func PickFolder(title string) (string, error) {
	out, err := output("osascript", "-e", `POSIX path of (choose folder with prompt "`+strings.ReplaceAll(title, `"`, "")+`")`)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return "", nil
	}
	return strings.TrimRight(strings.TrimSpace(out), "/"), err
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Notify shows a notification (Notification Center).
func Notify(title, body string) error {
	return start("osascript", "-e", "display notification "+quote(body)+" with title "+quote(title))
}

func agentFile() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", "io.github.nathplay15.strataos-sync.plist")
}

// SetAutostart starts the app at login (a LaunchAgent) or stops doing so.
func SetAutostart(on bool) error {
	f := agentFile()
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
	exe := strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(Executable())
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.nathplay15.strataos-sync</string>
  <key>ProgramArguments</key><array><string>` + exe + `</string><string>--background</string></array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`
	return os.WriteFile(f, []byte(plist), 0o644)
}

// Autostart tells whether the app starts at login.
func Autostart() bool {
	_, err := os.Stat(agentFile())
	return err == nil
}

// OSName names the system ("macOS 15.1").
func OSName() string {
	out, err := output("sw_vers", "-productVersion")
	if err != nil {
		return "macOS"
	}
	return "macOS " + strings.TrimSpace(out)
}
