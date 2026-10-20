package backup

// Pulls: back up another NAS into a folder of this one, over rsync+SSH, from
// an rsync daemon (rsync://host/module) or from an SMB share mounted read-only.

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	moduleRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,63}$`)
	smbShareRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.\-$]{0,79}$`)
	smbUserRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._@\-]{0,63}$`)
	domainRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]{0,63}$`)
	rsyncUserRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@\-]{0,63}$`)
)

// IsPull reports whether the task copies another NAS into this one.
func IsPull(t *store.BackupTask) bool { return t.SourceType != "" }

func validExtras(t *store.BackupTask) error {
	for _, e := range t.Excludes {
		if strings.ContainsAny(e, "\n\r") || strings.HasPrefix(e, "-") {
			return fmt.Errorf("invalid exclude pattern %q", e)
		}
	}
	if t.BWLimit != "" && !bwRe.MatchString(t.BWLimit) {
		return fmt.Errorf("invalid bandwidth limit (e.g. 10M)")
	}
	if t.KeepLast < 0 || t.KeepDays < 0 {
		return fmt.Errorf("retention values must be positive")
	}
	return nil
}

func validRemote(t *store.BackupTask, needUser bool) error {
	if !hostRe.MatchString(t.Host) {
		return fmt.Errorf("invalid host")
	}
	if t.Port < 0 || t.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if needUser && !userRe.MatchString(t.User) {
		return fmt.Errorf("invalid SSH user")
	}
	return nil
}

// relPath checks a path below a module or share: no "..", no line breaks.
func relPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	if strings.ContainsAny(p, "\n\r\x00") {
		return "", fmt.Errorf("invalid path")
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." || c == "." || c == "" {
			return "", fmt.Errorf("invalid path %q", p)
		}
	}
	return p, nil
}

// localDest checks a destination folder of this NAS.
func localDest(p string) (string, error) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, storage.MountRoot+"/") || strings.ContainsAny(c, "\n\r") {
		return "", fmt.Errorf("a local destination must be inside a volume (/srv/strata/...)")
	}
	return c, nil
}

// validatePull checks an rsync task whose source is another NAS.
func validatePull(t *store.BackupTask) error {
	if err := validExtras(t); err != nil {
		return err
	}
	t.DestType = "local"
	d, err := localDest(t.DestPath)
	if err != nil {
		return err
	}
	t.DestPath = d
	if strings.ContainsAny(t.RemotePassword, "\n\r\x00") {
		return fmt.Errorf("invalid password")
	}
	switch t.SourceType {
	case "ssh":
		if err := validRemote(t, true); err != nil {
			return err
		}
		t.RemotePassword, t.Domain = "", "" // key only
		for i, s := range t.Sources {
			c := path.Clean(s)
			if !strings.HasPrefix(c, "/") || c == "/" || strings.ContainsAny(c, "\n\r\x00") {
				return fmt.Errorf("remote folders must be absolute paths, e.g. /volume1/photos")
			}
			t.Sources[i] = c
		}
	case "rsyncd":
		if err := validRemote(t, false); err != nil {
			return err
		}
		if t.User != "" && !rsyncUserRe.MatchString(t.User) {
			return fmt.Errorf("invalid rsync user")
		}
		t.Domain = ""
		for i, s := range t.Sources {
			mod, rest, _ := strings.Cut(strings.Trim(s, "/"), "/")
			if !moduleRe.MatchString(mod) {
				return fmt.Errorf("invalid rsync module %q", mod)
			}
			r, err := relPath(rest)
			if err != nil {
				return err
			}
			t.Sources[i] = strings.TrimSuffix(mod+"/"+r, "/")
		}
	case "smb":
		if err := validRemote(t, false); err != nil {
			return err
		}
		if t.User != "" && !smbUserRe.MatchString(t.User) {
			return fmt.Errorf("invalid SMB user")
		}
		if t.Domain != "" && !domainRe.MatchString(t.Domain) {
			return fmt.Errorf("invalid domain")
		}
		for i, s := range t.Sources {
			share, rest, _ := strings.Cut(strings.Trim(s, "/"), "/")
			if !smbShareRe.MatchString(share) {
				return fmt.Errorf("invalid share name %q", share)
			}
			r, err := relPath(rest)
			if err != nil {
				return err
			}
			t.Sources[i] = strings.TrimSuffix(share+"/"+r, "/")
		}
	default:
		return fmt.Errorf("unknown source type %q", t.SourceType)
	}
	seen := map[string]bool{}
	for _, s := range t.Sources {
		b := path.Base(s)
		if seen[b] {
			return fmt.Errorf("two sources would be copied to the same folder %q", b)
		}
		seen[b] = true
	}
	if t.Versions && t.KeepLast == 0 && t.KeepDays == 0 {
		t.KeepLast = 14
	}
	return nil
}

// rsyncCommonArgs are the transfer options shared by pushes and pulls.
func rsyncCommonArgs(t *store.BackupTask, mode []string) []string {
	args := append(append([]string{}, mode...), "--info=progress2", "--no-inc-recursive", "--protect-args")
	if t.Delete {
		args = append(args, "--delete", "--delete-excluded")
	}
	if t.BWLimit != "" {
		args = append(args, "--bwlimit="+t.BWLimit)
	}
	args = append(args, "--exclude=/.recycle/", "--exclude=/#recycle/", "--exclude=/.snapshots/", "--exclude=@eaDir/")
	for _, e := range t.Excludes {
		args = append(args, "--exclude="+e)
	}
	return args
}

// pullMode keeps owners but never device files or set-id bits: the other NAS
// is not trusted with root's files here.
var pullMode = []string{"-aH", "--numeric-ids", "--no-D", "--chmod=ug-s"}

// pullArgs builds the rsync command line copying one source into dest.
// from is the mount point of the SMB share (unused otherwise), passFile the
// rsync daemon password file ("" without password).
func pullArgs(t *store.BackupTask, src, from, passFile string) []string {
	var args []string
	var spec string
	switch t.SourceType {
	case "ssh":
		args = rsyncCommonArgs(t, pullMode)
		args = append(args, "-e", sshCommand(t))
		spec = fmt.Sprintf("%s@%s:%s/", t.User, sshHost(t.Host), src)
	case "rsyncd":
		args = rsyncCommonArgs(t, pullMode)
		if passFile != "" {
			args = append(args, "--password-file="+passFile)
		}
		user := ""
		if t.User != "" {
			user = t.User + "@"
		}
		spec = fmt.Sprintf("rsync://%s%s:%s/%s/", user, sshHost(t.Host), portOr(t.Port, 873), src)
	case "smb":
		// SMB carries neither Unix owners nor ACLs: times and contents only
		// (the files inherit the permissions of the destination).
		args = rsyncCommonArgs(t, []string{"-rlt", "--modify-window=2"})
		_, rest, _ := strings.Cut(src, "/")
		spec = strings.TrimRight(filepath.Join(from, rest), "/") + "/"
	}
	dest := filepath.Join(t.DestPath, path.Base(src)) + "/"
	return append(args, "--", spec, dest)
}

// smbCredentials is the mount.cifs credentials file of a task.
func smbCredentials(t *store.BackupTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "username=%s\npassword=%s\n", t.User, t.RemotePassword)
	if t.Domain != "" {
		fmt.Fprintf(&b, "domain=%s\n", t.Domain)
	}
	return b.String()
}

// smbMountArgs mounts a share read-only, credentials read from credFile.
func smbMountArgs(t *store.BackupTask, share, mp, credFile string) []string {
	opts := "ro,nosuid,nodev,noexec,iocharset=utf8,port=" + portOr(t.Port, 445)
	if t.User == "" {
		opts += ",guest"
	} else {
		opts += ",credentials=" + credFile
	}
	return []string{"-t", "cifs", "//" + t.Host + "/" + share, mp, "-o", opts}
}

// mountSMB mounts a share on a new private mount point and returns it with
// the function that unmounts it.
func mountSMB(ctx context.Context, t *store.BackupTask, share string) (string, func(), error) {
	if !sys.Has("mount.cifs") {
		return "", nil, fmt.Errorf("cifs-utils is not installed (apt install cifs-utils)")
	}
	cred, rmCred, err := secretFile(smbCredentials(t))
	if err != nil {
		return "", nil, err
	}
	defer rmCred() // only read by mount.cifs
	mp, err := os.MkdirTemp(SecretDir, "smb-")
	if err != nil {
		return "", nil, err
	}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "mount", smbMountArgs(t, share, mp, cred)...); err != nil {
		_ = os.Remove(mp)
		return "", nil, fmt.Errorf("cannot mount //%s/%s: %w", t.Host, share, err)
	}
	return mp, func() {
		if _, err := sys.Run("umount", mp); err != nil {
			_, _ = sys.Run("umount", "-l", mp)
		}
		_ = os.Remove(mp)
	}, nil
}

func pullLabel(t *store.BackupTask, src string) string {
	switch t.SourceType {
	case "ssh":
		return t.User + "@" + t.Host + ":" + src
	case "rsyncd":
		return "rsync://" + t.Host + "/" + src
	}
	return "//" + t.Host + "/" + src
}

func runPull(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if !sys.Has("rsync") {
		return "", fmt.Errorf("rsync is not installed")
	}
	start := time.Now()
	if t.SourceType == "ssh" {
		if _, err := PublicKey(); err != nil {
			return "", err
		}
	}
	passFile := ""
	if t.SourceType == "rsyncd" && t.RemotePassword != "" {
		f, rm, err := secretFile(t.RemotePassword + "\n")
		if err != nil {
			return "", err
		}
		defer rm()
		passFile = f
	}
	if t.Versions {
		if err := prepareVersionedDir(t.DestPath, p); err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(t.DestPath, 0o755); err != nil {
		return "", err
	}
	for i, src := range t.Sources {
		from := ""
		if t.SourceType == "smb" {
			share, _, _ := strings.Cut(src, "/")
			mp, umount, err := mountSMB(ctx, t, share)
			if err != nil {
				return "", err
			}
			defer umount()
			from = mp
		}
		p.Log("rsync %s -> %s", pullLabel(t, src), filepath.Join(t.DestPath, path.Base(src)))
		base := float64(i) / float64(len(t.Sources)) * 100
		lw := &lineWriter{fn: func(l string) {
			if m := rsyncPct.FindStringSubmatch(" " + l + " "); m != nil {
				n, _ := strconv.Atoi(m[1])
				p.Set(base + float64(n)/float64(len(t.Sources)))
			} else if strings.TrimSpace(l) != "" {
				p.Log("%s", l)
			}
		}}
		if _, err := sys.RunOpts(ctx, sys.Opts{Output: lw, Timeout: 48 * time.Hour}, "rsync", pullArgs(t, src, from, passFile)...); err != nil {
			// 24: files vanished during transfer (normal on a live NAS).
			if ce, ok := err.(*sys.CmdError); !ok || ce.Code != 24 {
				return "", err
			}
		}
	}
	took := time.Since(start).Round(time.Second)
	if t.Versions {
		snap, err := snapshotFolder(t.DestPath, "pull-"+t.ID+"-", t.KeepLast, t.KeepDays, p)
		if err != nil {
			return "", fmt.Errorf("files copied, but the snapshot failed: %w", err)
		}
		if snap != "" {
			p.Log("%s", snap)
			return fmt.Sprintf("Copy of %d folder(s) from %s finished in %s, snapshot taken", len(t.Sources), t.Host, took), nil
		}
	}
	return fmt.Sprintf("Copy of %d folder(s) from %s finished in %s", len(t.Sources), t.Host, took), nil
}

// testPull lists the first source to check access and credentials.
func testPull(ctx context.Context, t *store.BackupTask) (string, error) {
	if len(t.Sources) == 0 {
		return "", fmt.Errorf("choose at least one source")
	}
	src := t.Sources[0]
	switch t.SourceType {
	case "ssh", "rsyncd":
		args := []string{"--list-only", "--protect-args"}
		var spec string
		if t.SourceType == "ssh" {
			if _, err := PublicKey(); err != nil {
				return "", err
			}
			args = append(args, "-e", sshCommand(t))
			spec = fmt.Sprintf("%s@%s:%s/", t.User, sshHost(t.Host), src)
		} else {
			if t.RemotePassword != "" {
				f, rm, err := secretFile(t.RemotePassword + "\n")
				if err != nil {
					return "", err
				}
				defer rm()
				args = append(args, "--password-file="+f)
			}
			user := ""
			if t.User != "" {
				user = t.User + "@"
			}
			spec = fmt.Sprintf("rsync://%s%s:%s/%s/", user, sshHost(t.Host), portOr(t.Port, 873), src)
		}
		out, err := sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "rsync", append(args, "--", spec)...)
		if err != nil {
			if t.SourceType == "ssh" {
				return "", fmt.Errorf("SSH connection failed (is the StrataOS public key in ~/.ssh/authorized_keys of %s?): %w", t.User, err)
			}
			return "", err
		}
		// The listing starts with the folder itself (".").
		return fmt.Sprintf("Connection successful (%d item(s) in %s)", max(countLines(out)-1, 0), src), nil
	case "smb":
		share, rest, _ := strings.Cut(src, "/")
		mp, umount, err := mountSMB(ctx, t, share)
		if err != nil {
			return "", err
		}
		defer umount()
		ents, err := os.ReadDir(filepath.Join(mp, rest))
		if err != nil {
			return "", fmt.Errorf("share mounted, but %s cannot be read", src)
		}
		return fmt.Sprintf("Connection successful (%d item(s) in %s)", len(ents), src), nil
	}
	return "", fmt.Errorf("unknown source type")
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}
