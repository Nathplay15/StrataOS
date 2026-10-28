package lxc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Each container runs in strata-lxc@<name>.service (lxc-start in the
// foreground): its own cgroup, delegated to lxc, outside strata.service, so
// that an update or a restart of StrataOS leaves the containers running. A
// drop-in per container makes the unit wait for (and stop before) the volume
// holding its root file system.

const unitDir = "/etc/systemd/system"

func unitName(name string) string { return "strata-lxc@" + name + ".service" }

func unitText() string {
	return fmt.Sprintf(`[Unit]
Description=StrataOS container %%i
After=network-online.target lxc-net.service lxcfs.service zfs-mount.service
Wants=lxc-net.service

[Service]
Type=simple
KillMode=mixed
TimeoutStartSec=120
TimeoutStopSec=150
Delegate=yes
ExecStart=/usr/bin/lxc-start -F -P %[1]s -n %%i
ExecStop=/usr/bin/lxc-stop -P %[1]s -n %%i -t 120
`, LXCPath)
}

// EnsureUnit installs the template unit.
func EnsureUnit() error {
	p := filepath.Join(unitDir, "strata-lxc@.service")
	if b, err := os.ReadFile(p); err == nil && string(b) == unitText() {
		return nil
	}
	if err := sys.WriteFileAtomic(p, []byte(unitText()), 0o644); err != nil {
		return err
	}
	_, err := sys.Run("systemctl", "daemon-reload")
	return err
}

func dropInDir(name string) string { return filepath.Join(unitDir, unitName(name)+".d") }

// systemdQuote escapes a path for a unit file setting.
func systemdQuote(p string) string {
	return strings.NewReplacer(`\`, `\\`, " ", `\x20`, "%", "%%").Replace(p)
}

// writeDropIn ties the container's unit to its storage.
func writeDropIn(s *Spec) error {
	text := "[Unit]\nRequiresMountsFor=" + systemdQuote(s.Storage.Rootfs()) + "\n"
	for _, m := range s.Mounts {
		text += "RequiresMountsFor=" + systemdQuote(m.Source) + "\n"
	}
	if err := os.MkdirAll(dropInDir(s.Name), 0o755); err != nil {
		return err
	}
	p := filepath.Join(dropInDir(s.Name), "strata.conf")
	if b, err := os.ReadFile(p); err == nil && string(b) == text {
		return nil
	}
	if err := sys.WriteFileAtomic(p, []byte(text), 0o644); err != nil {
		return err
	}
	_, err := sys.Run("systemctl", "daemon-reload")
	return err
}

func removeDropIn(name string) {
	_ = os.RemoveAll(dropInDir(name))
	_, _ = sys.Run("systemctl", "reset-failed", unitName(name))
	_, _ = sys.Run("systemctl", "daemon-reload")
}

// mountUnit returns the systemd name of the mount of a root image.
func mountUnit(st *StorageSpec) (string, error) {
	out, err := sys.Run("systemd-escape", "--path", "--suffix=mount", st.Rootfs())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func writeMountUnit(st *StorageSpec) error {
	u, err := mountUnit(st)
	if err != nil {
		return err
	}
	text := fmt.Sprintf(`[Unit]
Description=Root file system of a StrataOS container
RequiresMountsFor=%s

[Mount]
What=%s
Where=%s
Type=ext4
Options=loop,noatime
`, systemdQuote(imgPath(st)), systemdQuote(imgPath(st)), systemdQuote(st.Rootfs()))
	if err := sys.WriteFileAtomic(filepath.Join(unitDir, u), []byte(text), 0o644); err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "daemon-reload")
	return err
}

func startMount(st *StorageSpec) error {
	u, err := mountUnit(st)
	if err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "start", u)
	return err
}

func stopMount(st *StorageSpec) error {
	if !isMounted(st.Rootfs()) {
		return nil
	}
	u, err := mountUnit(st)
	if err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "stop", u)
	if err == nil {
		for i := 0; i < 20 && isMounted(st.Rootfs()); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if isMounted(st.Rootfs()) {
			return fmt.Errorf("%s is still mounted", st.Rootfs())
		}
	}
	return err
}

func removeMountUnit(st *StorageSpec) {
	if u, err := mountUnit(st); err == nil {
		_ = os.Remove(filepath.Join(unitDir, u))
		_, _ = sys.Run("systemctl", "daemon-reload")
	}
}

// unitLog returns the last lines the unit wrote (why a start failed).
func unitLog(name string) string {
	out, _ := sys.Run("journalctl", "-u", unitName(name), "-n", "15", "--no-pager", "-o", "cat", "--since", "-5min")
	return strings.TrimSpace(out)
}
