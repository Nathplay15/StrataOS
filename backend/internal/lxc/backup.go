package lxc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Backups are tar.zst archives: container.json, then rootfs/. tar runs inside
// a user namespace with the container's id mapping, so the archive holds the
// ids as the container sees them (0 for its root): it can be restored on
// another NAS whatever range that NAS gives its containers, file
// capabilities included. A running container is copied from a snapshot
// (Btrfs, ZFS, image) so the copy is consistent; a plain folder is copied
// live unless the mode says to stop it.

// ExportOpts says where and how to write a backup.
type ExportOpts struct {
	Dir  string `json:"dir"`            // folder inside a volume
	File string `json:"file,omitempty"` // file name (default <name>-<time>.tar.zst)
	Mode string `json:"mode,omitempty"` // "" (snapshot when possible) | stop | live
	Keep int    `json:"keep,omitempty"` // older backups of this container kept in Dir (0: all)
	// AnyDir: Dir was validated by the caller (destination of a backup task).
	AnyDir bool `json:"-"`
}

// Archive is a container backup found on a volume.
type Archive struct {
	Path      string    `json:"path"`
	Container string    `json:"container"`
	Created   time.Time `json:"created"`
	Size      int64     `json:"size"`
}

var archiveRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)-(\d{8}-\d{6})\.tar\.zst$`)

// ValidMode checks a backup mode.
func ValidMode(m string) bool { return m == "" || m == "stop" || m == "live" }

// usernsArgs returns the lxc-usernsexec prefix of a container's mapping
// (nothing for a privileged container).
func usernsArgs(s *Spec) []string {
	if s.Privileged {
		return nil
	}
	args := []string{"lxc-usernsexec"}
	for _, l := range s.IDMap.Lines() {
		args = append(args, "-m", strings.ReplaceAll(l, " ", ":"))
	}
	return append(args, "--")
}

func command(ctx context.Context, argv []string) *exec.Cmd {
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

var tarCommon = []string{"--numeric-owner", "--xattrs", "--xattrs-include=*", "--acls", "--zstd"}

// Export writes a backup of a container and returns its path.
func Export(ctx context.Context, name string, o ExportOpts, logf func(string, ...any)) (string, int64, error) {
	if !ValidMode(o.Mode) {
		return "", 0, fmt.Errorf("unknown backup mode %q", o.Mode)
	}
	dir, err := insideStrata(o.Dir)
	if o.AnyDir && filepath.IsAbs(o.Dir) {
		dir, err = filepath.Clean(o.Dir), nil
	}
	if err != nil {
		return "", 0, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", 0, fmt.Errorf("%s not found", dir)
	}
	file := o.File
	if file == "" {
		file = name + "-" + time.Now().Format("20060102-150405") + ".tar.zst"
	}
	if strings.ContainsAny(file, "/\n") || !strings.HasSuffix(file, ".tar.zst") {
		return "", 0, fmt.Errorf("invalid file name")
	}
	unlock := lock(name)
	if unlock == nil {
		return "", 0, busy(name)
	}
	defer unlock()
	s, err := Load(name)
	if err != nil {
		return "", 0, err
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return "", 0, err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return "", 0, err
	}
	tree := s.Storage.Rootfs()
	running := State(name) == "running"
	consistency := "stopped"
	if running {
		switch {
		case o.Mode == "stop":
			logf("Stopping %s for the copy", name)
			if err := Stop(name); err != nil {
				return "", 0, err
			}
			if !WaitState(ctx, name, "stopped", 3*time.Minute) {
				return "", 0, fmt.Errorf("%s did not stop", name)
			}
			defer func() {
				logf("Starting %s again", name)
				unlock()
				unlock = func() {}
				if err := Start(context.Background(), name); err != nil {
					logf("%s did not start again: %v", name, err)
				}
			}()
		case o.Mode == "" && s.Storage.Backend != "dir":
			const snap = "strata-backup"
			_ = be.DeleteSnapshot(ctx, &s.Storage, snap)
			if err := os.MkdirAll(s.Storage.snapDir(), 0o711); err != nil {
				return "", 0, err
			}
			_ = os.Chmod(s.Storage.snapDir(), 0o711)
			if err := be.Snapshot(ctx, &s.Storage, snap); err != nil {
				return "", 0, err
			}
			defer func() { _ = be.DeleteSnapshot(context.Background(), &s.Storage, snap) }()
			tree = be.SnapshotTree(&s.Storage, snap)
			if s.Storage.Backend == "image" {
				mnt := filepath.Join(s.Storage.Base, ".backup-mnt")
				_ = os.MkdirAll(mnt, 0o711)
				if _, err := sys.Run("mount", "-o", "loop,ro,noload", filepath.Join(s.Storage.snapDir(), snap+".img"), mnt); err != nil {
					return "", 0, err
				}
				defer func() { _, _ = sys.Run("umount", mnt); _ = os.Remove(mnt) }()
				tree = mnt
			}
			consistency = "snapshot"
		default:
			consistency = "live"
		}
	}
	logf("Backing up %s (%s copy) to %s", name, consistency, filepath.Join(dir, file))

	// container.json, readable from inside the user namespace.
	stage, err := os.MkdirTemp(s.Storage.Base, ".export-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(stage)
	_ = os.Chmod(stage, 0o755)
	b, err := os.ReadFile(filepath.Join(Dir(name), specFile))
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(filepath.Join(stage, specFile), b, 0o644); err != nil {
		return "", 0, err
	}
	final := filepath.Join(dir, file)
	part := final + ".partial"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	argv := append(usernsArgs(s), "tar", "--create", "--format=posix")
	argv = append(argv, tarCommon...)
	argv = append(argv, "--file=-", "-C", stage, specFile, "-C", tree, `--transform=s,^\.,rootfs,`, "--exclude=./dev/*", ".")
	cmd := command(ctx, argv)
	cmd.Stdout = out
	errb := &limitedText{max: 8 << 10}
	cmd.Stderr = errb
	err = cmd.Run()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	// tar exits with 1 when files changed while it read them (live copy).
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && consistency == "live" {
		logf("Some files changed during the copy: %s", strings.TrimSpace(errb.String()))
		err = nil
	}
	if err != nil {
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("tar: %v %s", err, strings.TrimSpace(errb.String()))
	}
	if err := os.Rename(part, final); err != nil {
		return "", 0, err
	}
	fi, _ := os.Stat(final)
	size := fi.Size()
	logf("Backup of %s written (%d MB)", name, size>>20)
	if o.Keep > 0 && o.File == "" {
		list := Archives(dir)
		var mine []Archive
		for _, a := range list {
			if a.Container == name {
				mine = append(mine, a)
			}
		}
		for i := o.Keep; i < len(mine); i++ {
			logf("Removing the older backup %s", filepath.Base(mine[i].Path))
			_ = os.Remove(mine[i].Path)
		}
	}
	return final, size, nil
}

type limitedText struct {
	b   strings.Builder
	max int
}

func (l *limitedText) Write(p []byte) (int, error) {
	if l.b.Len() < l.max {
		l.b.Write(p[:min(len(p), l.max-l.b.Len())])
	}
	return len(p), nil
}
func (l *limitedText) String() string { return l.b.String() }

// Archives lists the container backups of a folder, newest first.
func Archives(dir string) []Archive {
	entries, _ := os.ReadDir(dir)
	out := []Archive{}
	for _, e := range entries {
		m := archiveRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", m[3], time.Local)
		if err != nil {
			continue
		}
		fi, _ := e.Info()
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		out = append(out, Archive{Path: filepath.Join(dir, e.Name()), Container: m[1], Created: t, Size: size})
	}
	slices.SortFunc(out, func(a, b Archive) int { return b.Created.Compare(a.Created) })
	return out
}

// Importable is a container that can be brought into this NAS: a backup, or
// a container folder on a volume that is not registered (moved disks).
type Importable struct {
	Kind      string    `json:"kind"` // archive | folder
	Path      string    `json:"path"`
	Container string    `json:"container"`
	Created   time.Time `json:"created"`
	Size      int64     `json:"size,omitempty"`
}

// FindImportable looks for container backups (.tar.zst, depth-limited) and
// unregistered container folders in the volumes.
func FindImportable() []Importable {
	out := []Importable{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 || len(out) > 300 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.Name() == ".strata-lxc" && e.IsDir() {
				folders, _ := os.ReadDir(p)
				for _, f := range folders {
					base := filepath.Join(p, f.Name())
					b, err := os.ReadFile(filepath.Join(base, specFile))
					if err != nil {
						continue
					}
					s, err := parseSpec(b)
					if err != nil {
						continue
					}
					if cur, err := Load(s.Name); err == nil && cur.Storage.Base == base {
						continue
					}
					out = append(out, Importable{Kind: "folder", Path: base, Container: s.Name, Created: s.Created})
				}
				continue
			}
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				walk(p, depth+1)
			} else if m := archiveRe.FindStringSubmatch(e.Name()); m != nil || (e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".tar.zst")) {
				fi, _ := e.Info()
				it := Importable{Kind: "archive", Path: p}
				if fi != nil {
					it.Size, it.Created = fi.Size(), fi.ModTime()
				}
				if m != nil {
					it.Container = m[1]
				}
				out = append(out, it)
			}
		}
	}
	walk(MountRoot, 0)
	return out
}

// ImportReq brings a backup or a container folder into this NAS.
type ImportReq struct {
	Source   string `json:"source"`   // .tar.zst backup or container folder
	Name     string `json:"name"`     // default: the original name
	Location string `json:"location"` // archives: where to put the root file system
	Backend  string `json:"backend"`
	SizeGB   int    `json:"sizeGb"`
	Start    bool   `json:"start"`
}

// ReadArchiveSpec reads container.json from a backup.
func ReadArchiveSpec(ctx context.Context, archive string) (*Spec, error) {
	p, err := insideStrata(archive)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(p, ".tar.zst") {
		return nil, fmt.Errorf("choose a .tar.zst container backup")
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Minute}, "tar", "--zstd", "-xOf", p, specFile)
	if err != nil {
		return nil, fmt.Errorf("not a container backup: %v", err)
	}
	return parseSpec([]byte(out))
}

// fitSpec adapts an imported spec to this NAS: mounts of folders that are
// not here are dropped, a missing bridge falls back to NAT, a MAC address
// already in use is replaced.
func fitSpec(s *Spec, logf func(string, ...any)) {
	var mounts []Mount
	for _, m := range s.Mounts {
		if fi, err := os.Stat(m.Source); err == nil && fi.IsDir() {
			mounts = append(mounts, m)
		} else {
			logf("Shared folder %s (%s) is not on this NAS: its mount is removed", m.Share, m.Source)
		}
	}
	s.Mounts = mounts
	if s.Network.Mode == "bridge" {
		if _, err := os.Stat("/sys/class/net/" + s.Network.Bridge + "/bridge"); err != nil {
			logf("Bridge %s is not on this NAS: the container uses NAT", s.Network.Bridge)
			s.Network.Mode, s.Network.Bridge = "nat", ""
		}
	}
	for _, n := range Names() {
		if o, err := Load(n); err == nil && o.Name != s.Name && strings.EqualFold(o.Network.MAC, s.Network.MAC) {
			s.Network.MAC = NewMAC()
			logf("New MAC address %s (the old one is used by %s)", s.Network.MAC, n)
		}
	}
	s.Autostart = false
}

// Import restores a backup as a new container, or registers a container
// folder found on a volume.
func Import(ctx context.Context, r ImportReq, logf func(string, ...any)) (err error) {
	if !Installed() {
		return fmt.Errorf("containers (LXC) are not installed")
	}
	src := filepath.Clean(r.Source)
	if !strings.HasPrefix(src, MountRoot+"/") || strings.ContainsAny(src, "\n\r\x00") {
		return fmt.Errorf("%s must be inside a volume", r.Source)
	}
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		if filepath.Base(filepath.Dir(src)) != ".strata-lxc" {
			return fmt.Errorf("%s is not a container folder", src)
		}
		return adopt(ctx, src, r, logf)
	}
	s, err := ReadArchiveSpec(ctx, src)
	if err != nil {
		return err
	}
	orig := s.Name
	if r.Name != "" {
		s.Name = r.Name
	}
	if err := ValidName(s.Name); err != nil {
		return err
	}
	unlock := lock(s.Name)
	if unlock == nil {
		return busy(s.Name)
	}
	defer unlock()
	if _, err := os.Stat(Dir(s.Name)); err == nil {
		return fmt.Errorf("a container named %s already exists", s.Name)
	}
	if r.Location == "" {
		r.Location, r.Backend = s.Storage.Location, s.Storage.Backend
	}
	if r.Backend == "" {
		r.Backend = s.Storage.Backend
	}
	loc, err := CheckLocation(r.Location, r.Backend)
	if err != nil {
		return err
	}
	size := r.SizeGB
	if size == 0 {
		size = s.Storage.SizeGB
	}
	if r.Backend != "dir" && size < 1 {
		size = 8
	}
	s.Storage = StorageSpec{Backend: r.Backend, Location: loc, Base: baseFor(loc, s.Name), SizeGB: size}
	if !s.Privileged {
		if s.IDMap, err = EnsureIDMap(s.IDMap.UIDs, s.IDMap.GIDs); err != nil {
			return err
		}
	}
	fitSpec(s, logf)
	if err := s.Validate(); err != nil {
		return err
	}
	if err := EnsureUnit(); err != nil {
		return err
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := mkBase(s.Storage.Base); err != nil {
		return err
	}
	created := false
	defer func() {
		if err != nil {
			if created {
				_ = be.Destroy(context.Background(), &s.Storage)
			} else {
				_ = removeBase(&s.Storage)
			}
			_ = os.RemoveAll(Dir(s.Name))
			removeDropIn(s.Name)
		}
	}()
	if err = be.Create(ctx, &s.Storage); err != nil {
		return err
	}
	created = true
	ensureTraverse(s)
	uid, gid := guestRoot(s)
	if err = os.Chown(s.Storage.Rootfs(), uid, gid); err != nil {
		return err
	}
	logf("Restoring the files of %s from %s", orig, src)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	argv := append(usernsArgs(s), "tar", "--extract", "--same-permissions", "--file=-", "-C", s.Storage.Rootfs(), "--strip-components=1")
	argv = append(argv, tarCommon...)
	argv = append(argv, "rootfs")
	cmd := command(ctx, argv)
	cmd.Stdin = in
	errb := &limitedText{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = io.Discard, errb
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("tar: %v %s", err, strings.TrimSpace(errb.String()))
	}
	if err = os.MkdirAll(Dir(s.Name), 0o750); err != nil {
		return err
	}
	if err = save(s); err != nil {
		return err
	}
	if err = writeConfig(s); err != nil {
		return err
	}
	if err = writeDropIn(s); err != nil {
		return err
	}
	if s.Name != orig {
		_ = setGuestHostname(s, orig)
	}
	_ = applyGuestNetwork(s)
	logf("Container %s restored", s.Name)
	if r.Start {
		unlock()
		unlock = func() {}
		if err := Start(ctx, s.Name); err != nil {
			logf("The container did not start: %v", err)
		}
	}
	return nil
}

// adopt registers a container folder found on a volume (disks moved from
// another NAS, or a reinstalled NAS). Its files keep their owners: when this
// NAS gives containers another id range, the owners are shifted.
func adopt(ctx context.Context, base string, r ImportReq, logf func(string, ...any)) error {
	b, err := os.ReadFile(filepath.Join(base, specFile))
	if err != nil {
		return fmt.Errorf("%s holds no container", base)
	}
	s, err := parseSpec(b)
	if err != nil {
		return err
	}
	if filepath.Base(base) != s.Name {
		return fmt.Errorf("the folder of %s must be named after it", s.Name)
	}
	if r.Name != "" && r.Name != s.Name {
		return fmt.Errorf("rename the container after adding it")
	}
	unlock := lock(s.Name)
	if unlock == nil {
		return busy(s.Name)
	}
	defer unlock()
	if _, err := os.Stat(Dir(s.Name)); err == nil {
		return fmt.Errorf("a container named %s already exists", s.Name)
	}
	s.Storage.Base = base
	s.Storage.Location = filepath.Dir(filepath.Dir(base))
	if s.Storage.Backend == "zfs" {
		if out, err := sys.Run("zfs", "list", "-H", "-o", "name", s.Storage.Rootfs()); err == nil {
			s.Storage.Dataset = strings.TrimSpace(out)
		} else {
			return fmt.Errorf("the ZFS dataset of %s is not mounted at %s", s.Name, s.Storage.Rootfs())
		}
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return err
	}
	if !s.Privileged {
		old := s.IDMap
		m, err := EnsureIDMap(old.UIDs, old.GIDs)
		if err != nil {
			return err
		}
		if m.Base != old.Base {
			logf("Shifting the owners of the files from %d to %d", old.Base, m.Base)
			if err := shiftOwners(ctx, s, old, m); err != nil {
				return err
			}
		}
		s.IDMap = m
	}
	fitSpec(s, logf)
	if err := s.Validate(); err != nil {
		return err
	}
	if err := EnsureUnit(); err != nil {
		return err
	}
	ensureTraverse(s)
	if err := save(s); err != nil {
		return err
	}
	if err := writeConfig(s); err != nil {
		return err
	}
	if err := writeDropIn(s); err != nil {
		return err
	}
	logf("Container %s added", s.Name)
	return nil
}

// shiftOwners moves the files of a root file system from one id range to
// another: a tar copy from the old user namespace into the new one (the
// kernel rewrites owners and file capabilities on the way), into a folder of
// the same file system that then replaces the old tree.
func shiftOwners(ctx context.Context, s *Spec, from, to IDMap) error {
	const shift = ".strata-shift"
	rootfs := s.Storage.Rootfs()
	tmp := filepath.Join(rootfs, shift)
	_ = os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Chown(tmp, to.Base, to.Base); err != nil {
		return err
	}
	a, b := *s, *s
	a.IDMap, b.IDMap = from, to
	pack := command(ctx, append(usernsArgs(&a), "tar", "--create", "--format=posix", "--numeric-owner", "--xattrs", "--xattrs-include=*", "--acls", "--file=-", "-C", rootfs, "--exclude=./"+shift, "."))
	unpack := command(ctx, append(usernsArgs(&b), "tar", "--extract", "--same-permissions", "--numeric-owner", "--xattrs", "--xattrs-include=*", "--acls", "--file=-", "-C", tmp))
	r, err := pack.StdoutPipe()
	if err != nil {
		return err
	}
	unpack.Stdin = r
	if err := unpack.Start(); err != nil {
		return err
	}
	if err := pack.Run(); err != nil {
		_ = unpack.Wait()
		return err
	}
	if err := unpack.Wait(); err != nil {
		return err
	}
	entries, err := os.ReadDir(rootfs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == shift {
			continue
		}
		if err := os.RemoveAll(filepath.Join(rootfs, e.Name())); err != nil {
			return err
		}
	}
	moved, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range moved {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(rootfs, e.Name())); err != nil {
			return err
		}
	}
	_ = os.Remove(tmp)
	return os.Chown(rootfs, to.Base, to.Base)
}
