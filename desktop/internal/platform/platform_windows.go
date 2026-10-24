package platform

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// Open opens a folder or a link.
func Open(target string) error {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return start("rundll32", "url.dll,FileProtocolHandler", target)
	}
	return start("explorer", target)
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// PickFolder asks for a folder ("" when cancelled).
func PickFolder(title string) (string, error) {
	script := "Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description = " + psQuote(title) +
		"; $d.ShowNewFolderButton = $true; if ($d.ShowDialog() -eq 'OK') { [Console]::OutputEncoding = [Text.Encoding]::UTF8; Write-Output $d.SelectedPath }"
	out, err := output("powershell", "-NoProfile", "-STA", "-Command", script)
	return strings.TrimSpace(out), err
}

// Notify shows a toast notification.
func Notify(title, body string) error {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;").Replace(s)
	}
	xml := "<toast><visual><binding template='ToastGeneric'><text>" + esc(title) + "</text><text>" + esc(body) + "</text></binding></visual></toast>"
	script := "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;" +
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null;" +
		"$x = New-Object Windows.Data.Xml.Dom.XmlDocument; $x.LoadXml(" + psQuote(xml) + ");" +
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('StrataOS Sync').Show([Windows.UI.Notifications.ToastNotification]::new($x))"
	return start("powershell", "-NoProfile", "-Command", script)
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart starts the app at login (the Run key of the user) or stops doing so.
func SetAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("StrataOS Sync"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue("StrataOS Sync", `"`+Executable()+`" --background`)
}

// Autostart tells whether the app starts at login.
func Autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("StrataOS Sync")
	return err == nil
}

// OSName names the system ("Windows 11 Pro 23H2").
func OSName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()
	name, _, _ := k.GetStringValue("ProductName")
	build, _, _ := k.GetStringValue("CurrentBuildNumber")
	disp, _, _ := k.GetStringValue("DisplayVersion")
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1) // ProductName still says 10
	}
	if name == "" {
		name = "Windows"
	}
	if disp != "" {
		name += " " + disp
	}
	return name
}
