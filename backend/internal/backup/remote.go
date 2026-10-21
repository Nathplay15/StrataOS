package backup

// Helpers shared by the tasks that talk to another NAS: host keys, temporary
// credential files, and the snapshots that keep versions of what was copied.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/sys"
)

// SecretDir holds credential files while a task runs (root only, on tmpfs).
var SecretDir = "/run/strata/backup"

var (
	bwRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[KMG]?$`)
	// Sub-volumes whose path can name a snapshot (otherwise the volume is snapshotted).
	subNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-/]{0,90}$`)
)

// KnownHost records the server's SSH keys the first time it is used (like
// StrictHostKeyChecking=accept-new) and returns the known_hosts file, for Go
// SSH clients (rclone). OpenSSH records only the key type it negotiated, while
// Go's client prefers ECDSA/RSA and reports a "key mismatch" when the type it
// gets is not listed: the other key types the server offers are added, once
// a key already known is confirmed unchanged.
// scanned remembers when each host was last scanned for its keys.
var (
	scannedMu sync.Mutex
	scanned   = map[string]time.Time{}
)

func KnownHost(ctx context.Context, host, port string) (string, error) {
	kh := knownHostsPath()
	if port == "" || port == "0" {
		port = "22"
	}
	name := host
	if port != "22" {
		name = "[" + host + "]:" + port
	}
	if strings.HasPrefix(host, "-") {
		return "", fmt.Errorf("invalid host")
	}
	known, _ := sys.Run("ssh-keygen", "-F", name, "-f", kh)
	// A scan is a connection that closes without signing in: OpenSSH 9.8+
	// penalises those (PerSourcePenalties) and soon refuses the NAS. Once
	// the host is known, look again at most once a day for missing key types.
	scannedMu.Lock()
	last := scanned[name]
	scannedMu.Unlock()
	if strings.TrimSpace(known) != "" && time.Since(last) < 24*time.Hour {
		return kh, nil
	}
	keys, err := sys.RunOpts(ctx, sys.Opts{Timeout: 20 * time.Second}, "ssh-keyscan", "-p", port, "-T", "10", host)
	if err != nil || strings.TrimSpace(keys) == "" {
		if strings.TrimSpace(known) != "" {
			return kh, nil // let the client report why it cannot connect
		}
		return "", fmt.Errorf("cannot reach the SSH server %s:%s", host, port)
	}
	add, err := newHostKeys(known, keys)
	if err != nil {
		return "", fmt.Errorf("%s:%s: %w", host, port, err)
	}
	scannedMu.Lock()
	scanned[name] = time.Now()
	scannedMu.Unlock()
	if add == "" {
		return kh, nil
	}
	if err := os.MkdirAll(KeyDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(add)
	return kh, err
}

// newHostKeys returns the ssh-keyscan lines to append to known_hosts given
// the lines already known for the host (ssh-keygen -F output). With nothing
// known, all scanned keys; otherwise the key types not known yet, provided a
// known key is offered unchanged and none of its type differs.
func newHostKeys(known, scanned string) (string, error) {
	type key struct{ typ, blob string }
	parse := func(lines string) []key {
		var out []key
		for _, l := range strings.Split(lines, "\n") {
			f := strings.Fields(l)
			if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") {
				continue
			}
			out = append(out, key{f[1], f[2]})
		}
		return out
	}
	have := parse(known)
	var lines []string
	for _, l := range strings.Split(scanned, "\n") {
		if f := strings.Fields(l); len(f) >= 3 && !strings.HasPrefix(f[0], "#") {
			lines = append(lines, l)
		}
	}
	if len(have) == 0 {
		if len(lines) == 0 {
			return "", nil
		}
		return strings.Join(lines, "\n") + "\n", nil
	}
	knownTypes := map[string]bool{}
	for _, k := range have {
		knownTypes[k.typ] = true
	}
	confirmed := false
	var add []string
	for _, l := range lines {
		f := strings.Fields(l)
		k := key{f[1], f[2]}
		if !knownTypes[k.typ] {
			add = append(add, l)
			continue
		}
		match := false
		for _, h := range have {
			if h == k {
				match = true
			}
		}
		if !match {
			return "", fmt.Errorf("the SSH host key changed (possible interception): remove the old key from %s if the server was reinstalled", knownHostsPath())
		}
		confirmed = true
	}
	if !confirmed || len(add) == 0 {
		return "", nil
	}
	return strings.Join(add, "\n") + "\n", nil
}

// HostKeyAlgorithms lists, space separated, the host key algorithms matching
// the keys known_hosts holds for host:port ("" when none). Go's SSH client
// (rclone) prefers ECDSA and reports a "key mismatch" when only another key
// type is known, as when OpenSSH recorded just the ed25519 key it negotiated:
// restricting it to the known types avoids that.
func HostKeyAlgorithms(host, port string) string {
	name := host
	if port != "" && port != "0" && port != "22" {
		name = "[" + host + "]:" + port
	}
	out, _ := sys.Run("ssh-keygen", "-F", name, "-f", knownHostsPath())
	return strings.Join(keyAlgos(out), " ")
}

// keyAlgos maps the key types of known_hosts lines to signature algorithms.
func keyAlgos(lines string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	for _, l := range strings.Split(lines, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || f[0] == "@revoked" || f[0] == "@cert-authority" {
			continue
		}
		switch typ := f[1]; {
		case typ == "ssh-rsa":
			add("rsa-sha2-512", "rsa-sha2-256", "ssh-rsa")
		case typ == "ssh-ed25519" || strings.HasPrefix(typ, "ecdsa-sha2-") || strings.HasPrefix(typ, "sk-"):
			add(typ)
		}
	}
	return out
}

// secretFile writes content to a new root-only file and returns its path and
// a function removing it. Credentials go through such files, never argv.
func secretFile(content string) (string, func(), error) {
	if err := os.MkdirAll(SecretDir, 0o700); err != nil {
		return "", nil, err
	}
	_ = os.Chmod(SecretDir, 0o700)
	f, err := os.CreateTemp(SecretDir, "cred-")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

// mountInfo returns the mount point, file system type and source (the
// dataset name on ZFS) holding p.
func mountInfo(p string) (target, fstype, source string) {
	out, err := sys.Run("findmnt", "-n", "-o", "TARGET,FSTYPE,SOURCE", "-T", p)
	if err != nil {
		return "", "", ""
	}
	f := strings.Fields(strings.TrimSpace(strings.Split(out, "\n")[0]))
	if len(f) < 3 {
		return "", "", ""
	}
	unesc := func(s string) string { return strings.ReplaceAll(s, `\x20`, " ") }
	return unesc(f[0]), f[1], unesc(f[2])
}

// isSubvolume reports whether dir is the root of a Btrfs sub-volume (inode 256).
func isSubvolume(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Ino == 256
}

// prepareVersionedDir creates a missing destination folder as its own Btrfs
// sub-volume or ZFS dataset, so that its snapshots hold only the backup.
func prepareVersionedDir(dir string, p Progress) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	target, fstype, source := mountInfo(parent)
	switch {
	case fstype == "btrfs" && strings.HasPrefix(target, storage.MountRoot+"/"):
		if _, err := sys.Run("btrfs", "subvolume", "create", dir); err == nil {
			p.Log("Created the Btrfs sub-volume %s", dir)
			return nil
		}
	case fstype == "zfs" && target == parent:
		ds := source + "/" + filepath.Base(dir)
		if storage.ValidDS(ds) == nil {
			if _, err := sys.Run("zfs", "create", ds); err == nil {
				p.Log("Created the ZFS dataset %s", ds)
				return nil
			}
		}
	}
	return os.MkdirAll(dir, 0o755)
}

// snapshotFolder takes a read-only snapshot of the Btrfs sub-volume (or the
// whole volume) or ZFS dataset holding dir, with retention on the snapshots
// of tag. On other file systems it does nothing and says so.
func snapshotFolder(dir, tag string, keepLast, keepDays int, p Progress) (string, error) {
	target, fstype, source := mountInfo(dir)
	if !strings.HasPrefix(target, storage.MountRoot+"/") {
		return "", nil
	}
	switch fstype {
	case "btrfs":
		sub := ""
		if rel, err := filepath.Rel(target, dir); err == nil && dir != target && isSubvolume(dir) && subNameRe.MatchString(rel) {
			sub = rel
		}
		return scheduler.SnapshotBtrfs(target, sub, tag, keepLast, keepDays, 0, p.Log)
	case "zfs":
		if storage.ValidDS(source) != nil {
			return "", fmt.Errorf("cannot find the ZFS dataset of %s", dir)
		}
		return scheduler.SnapshotZFS(source, tag, false, keepLast, keepDays, 0, p.Log)
	}
	p.Log("No snapshot: %s is on %s (versions need Btrfs or ZFS)", dir, fstype)
	return "", nil
}

func portOr(port, def int) string {
	if port == 0 {
		port = def
	}
	return strconv.Itoa(port)
}

// sshHost puts an IPv6 address between brackets for rsync and rclone host specs.
func sshHost(h string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]"
	}
	return h
}
