package lxc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Files inside a container's root file system are written by the NAS's root,
// but the container may have planted symbolic links anywhere in it: every
// path is resolved with openat2(RESOLVE_IN_ROOT), as if the root file system
// were "/", so a link can never lead out of it.

func openRoot(rootfs string) (int, error) {
	return unix.Open(rootfs, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

func openIn(root int, rel string, flags uint64, mode uint64) (int, error) {
	return unix.Openat2(root, rel, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Mode: mode, Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS})
}

// guestExists reports whether a path exists in the container (kind: "dir",
// "file" or "" for anything, symbolic links not followed for the last part).
func guestExists(rootfs, rel, kind string) bool {
	root, err := openRoot(rootfs)
	if err != nil {
		return false
	}
	defer unix.Close(root)
	dir, err := openIn(root, filepath.Dir(rel), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return false
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if unix.Fstatat(dir, filepath.Base(rel), &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	switch kind {
	case "dir":
		return st.Mode&unix.S_IFMT == unix.S_IFDIR
	case "file":
		return st.Mode&unix.S_IFMT == unix.S_IFREG
	}
	return true
}

func readGuest(rootfs, rel string) ([]byte, error) {
	root, err := openRoot(rootfs)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	fd, err := openIn(root, rel, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// writeGuest replaces a file of the container (through a temporary file and
// a rename in the same folder), owned by uid:gid of the host.
func writeGuest(rootfs, rel string, data []byte, mode uint32, uid, gid int) error {
	root, err := openRoot(rootfs)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	dir, err := openIn(root, filepath.Dir(rel), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Dir(rel), err)
	}
	defer unix.Close(dir)
	rnd := make([]byte, 6)
	_, _ = rand.Read(rnd)
	tmp := ".strata-" + hex.EncodeToString(rnd)
	fd, err := unix.Openat(dir, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Chmod(os.FileMode(mode))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(dir, tmp, dir, filepath.Base(rel))
	}
	if err != nil {
		_ = unix.Unlinkat(dir, tmp, 0)
	}
	return err
}

func removeGuest(rootfs, rel string) {
	root, err := openRoot(rootfs)
	if err != nil {
		return
	}
	defer unix.Close(root)
	dir, err := openIn(root, filepath.Dir(rel), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return
	}
	defer unix.Close(dir)
	_ = unix.Unlinkat(dir, filepath.Base(rel), 0)
}

// guestRoot is the host uid and gid of the container's root.
func guestRoot(s *Spec) (int, int) {
	if s.Privileged {
		return 0, 0
	}
	return s.IDMap.Base, s.IDMap.Base
}

// setGuestHostname writes /etc/hostname and renames the host in /etc/hosts.
func setGuestHostname(s *Spec, old string) error {
	uid, gid := guestRoot(s)
	rootfs := s.Storage.Rootfs()
	if err := writeGuest(rootfs, "etc/hostname", []byte(s.Name+"\n"), 0o644, uid, gid); err != nil {
		return err
	}
	if old == "" || old == s.Name {
		return nil
	}
	if b, err := readGuest(rootfs, "etc/hosts"); err == nil {
		return writeGuest(rootfs, "etc/hosts", []byte(renameHost(string(b), old, s.Name)), 0o644, uid, gid)
	}
	return nil
}

// renameHost replaces the host name old by name in a hosts file.
func renameHost(hosts, old, name string) string {
	lines := strings.Split(hosts, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		f := strings.Fields(l)
		changed := false
		for j := 1; j < len(f); j++ {
			if f[j] == old {
				f[j], changed = name, true
			}
		}
		if changed {
			lines[i] = strings.Join(f, "\t")
		}
	}
	return strings.Join(lines, "\n")
}

const (
	networkdFile = "etc/systemd/network/05-strata-eth0.network"
	netplanFile  = "etc/netplan/99-strata.yaml"
)

// networkdConfig is the systemd-networkd file of a static address.
func networkdConfig(n Network) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS (static address of the container)\n[Match]\nName=eth0\n\n[Network]\n")
	fmt.Fprintf(&b, "Address=%s\n", n.Address)
	if n.Gateway != "" {
		fmt.Fprintf(&b, "Gateway=%s\n", n.Gateway)
	}
	for _, d := range n.DNS {
		fmt.Fprintf(&b, "DNS=%s\n", d)
	}
	return b.String()
}

func netplanConfig(n Network) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS (static address of the container)\nnetwork:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: false\n")
	fmt.Fprintf(&b, "      addresses: [%s]\n", n.Address)
	if n.Gateway != "" {
		fmt.Fprintf(&b, "      routes:\n        - to: default\n          via: %s\n", n.Gateway)
	}
	if len(n.DNS) > 0 {
		fmt.Fprintf(&b, "      nameservers:\n        addresses: [%s]\n", strings.Join(n.DNS, ", "))
	}
	return b.String()
}

// rewriteInterfaces replaces the eth0 stanza of /etc/network/interfaces
// (ifupdown: Alpine, Devuan…) by DHCP or a static address.
func rewriteInterfaces(content string, n Network) string {
	var out []string
	skip := false
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 {
			switch f[0] {
			case "iface", "mapping", "auto", "allow-hotplug", "source", "source-directory":
				skip = f[0] == "iface" && len(f) > 1 && f[1] == "eth0"
			}
		}
		if !skip {
			out = append(out, l)
		}
	}
	hasAuto := false
	for _, l := range out {
		if f := strings.Fields(l); len(f) > 1 && (f[0] == "auto" || f[0] == "allow-hotplug") && strings.Contains(" "+strings.Join(f[1:], " ")+" ", " eth0 ") {
			hasAuto = true
		}
	}
	if !hasAuto {
		out = append(out, "auto eth0")
	}
	if n.Address == "" {
		out = append(out, "iface eth0 inet dhcp")
	} else {
		out = append(out, "iface eth0 inet static", "\taddress "+n.Address)
		if n.Gateway != "" {
			out = append(out, "\tgateway "+n.Gateway)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func resolvConf(dns []string) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS\n")
	for _, d := range dns {
		fmt.Fprintf(&b, "nameserver %s\n", d)
	}
	return b.String()
}

// applyGuestNetwork makes the container's own network configuration agree
// with its address: DHCP by default, or the static address (lxc also sets it
// on the interface before the container starts, for systems handled here).
func applyGuestNetwork(s *Spec) error {
	rootfs := s.Storage.Rootfs()
	uid, gid := guestRoot(s)
	n := s.Network
	static := n.Mode != "none" && n.Address != ""
	if guestExists(rootfs, "etc/systemd/network", "dir") {
		if static {
			if err := writeGuest(rootfs, networkdFile, []byte(networkdConfig(n)), 0o644, uid, gid); err != nil {
				return err
			}
		} else {
			removeGuest(rootfs, networkdFile)
		}
	}
	if guestExists(rootfs, "etc/netplan", "dir") {
		if static {
			if err := writeGuest(rootfs, netplanFile, []byte(netplanConfig(n)), 0o600, uid, gid); err != nil {
				return err
			}
		} else {
			removeGuest(rootfs, netplanFile)
		}
	}
	if guestExists(rootfs, "etc/network/interfaces", "file") {
		b, err := readGuest(rootfs, "etc/network/interfaces")
		if err == nil && (static || strings.Contains(string(b), "inet static")) {
			if err := writeGuest(rootfs, "etc/network/interfaces", []byte(rewriteInterfaces(string(b), n)), 0o644, uid, gid); err != nil {
				return err
			}
		}
	}
	if static && len(n.DNS) > 0 && guestExists(rootfs, "etc/resolv.conf", "file") {
		return writeGuest(rootfs, "etc/resolv.conf", []byte(resolvConf(n.DNS)), 0o644, uid, gid)
	}
	return nil
}
