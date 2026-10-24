package ceph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Daemon folders of the Debian packages.
const varLib = "/var/lib/ceph"

var daemonNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func validDaemon(name string) error {
	if !daemonNameRe.MatchString(name) {
		return fmt.Errorf("invalid Ceph daemon name %q", name)
	}
	return nil
}

func chownCeph(paths ...string) error {
	for _, p := range paths {
		if _, err := sys.Run("chown", "-R", "ceph:ceph", p); err != nil {
			return err
		}
	}
	return nil
}

func systemctl(args ...string) error {
	_, err := sys.Run("systemctl", args...)
	return err
}

// WriteBase installs the configuration and the keys every node has: the
// cluster configuration, the admin key and the OSD bootstrap key.
func WriteBase(conf, adminKeyring, bootstrapKeyring string) error {
	if err := os.MkdirAll("/etc/ceph", 0o755); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(ConfPath, []byte(conf), 0o644); err != nil {
		return err
	}
	if err := writeSecret(AdminKeyring, adminKeyring); err != nil {
		return err
	}
	if bootstrapKeyring != "" {
		if err := os.MkdirAll(filepath.Dir(BootstrapOSD), 0o750); err != nil {
			return err
		}
		if err := writeSecret(BootstrapOSD, bootstrapKeyring); err != nil {
			return err
		}
		if err := chownCeph(filepath.Dir(BootstrapOSD)); err != nil {
			return err
		}
	}
	_ = systemctl("enable", "ceph.target")
	return nil
}

// WriteConf replaces ceph.conf only (monitor list changes).
func WriteConf(conf string) error {
	return sys.WriteFileAtomic(ConfPath, []byte(conf), 0o644)
}

// MonMap builds the initial monitor map of a new cluster.
func MonMap(ctx context.Context, fsid string, mons []Mon) ([]byte, error) {
	dir, err := os.MkdirTemp("", "strata-monmap")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "monmap")
	args := []string{"--create", "--clobber", "--fsid", fsid}
	for _, m := range mons {
		if err := validDaemon(m.Name); err != nil {
			return nil, err
		}
		args = append(args, "--addv", m.Name, m.AddrVec())
	}
	args = append(args, path)
	if _, err := sys.RunCtx(ctx, "monmaptool", args...); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// CreateMon creates and starts the monitor of this node from a monitor map
// and the monitor keyring (for a new cluster, or one taken from it to join).
func CreateMon(ctx context.Context, name, addr string, monmap []byte, monKeyring string) error {
	if err := validDaemon(name); err != nil {
		return err
	}
	dir := filepath.Join(varLib, "mon", "ceph-"+name)
	if _, err := os.Stat(filepath.Join(dir, "store.db")); err == nil {
		return fmt.Errorf("a monitor already exists on this NAS")
	}
	tmp, err := os.MkdirTemp(filepath.Join(varLib, "tmp"), "strata-mon")
	if err != nil {
		if err := os.MkdirAll(filepath.Join(varLib, "tmp"), 0o750); err != nil {
			return err
		}
		if tmp, err = os.MkdirTemp(filepath.Join(varLib, "tmp"), "strata-mon"); err != nil {
			return err
		}
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "monmap"), monmap, 0o600); err != nil {
		return err
	}
	if err := writeSecret(filepath.Join(tmp, "keyring"), monKeyring); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := chownCeph(tmp, dir); err != nil {
		return err
	}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 5 * time.Minute}, "ceph-mon", "--mkfs", "-i", name, "--monmap", filepath.Join(tmp, "monmap"), "--keyring", filepath.Join(tmp, "keyring"), "--public-addr", addr, "--setuser", "ceph", "--setgroup", "ceph"); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	_ = systemctl("enable", "ceph-mon.target")
	return systemctl("enable", "--now", "ceph-mon@"+name+".service")
}

// authDaemon gets (or creates) the key of a daemon and writes its keyring
// into its folder. The key never appears in an error.
func authDaemon(ctx context.Context, kind, name string, caps ...string) (string, error) {
	dir := filepath.Join(varLib, kind, "ceph-"+name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	args := append([]string{"auth", "get-or-create", kind + "." + name}, caps...)
	out, err := Local.Ceph(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("cannot create the key of %s.%s: %w", kind, name, redact(err))
	}
	if KeyOf(out, kind+"."+name) == "" {
		return "", fmt.Errorf("cannot create the key of %s.%s", kind, name)
	}
	if err := writeSecret(filepath.Join(dir, "keyring"), out); err != nil {
		return "", err
	}
	return dir, chownCeph(dir)
}

// redact drops the output of a failed command that may hold a key.
func redact(err error) error {
	if ce, ok := err.(*sys.CmdError); ok {
		return fmt.Errorf("%s", RedactKeys(strings.TrimSpace(ce.Stderr)))
	}
	return redactErr(err)
}

// CreateMgr creates and starts the manager of this node.
func CreateMgr(ctx context.Context, name string) error {
	if err := validDaemon(name); err != nil {
		return err
	}
	if _, err := authDaemon(ctx, "mgr", name, "mon", "allow profile mgr", "osd", "allow *", "mds", "allow *"); err != nil {
		return err
	}
	_ = systemctl("enable", "ceph-mgr.target")
	return systemctl("enable", "--now", "ceph-mgr@"+name+".service")
}

// CreateMDS creates and starts the metadata server of this node.
func CreateMDS(ctx context.Context, name string) error {
	if err := validDaemon(name); err != nil {
		return err
	}
	if _, err := authDaemon(ctx, "mds", name, "mon", "profile mds", "mgr", "profile mds", "mds", "allow *", "osd", "allow *"); err != nil {
		return err
	}
	_ = systemctl("enable", "ceph-mds.target")
	return systemctl("enable", "--now", "ceph-mds@"+name+".service")
}

// RemoveDaemon stops a monitor, manager or metadata server of this node and
// deletes its folder. Monitors must be removed from the map first (by the
// caller), the key of the others is deleted here.
func RemoveDaemon(ctx context.Context, kind, name string) error {
	if err := validDaemon(name); err != nil {
		return err
	}
	if kind != "mon" && kind != "mgr" && kind != "mds" {
		return fmt.Errorf("unknown daemon kind %q", kind)
	}
	unit := "ceph-" + kind + "@" + name + ".service"
	_ = systemctl("disable", "--now", unit)
	if kind != "mon" {
		_, _ = Local.Ceph(ctx, "auth", "del", kind+"."+name)
	}
	return os.RemoveAll(filepath.Join(varLib, kind, "ceph-"+name))
}

// HasDaemon reports whether this node runs a daemon of kind.
func HasDaemon(kind, name string) bool {
	_, err := os.Stat(filepath.Join(varLib, kind, "ceph-"+name))
	return err == nil
}

// DaemonActive reports whether the systemd unit of a daemon is active.
func DaemonActive(kind, name string) bool {
	out, _ := sys.Run("systemctl", "is-active", "ceph-"+kind+"@"+name+".service")
	return strings.TrimSpace(out) == "active"
}

var devPathRe = regexp.MustCompile(`^/dev/[A-Za-z0-9/_.:-]+$`)

// CreateOSD turns a disk into an OSD (BlueStore on LVM, ceph-volume). With
// erase the disk is zapped first; id >= 0 reuses a destroyed OSD's ID
// (replacing a failed disk).
func CreateOSD(ctx context.Context, dev string, erase bool, id int, logf func(string, ...any)) error {
	if !devPathRe.MatchString(dev) {
		return fmt.Errorf("invalid device %q", dev)
	}
	out := sys.Opts{Timeout: 20 * time.Minute, Output: &lineWriter{logf}}
	if erase {
		logf("Erasing %s", dev)
		if _, err := sys.RunOpts(ctx, out, "ceph-volume", "lvm", "zap", "--destroy", dev); err != nil {
			return redactErr(err)
		}
	}
	args := []string{"lvm", "create", "--bluestore", "--data", dev}
	if id >= 0 {
		args = append(args, "--osd-id", strconv.Itoa(id))
	}
	logf("ceph-volume %s", strings.Join(args, " "))
	_, err := sys.RunOpts(ctx, out, "ceph-volume", args...)
	return redactErr(err)
}

// StopOSD stops an OSD of this node and keeps it from starting again.
func StopOSD(id int) error {
	_ = systemctl("disable", "--now", fmt.Sprintf("ceph-osd@%d.service", id))
	return nil
}

// ZapOSD erases the disk(s) of an OSD of this node.
func ZapOSD(ctx context.Context, id int, logf func(string, ...any)) error {
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Minute, Output: &lineWriter{logf}}, "ceph-volume", "lvm", "zap", "--destroy", "--osd-id", strconv.Itoa(id))
	return redactErr(err)
}

// LocalOSDs lists the OSDs of this node and their disks (ceph-volume).
func LocalOSDs(ctx context.Context) (map[int][]string, error) {
	out, err := sys.RunCtx(ctx, "ceph-volume", "lvm", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParseVolumeList([]byte(out))
}

// ParseVolumeList reads `ceph-volume lvm list --format json`.
func ParseVolumeList(data []byte) (map[int][]string, error) {
	var raw map[string][]struct {
		Devices []string `json:"devices"`
		Type    string   `json:"type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph-volume: %w", err)
	}
	out := map[int][]string{}
	for k, lvs := range raw {
		id, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		for _, lv := range lvs {
			if lv.Type == "block" {
				out[id] = append(out[id], lv.Devices...)
			}
		}
	}
	return out, nil
}

// Purge removes every Ceph daemon and key of this node (leaving the
// cluster, or destroying it). OSDs must be gone first.
func Purge() error {
	_ = systemctl("disable", "--now", "ceph.target", "ceph-mon.target", "ceph-mgr.target", "ceph-mds.target", "ceph-osd.target")
	for _, kind := range []string{"mon", "mgr", "mds"} {
		dirs, _ := filepath.Glob(filepath.Join(varLib, kind, "ceph-*"))
		for _, d := range dirs {
			name := strings.TrimPrefix(filepath.Base(d), "ceph-")
			_ = systemctl("disable", "--now", "ceph-"+kind+"@"+name+".service")
			_ = os.RemoveAll(d)
		}
	}
	for _, p := range []string{ConfPath, AdminKeyring, BootstrapOSD, filepath.Join(StrataDir, "local.admin.secret")} {
		_ = os.Remove(p)
	}
	_, _ = sys.Run("systemctl", "reset-failed")
	return nil
}

// lineWriter sends command output to a job log line by line, without the
// keys ceph-volume prints in the commands it runs.
type lineWriter struct{ logf func(string, ...any) }

func (w *lineWriter) Write(b []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			w.logf("%s", RedactKeys(l))
		}
	}
	return len(b), nil
}

var keyInTextRe = regexp.MustCompile(`AQ[A-Za-z0-9+/]{36,}={0,2}`)

// RedactKeys hides cephx keys in a text (logs, error messages).
func RedactKeys(s string) string { return keyInTextRe.ReplaceAllString(s, "[key]") }

// redactErr hides the keys in the error of a command.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", RedactKeys(err.Error()))
}
