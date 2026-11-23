package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Migration import: data of another NAS (SMB, NFS or rsync daemon) is copied
// into a shared folder of this NAS with rsync, without re-encoding anything.
// SMB and NFS sources are mounted read-only for the duration of the copy.

// RunDir holds the mount points and credential files, tmpfs, root only.
var RunDir = "/run/strata/migrate"

var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:]*[A-Za-z0-9\]]$|^[A-Za-z0-9]$`)

// Plan is one import: how the source is reached and the rsync command.
type Plan struct {
	Kind     string   // smb | nfs | rsync
	Mount    string   // local mount point for smb/nfs ("" for the rsync daemon)
	CredFile string   // SMB credentials file ("" otherwise)
	Env      []string // RSYNC_PASSWORD for the rsync daemon
	Src      string   // rsync source
	Dest     string   // local destination folder
	Delete   bool     // rsync --delete
}

// Validate checks the fields of a migration and normalizes them.
func Validate(m *store.Migration) error {
	m.Host = strings.TrimSpace(m.Host)
	m.Share = strings.TrimSpace(m.Share)
	m.Path = strings.TrimPrefix(filepath.Clean("/"+strings.TrimSpace(m.Path)), "/")
	m.User = strings.TrimSpace(m.User)
	m.Name = strings.TrimSpace(m.Name)
	m.Dest = strings.TrimSpace(m.Dest)
	switch m.Kind {
	case "smb", "nfs", "rsync":
	default:
		return fmt.Errorf("the source must be SMB, NFS or rsync")
	}
	if m.Name == "" {
		return fmt.Errorf("a name is required")
	}
	if len(m.Name) > 100 {
		return fmt.Errorf("the name must be 100 characters at most")
	}
	if !hostRe.MatchString(m.Host) || len(m.Host) > 253 {
		return fmt.Errorf("invalid host name or address")
	}
	if m.Share == "" || strings.ContainsAny(m.Share, "/\n\r") || len(m.Share) > 255 {
		return fmt.Errorf("invalid share, export or module name")
	}
	if strings.Contains(m.Path, "..") {
		return fmt.Errorf("invalid path inside the share")
	}
	if m.Port != 0 && (m.Port < 1 || m.Port > 65535) {
		return fmt.Errorf("invalid port")
	}
	if m.Kind == "rsync" && m.User == "" {
		return fmt.Errorf("the rsync daemon needs a user name")
	}
	if m.Dest == "" || !strings.HasPrefix(m.Dest, "/") || strings.Contains(m.Dest, "..") {
		return fmt.Errorf("the destination must be /Share/folder")
	}
	return nil
}

// Build makes the plan of one run of m, dest being the absolute local folder.
func Build(m store.Migration, dest string) (Plan, error) {
	if err := Validate(&m); err != nil {
		return Plan{}, err
	}
	p := Plan{Kind: m.Kind, Dest: dest, Delete: m.Delete}
	switch m.Kind {
	case "rsync":
		port := m.Port
		if port == 0 {
			port = 873
		}
		p.Src = fmt.Sprintf("rsync://%s@%s:%d/%s/%s/", m.User, m.Host, port, m.Share, m.Path)
		if m.Password != "" {
			p.Env = []string{"RSYNC_PASSWORD=" + m.Password}
		}
	case "smb":
		p.Mount = filepath.Join(RunDir, m.ID)
		p.CredFile = p.Mount + ".cred"
		p.Src = filepath.Join(p.Mount, m.Path) + "/"
	case "nfs":
		p.Mount = filepath.Join(RunDir, m.ID)
		p.Src = filepath.Join(p.Mount, m.Path) + "/"
	}
	return p, nil
}

// RsyncArgs is the rsync command of a plan (archive, stats, read-only source).
func (p Plan) RsyncArgs() []string {
	a := []string{"-a", "--stats", "--timeout=60", "--contimeout=30"}
	if p.Delete {
		a = append(a, "--delete")
	}
	return append(a, p.Src, p.Dest+"/")
}

// MountArgs is the mount command of a plan (smb or nfs), read-only.
func (p Plan) MountArgs(m store.Migration) []string {
	if p.Kind == "smb" {
		return []string{"-t", "cifs", "-o", "ro,credentials=" + p.CredFile + ",iocharset=utf8,vers=3.0", "//" + m.Host + "/" + m.Share, p.Mount}
	}
	return []string{"-t", "nfs", "-o", "ro,soft,timeo=50", m.Host + ":/" + m.Share, p.Mount}
}

// Executors, replaced in tests.
var (
	MountFn = func(ctx context.Context, args []string) error {
		_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 60 * time.Second}, "mount", args...)
		return err
	}
	UmountFn = func(mount string) error { _, err := sys.Run("umount", mount); return err }
	RsyncFn  = func(ctx context.Context, env, args []string) (string, error) {
		return sys.RunOpts(ctx, sys.Opts{Env: env}, "rsync", args...)
	}
	WriteCred = func(path, user, password string) error {
		return sys.WriteFileAtomic(path, []byte("username="+user+"\npassword="+password+"\n"), 0o600)
	}
)

// maxRun bounds one import (a week): the job context cancels it sooner.
const maxRun = 7 * 24 * time.Hour

// Execute runs one plan: mount when needed, rsync, umount. It returns the
// rsync output (--stats) for the caller to log and count.
func Execute(ctx context.Context, m store.Migration, p Plan) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, maxRun)
	defer cancel()
	if p.Mount != "" {
		if err := os.MkdirAll(RunDir, 0o700); err != nil {
			return "", err
		}
		if p.CredFile != "" {
			if err := WriteCred(p.CredFile, m.User, m.Password); err != nil {
				return "", err
			}
			defer os.Remove(p.CredFile)
		}
		if err := os.MkdirAll(p.Mount, 0o700); err != nil {
			return "", err
		}
		if err := MountFn(ctx, p.MountArgs(m)); err != nil {
			return "", fmt.Errorf("mount %s: %w", m.Host, err)
		}
		defer func() { _ = UmountFn(p.Mount) }()
	}
	return RsyncFn(ctx, p.Env, p.RsyncArgs())
}

// ParseStats reads the --stats output of rsync: files transferred and bytes.
func ParseStats(out string) (files, bytes int64) {
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "Number of regular files transferred:"):
			files, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(l, "Number of regular files transferred:")), 10, 64)
		case strings.HasPrefix(l, "Total transferred file size:"):
			f := strings.TrimSpace(strings.TrimPrefix(l, "Total transferred file size:"))
			if i := strings.IndexByte(f, ' '); i > 0 {
				f = f[:i]
			}
			bytes, _ = strconv.ParseInt(f, 10, 64)
		}
	}
	return files, bytes
}
