package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Paths of the node's own cluster (the Debian defaults) and of the files
// StrataOS keeps for the clusters it uses as storage.
const (
	ConfPath      = "/etc/ceph/ceph.conf"
	AdminKeyring  = "/etc/ceph/ceph.client.admin.keyring"
	BootstrapOSD  = "/var/lib/ceph/bootstrap-osd/ceph.keyring"
	StrataDir     = "/etc/ceph/strata" // root only: client keys and secrets
	LocalCluster  = "local"
	connectTimout = "15"
)

// Installed reports whether the Ceph component is present.
func Installed() bool {
	return sys.Has("ceph") && sys.Has("ceph-mon") && sys.Has("ceph-osd") && sys.Has("ceph-volume")
}

// ClientInstalled reports whether the client tools are present (enough to
// use an external cluster).
func ClientInstalled() bool { return sys.Has("ceph") && sys.Has("rbd") && sys.Has("mount.ceph") }

// Version is the installed Ceph version ("" when not installed).
func Version() string {
	out, err := sys.Run("ceph", "--version")
	if err != nil {
		return ""
	}
	f := strings.Fields(out)
	if len(f) >= 3 {
		return f[2]
	}
	return strings.TrimSpace(out)
}

// Conn is how the ceph and rbd tools reach a cluster.
type Conn struct {
	Conf    string
	ID      string // cephx user without "client."
	Keyring string
}

// Local is this node's connection to its own cluster (admin key).
var Local = Conn{Conf: ConfPath, ID: "admin", Keyring: AdminKeyring}

// External is the connection to an external cluster saved by StrataOS.
func External(id, user string) Conn {
	return Conn{Conf: filepath.Join(StrataDir, id+".conf"), ID: user, Keyring: filepath.Join(StrataDir, id+".keyring")}
}

// SecretFile is the file holding just the key, for mount.ceph.
func (c Conn) SecretFile() string {
	return strings.TrimSuffix(c.Keyring, ".keyring") + ".secret"
}

func (c Conn) args(a ...string) []string {
	return append([]string{"--conf", c.Conf, "--id", c.ID, "--keyring", c.Keyring}, a...)
}

// ErrUnreachable is returned when the monitors do not answer in time.
var ErrUnreachable = errors.New("the Ceph monitors do not answer (no quorum, or the cluster network is down)")

// Ceph runs the ceph tool against the cluster.
func (c Conn) Ceph(ctx context.Context, args ...string) (string, error) {
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 90 * time.Second}, "ceph", c.args(append([]string{"--connect-timeout", connectTimout}, args...)...)...)
	return out, friendly(err)
}

// JSON runs a ceph command with JSON output into out.
func (c Conn) JSON(ctx context.Context, out any, args ...string) error {
	data, err := c.Ceph(ctx, append(args, "-f", "json")...)
	if err != nil {
		return err
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = []byte(data)
		return nil
	}
	return json.Unmarshal([]byte(data), out)
}

// RBD runs the rbd tool against the cluster.
func (c Conn) RBD(ctx context.Context, args ...string) (string, error) {
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Minute}, "rbd", c.args(args...)...)
	return out, friendly(err)
}

// friendly shortens the usual connection errors of the ceph tools.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timed out"), strings.Contains(msg, "error connecting to the cluster") && strings.Contains(msg, "TimedOut"),
		strings.Contains(msg, "monclient(hunting): authenticate timed out"):
		return ErrUnreachable
	case strings.Contains(msg, "Permission denied") || strings.Contains(msg, "EACCES"), strings.Contains(msg, "handle_auth_bad_method"):
		return fmt.Errorf("the Ceph cluster refused the user or key (check them and their rights)")
	}
	return redactErr(err)
}

// Status reads the cluster status.
func (c Conn) Status(ctx context.Context) (Status, error) {
	var raw []byte
	if err := c.JSON(ctx, &raw, "status"); err != nil {
		return Status{}, err
	}
	return ParseStatus(raw)
}

// FSID asks the cluster its fsid.
func (c Conn) FSID(ctx context.Context) (string, error) {
	out, err := c.Ceph(ctx, "fsid")
	if err != nil {
		return "", err
	}
	f := strings.TrimSpace(out)
	if err := ValidFSID(f); err != nil {
		return "", err
	}
	return f, nil
}

// writeSecret writes a root-only file (keys, keyrings).
func writeSecret(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return sys.WriteFileAtomic(path, []byte(content), 0o600)
}

// SaveExternal writes the configuration, keyring and secret file of an
// external cluster (root only).
func SaveExternal(id, user, key, fsid string, mons []string) (Conn, error) {
	if err := os.MkdirAll(StrataDir, 0o700); err != nil {
		return Conn{}, err
	}
	_ = os.Chmod(StrataDir, 0o700)
	c := External(id, user)
	if err := sys.WriteFileAtomic(c.Conf, []byte(ClientConfig(fsid, mons)), 0o600); err != nil {
		return c, err
	}
	if err := writeSecret(c.Keyring, Keyring(Entity{Name: "client." + user, Key: key})); err != nil {
		return c, err
	}
	return c, writeSecret(c.SecretFile(), key)
}

// RemoveExternal deletes the files of an external cluster.
func RemoveExternal(id string) {
	c := External(id, "x")
	for _, p := range []string{c.Conf, c.Keyring, c.SecretFile()} {
		_ = os.Remove(p)
	}
}

// LocalSecretFile writes (once) the admin key of the node's own cluster as
// a secret file for mount.ceph.
func LocalSecretFile() (string, error) {
	data, err := os.ReadFile(AdminKeyring)
	if err != nil {
		return "", fmt.Errorf("this NAS has no key of the Ceph cluster")
	}
	key := KeyOf(string(data), "client.admin")
	if key == "" {
		return "", fmt.Errorf("this NAS has no key of the Ceph cluster")
	}
	p := filepath.Join(StrataDir, "local.admin.secret")
	if old, err := os.ReadFile(p); err == nil && string(old) == key {
		return p, nil
	}
	return p, writeSecret(p, key)
}
