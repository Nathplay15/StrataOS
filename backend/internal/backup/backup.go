// Package backup runs backup tasks: rsync mirrors, restic versioned backups, ZFS
// replication (push or pull), pulls from other NAS and two-way syncs.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// KeyDir holds the SSH identity StrataOS uses for remote destinations.
var KeyDir = "/var/lib/strata/ssh"

func keyPath() string        { return filepath.Join(KeyDir, "id_ed25519") }
func knownHostsPath() string { return filepath.Join(KeyDir, "known_hosts") }

// PublicKey returns StrataOS' SSH public key, creating the key pair on first use.
func PublicKey() (string, error) {
	if _, err := os.Stat(keyPath()); err != nil {
		if err := os.MkdirAll(KeyDir, 0o700); err != nil {
			return "", err
		}
		host, _ := os.Hostname()
		if _, err := sys.Run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "strataos@"+host, "-f", keyPath()); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(keyPath() + ".pub")
	return strings.TrimSpace(string(b)), err
}

var (
	hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:\[\]]{0,252}$`)
	userRe = regexp.MustCompile(`^[a-z_][a-z0-9_.\-]{0,31}$`)
)

// sshArgs are the options shared by every SSH connection to a backup target.
func sshArgs(t *store.BackupTask) []string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return []string{"-i", keyPath(), "-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHostsPath(), "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30"}
}

func sshCommand(t *store.BackupTask) string {
	return "ssh " + strings.Join(sshArgs(t), " ")
}

// shellQuote quotes s for a POSIX shell (used for commands run on remote hosts).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Validate checks a task before it is saved and normalises its fields.
func Validate(t *store.BackupTask) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(t.Sources) == 0 && len(t.VMs) == 0 {
		return fmt.Errorf("choose at least one source")
	}
	if err := validateVMs(t); err != nil {
		return err
	}
	if t.Kind == "sync" {
		return validateSync(t)
	}
	if IsPull(t) {
		switch t.Kind {
		case "rsync":
			return validatePull(t)
		case "zfs":
			return validateZFSPull(t)
		}
		return fmt.Errorf("this backup type cannot pull from another NAS")
	}
	if t.Kind != "zfs" {
		for i, src := range t.Sources {
			c := filepath.Clean(src)
			if !strings.HasPrefix(c, storage.MountRoot+"/") || strings.Contains(c, "\n") {
				return fmt.Errorf("sources must be folders inside /srv/strata")
			}
			t.Sources[i] = c
		}
	}
	if err := validExtras(t); err != nil {
		return err
	}
	remote := t.DestType == "ssh" || t.DestType == "sftp"
	if remote {
		if !hostRe.MatchString(t.Host) {
			return fmt.Errorf("invalid host")
		}
		if !userRe.MatchString(t.User) {
			return fmt.Errorf("invalid SSH user")
		}
		if t.Port < 0 || t.Port > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	if strings.ContainsAny(t.DestPath, "\n\r") || strings.HasPrefix(t.DestPath, "-") {
		return fmt.Errorf("invalid destination")
	}
	switch t.Kind {
	case "rsync":
		switch t.DestType {
		case "local":
			c := filepath.Clean(t.DestPath)
			if !strings.HasPrefix(c, storage.MountRoot+"/") {
				return fmt.Errorf("a local destination must be inside a volume (/srv/strata/...)")
			}
			for _, src := range t.Sources {
				if c == src || strings.HasPrefix(c, src+"/") {
					return fmt.Errorf("the destination cannot be inside a source folder")
				}
			}
			t.DestPath = c
		case "ssh":
			if !strings.HasPrefix(t.DestPath, "/") {
				return fmt.Errorf("the remote path must be absolute")
			}
		default:
			return fmt.Errorf("rsync destinations are local folders or SSH servers")
		}
	case "restic":
		switch t.DestType {
		case "local":
			c := filepath.Clean(t.DestPath)
			if !strings.HasPrefix(c, storage.MountRoot+"/") {
				return fmt.Errorf("a local repository must be inside a volume (/srv/strata/...)")
			}
			t.DestPath = c
		case "sftp":
			if !strings.HasPrefix(t.DestPath, "/") {
				return fmt.Errorf("the remote path must be absolute")
			}
		case "s3":
			if t.S3Endpoint == "" || t.DestPath == "" || t.AccessKey == "" || t.SecretKey == "" {
				return fmt.Errorf("S3 needs an endpoint, a bucket and access keys")
			}
			if u, err := url.Parse(t.S3Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return fmt.Errorf("the S3 endpoint must be an http(s) URL")
			}
		case "rest":
			if u, err := url.Parse(t.DestPath); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return fmt.Errorf("the REST server must be an http(s) URL")
			}
		case "strataos":
			if u, err := url.Parse(t.VaultURL); err != nil || u.Scheme != "https" || t.VaultToken == "" || len(t.VaultCert) != 64 {
				return fmt.Errorf("pair this NAS with the vault first")
			}
		default:
			return fmt.Errorf("unsupported restic destination")
		}
		if len(t.Password) < 8 {
			return fmt.Errorf("the repository password must be at least 8 characters (keep it safe: without it the backup cannot be restored)")
		}
		if t.KeepLast+t.KeepDaily+t.KeepWeekly+t.KeepMonthly == 0 {
			t.KeepDaily, t.KeepWeekly, t.KeepMonthly = 7, 4, 12
		}
	case "zfs":
		if len(t.Sources) != 1 || storage.ValidDS(t.Sources[0]) != nil || strings.Contains(t.Sources[0], "@") {
			return fmt.Errorf("choose one ZFS dataset to replicate")
		}
		if storage.ValidDS(t.DestPath) != nil || strings.Contains(t.DestPath, "@") {
			return fmt.Errorf("the destination must be a ZFS dataset name (pool/backup/...)")
		}
		if t.DestType != "local" && t.DestType != "ssh" {
			return fmt.Errorf("ZFS replication goes to a local pool or an SSH server")
		}
		if t.DestType == "local" && (t.DestPath == t.Sources[0] || strings.HasPrefix(t.DestPath, t.Sources[0]+"/")) {
			return fmt.Errorf("the destination cannot be inside the source dataset")
		}
		if t.KeepLast <= 0 {
			t.KeepLast = 7
		}
	default:
		return fmt.Errorf("unknown backup type %q", t.Kind)
	}
	return nil
}

// lineWriter turns command output into lines (splitting on \r too, for progress bars).
type lineWriter struct {
	buf bytes.Buffer
	fn  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			if w.buf.Len() > 0 {
				w.fn(w.buf.String())
				w.buf.Reset()
			}
			continue
		}
		w.buf.WriteByte(b)
	}
	return len(p), nil
}

// Progress reports a percentage (-1 when unknown) and log lines.
type Progress struct {
	Set func(float64)
	Log func(string, ...any)
}

// Run executes one backup and returns a summary.
func Run(ctx context.Context, t store.BackupTask, p Progress) (string, error) {
	if len(t.VMs) > 0 {
		return runWithVMs(ctx, &t, p)
	}
	switch t.Kind {
	case "rsync":
		if IsPull(&t) {
			return runPull(ctx, &t, p)
		}
		return runRsync(ctx, &t, p)
	case "restic":
		return runRestic(ctx, &t, p)
	case "zfs":
		if IsPull(&t) {
			return runZFSPull(ctx, &t, p)
		}
		return runZFS(ctx, &t, p)
	case "sync":
		return runSync(ctx, &t, p)
	}
	return "", fmt.Errorf("unknown backup type")
}

var rsyncPct = regexp.MustCompile(`\s(\d{1,3})%\s`)

func runRsync(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if !sys.Has("rsync") {
		return "", fmt.Errorf("rsync is not installed")
	}
	start := time.Now()
	for i, src := range t.Sources {
		fi, err := os.Stat(src)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("source %s is missing", src)
		}
		name := filepath.Base(src)
		args := rsyncCommonArgs(t, []string{"-aHAX", "--numeric-ids"})
		var dest string
		if t.DestType == "local" {
			if err := os.MkdirAll(t.DestPath, 0o755); err != nil {
				return "", err
			}
			dest = filepath.Join(t.DestPath, name) + "/"
		} else {
			if _, err := PublicKey(); err != nil {
				return "", err
			}
			args = append(args, "-e", sshCommand(t), "--rsync-path=mkdir -p "+shellQuote(t.DestPath)+" && rsync")
			dest = fmt.Sprintf("%s@%s:%s/", t.User, t.Host, strings.TrimRight(t.DestPath, "/")+"/"+name)
		}
		args = append(args, "--", strings.TrimRight(src, "/")+"/", dest)
		p.Log("rsync %s -> %s", src, dest)
		base := float64(i) / float64(len(t.Sources)) * 100
		lw := &lineWriter{fn: func(l string) {
			if m := rsyncPct.FindStringSubmatch(" " + l + " "); m != nil {
				n, _ := strconv.Atoi(m[1])
				p.Set(base + float64(n)/float64(len(t.Sources)))
			} else if strings.TrimSpace(l) != "" {
				p.Log("%s", l)
			}
		}}
		if _, err := sys.RunOpts(ctx, sys.Opts{Output: lw, Timeout: 48 * time.Hour}, "rsync", args...); err != nil {
			// 24: files vanished during transfer (normal on a live NAS).
			if ce, ok := err.(*sys.CmdError); !ok || ce.Code != 24 {
				return "", err
			}
		}
	}
	return fmt.Sprintf("Copy of %d folder(s) finished in %s", len(t.Sources), time.Since(start).Round(time.Second)), nil
}

// resticEnv builds the repository location and credentials for restic.
func resticEnv(t *store.BackupTask) []string {
	var repo string
	switch t.DestType {
	case "local":
		repo = t.DestPath
	case "sftp":
		repo = fmt.Sprintf("sftp:%s@%s:%s", t.User, t.Host, t.DestPath)
	case "s3":
		repo = "s3:" + strings.TrimRight(t.S3Endpoint, "/") + "/" + strings.TrimLeft(t.DestPath, "/")
	case "rest":
		repo = "rest:" + t.DestPath
	}
	env := []string{"RESTIC_REPOSITORY=" + repo, "RESTIC_PASSWORD=" + t.Password, "RESTIC_CACHE_DIR=/var/cache/strata/restic", "HOME=/root"}
	if t.DestType == "s3" {
		env = append(env, "AWS_ACCESS_KEY_ID="+t.AccessKey, "AWS_SECRET_ACCESS_KEY="+t.SecretKey)
	}
	return env
}

func resticArgs(t *store.BackupTask, args ...string) []string {
	if t.DestType == "sftp" {
		port := t.Port
		if port == 0 {
			port = 22
		}
		cmd := fmt.Sprintf("ssh %s %s@%s -s sftp", strings.Join(sshArgs(t), " "), t.User, t.Host)
		args = append([]string{"-o", "sftp.command=" + cmd}, args...)
	}
	return args
}

func restic(ctx context.Context, t *store.BackupTask, out *lineWriter, args ...string) (string, error) {
	if t.DestType == "strataos" {
		p, err := startVaultProxy(t)
		if err != nil {
			return "", err
		}
		defer p.close()
		c := *t
		c.DestType, c.DestPath = "rest", p.url
		t = &c
	}
	o := sys.Opts{Env: resticEnv(t), Timeout: 48 * time.Hour}
	if out != nil {
		o.Output = out
	}
	return sys.RunOpts(ctx, o, "restic", resticArgs(t, args...)...)
}

// ensureRepo initialises the restic repository on first use.
func ensureRepo(ctx context.Context, t *store.BackupTask, p Progress) error {
	if !sys.Has("restic") {
		return fmt.Errorf("restic is not installed (Settings → Components)")
	}
	if t.DestType == "sftp" {
		if _, err := PublicKey(); err != nil {
			return err
		}
	}
	if _, err := restic(ctx, t, nil, "cat", "config"); err == nil {
		return nil
	}
	if t.DestType == "local" {
		_ = os.MkdirAll(t.DestPath, 0o700)
	}
	p.Log("Initialising the restic repository")
	_, err := restic(ctx, t, nil, "init")
	return err
}

func runRestic(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if err := ensureRepo(ctx, t, p); err != nil {
		return "", err
	}
	_ = os.MkdirAll("/var/cache/strata/restic", 0o700)
	var summary struct {
		FilesNew, FilesChanged, FilesUnmodified int
		DataAdded                               int64  `json:"data_added"`
		SnapshotID                              string `json:"snapshot_id"`
	}
	lw := &lineWriter{fn: func(l string) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			if strings.TrimSpace(l) != "" {
				p.Log("%s", l)
			}
			return
		}
		switch m["message_type"] {
		case "status":
			if pd, ok := m["percent_done"].(float64); ok {
				p.Set(pd * 90)
			}
		case "summary":
			_ = json.Unmarshal([]byte(l), &summary)
			if v, ok := m["files_new"].(float64); ok {
				summary.FilesNew = int(v)
			}
			if v, ok := m["files_changed"].(float64); ok {
				summary.FilesChanged = int(v)
			}
		case "error":
			p.Log("%v", m["error"])
		}
	}}
	args := []string{"backup", "--json", "--host", hostname(), "--tag", "strata-" + t.ID, "--exclude", ".recycle", "--exclude", ".snapshots"}
	for _, e := range t.Excludes {
		args = append(args, "--exclude", e)
	}
	args = append(args, "--")
	args = append(args, t.Sources...)
	if _, err := restic(ctx, t, lw, args...); err != nil {
		// Exit code 3: some files could not be read; the snapshot is still created.
		if ce, ok := err.(*sys.CmdError); !ok || ce.Code != 3 {
			return "", err
		}
		p.Log("warning: some files could not be read")
	}
	p.Set(92)
	forget := []string{"forget", "--prune", "--tag", "strata-" + t.ID, "--group-by", "host,tags"}
	for flag, n := range map[string]int{"--keep-last": t.KeepLast, "--keep-daily": t.KeepDaily, "--keep-weekly": t.KeepWeekly, "--keep-monthly": t.KeepMonthly} {
		if n > 0 {
			forget = append(forget, flag, strconv.Itoa(n))
		}
	}
	if t.AppendOnly {
		p.Log("Append-only vault: old snapshots are kept (deletions are refused by the vault)")
	} else {
		p.Log("Applying retention")
		if _, err := restic(ctx, t, nil, forget...); err != nil {
			p.Log("warning: retention failed: %v", err)
		}
	}
	return fmt.Sprintf("Snapshot %s: %d new, %d changed, %s added", short(summary.SnapshotID), summary.FilesNew, summary.FilesChanged, humanBytes(summary.DataAdded)), nil
}

type ResticSnapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Hostname string    `json:"hostname"`
	Summary  *struct {
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
		TotalFilesProcessed int   `json:"total_files_processed"`
	} `json:"summary,omitempty"`
}

// Snapshots lists the restic snapshots of a task, newest first.
func Snapshots(ctx context.Context, t store.BackupTask) ([]ResticSnapshot, error) {
	if t.Kind != "restic" {
		return nil, fmt.Errorf("only restic backups keep versions")
	}
	out, err := restic(ctx, &t, nil, "snapshots", "--json", "--tag", "strata-"+t.ID)
	if err != nil {
		return nil, err
	}
	var snaps []ResticSnapshot
	if err := json.Unmarshal([]byte(out), &snaps); err != nil {
		return nil, err
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	return snaps, nil
}

var snapIDRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// Restore extracts a restic snapshot (or one path of it) into target.
func Restore(ctx context.Context, t store.BackupTask, snapshot, include, target string, p Progress) (string, error) {
	if !snapIDRe.MatchString(snapshot) {
		return "", fmt.Errorf("invalid snapshot id")
	}
	target = filepath.Clean(target)
	if !strings.HasPrefix(target, storage.MountRoot+"/") {
		return "", fmt.Errorf("restore into a folder of a volume (/srv/strata/...)")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	args := []string{"restore", "--json", snapshot, "--target", target}
	if include != "" {
		if strings.ContainsAny(include, "\n\r") || strings.HasPrefix(include, "-") {
			return "", fmt.Errorf("invalid path")
		}
		args = append(args, "--include", include)
	}
	lw := &lineWriter{fn: func(l string) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			if pd, ok := m["percent_done"].(float64); ok {
				p.Set(pd * 100)
			}
		}
	}}
	if _, err := restic(ctx, &t, lw, args...); err != nil {
		return "", err
	}
	return "Restore finished in " + target, nil
}

// ---- ZFS replication ----

func replPrefix(t *store.BackupTask) string { return "strata-repl-" + t.ID + "-" }

// remoteZFS runs a zfs command on the replication target (locally or over SSH).
func remoteZFS(ctx context.Context, t *store.BackupTask, args ...string) (string, error) {
	if t.DestType == "local" {
		return sys.RunCtx(ctx, "zfs", args...)
	}
	return sshZFS(ctx, t, args...)
}

func runZFS(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if !storage.ZFSAvailable() {
		return "", fmt.Errorf("ZFS is not installed")
	}
	if t.DestType == "ssh" {
		if _, err := PublicKey(); err != nil {
			return "", err
		}
	}
	src := t.Sources[0]
	snap := replPrefix(t) + time.Now().Format("20060102-150405")
	p.Log("Snapshot %s@%s", src, snap)
	if err := storage.Snapshot(src, snap, t.Recursive); err != nil {
		return "", err
	}
	// Find the newest replication snapshot that already exists on the destination.
	mine, err := ownSnapshots(src, t)
	if err != nil {
		return "", err
	}
	destSnaps := map[string]bool{}
	if out, err := remoteZFS(ctx, t, "list", "-H", "-t", "snapshot", "-o", "name", "-d", "1", t.DestPath); err == nil {
		for _, l := range strings.Fields(out) {
			if _, s, ok := strings.Cut(l, "@"); ok {
				destSnaps[s] = true
			}
		}
	}
	base := ""
	for _, s := range mine { // newest first
		if s != snap && destSnaps[s] {
			base = s
			break
		}
	}
	sendArgs := []string{"send"}
	if t.Recursive {
		sendArgs = append(sendArgs, "-R")
	}
	if base != "" {
		sendArgs = append(sendArgs, "-I", src+"@"+base)
		p.Log("Incremental send from %s", base)
	} else {
		p.Log("Full send (first replication)")
		// Make sure the parent of the destination dataset exists.
		if parent := filepath.Dir(t.DestPath); parent != "." && parent != t.DestPath {
			_, _ = remoteZFS(ctx, t, "create", "-p", parent)
		}
	}
	sendArgs = append(sendArgs, src+"@"+snap)
	// The pipe runs in bash with every value passed as a positional parameter,
	// never interpolated into the script.
	n := strconv.Itoa(len(sendArgs))
	if t.DestType == "local" {
		script := `set -o pipefail; n=$1; shift; zfs "${@:1:$n}" | zfs recv -F -u "${@: -1}"`
		args := append(append([]string{n}, sendArgs...), t.DestPath)
		return finishZFS(ctx, t, src, snap, mine, script, args, "", p)
	}
	// Over SSH the receive command is interpreted by the remote shell: quote it.
	recv := []string{"zfs", "recv", "-F", "-u", t.DestPath}
	for i, a := range recv {
		recv[i] = shellQuote(a)
	}
	script := `set -o pipefail; n=$1; shift; ssh_opts=$1; shift; remote=$1; shift; zfs "$@" | ssh $ssh_opts "$remote" "$REMOTE_RECV"`
	args := append([]string{n, strings.Join(sshArgs(t), " "), t.User + "@" + t.Host}, sendArgs...)
	return finishZFS(ctx, t, src, snap, mine, script, args, "REMOTE_RECV="+strings.Join(recv, " "), p)
}

func finishZFS(ctx context.Context, t *store.BackupTask, src, snap string, mine []string, script string, args []string, env string, p Progress) (string, error) {
	o := sys.Opts{Timeout: 48 * time.Hour, Output: &lineWriter{fn: func(l string) { p.Log("%s", l) }}}
	if env != "" {
		o.Env = []string{env}
	}
	if _, err := sys.RunOpts(ctx, o, "bash", append([]string{"-c", script, "strata-zfs-repl"}, args...)...); err != nil {
		// Leave the new snapshot out of the chain: it was never received.
		_ = storage.DestroyDataset(src+"@"+snap, t.Recursive)
		return "", err
	}
	// Retention: keep the newest KeepLast replication snapshots on both sides.
	all := append([]string{snap}, mine...)
	seen := map[string]bool{}
	var ordered []string
	for _, s := range all {
		if !seen[s] {
			seen[s] = true
			ordered = append(ordered, s)
		}
	}
	removed := 0
	for i, s := range ordered {
		if i < t.KeepLast {
			continue
		}
		if storage.DestroyDataset(src+"@"+s, t.Recursive) == nil {
			removed++
		}
		args := []string{"destroy"}
		if t.Recursive {
			args = append(args, "-r")
		}
		_, _ = remoteZFS(ctx, t, append(args, t.DestPath+"@"+s)...)
	}
	where := t.DestPath
	if t.DestType == "ssh" {
		where = t.Host + ":" + t.DestPath
	}
	return fmt.Sprintf("Replication of %s to %s finished (%s), %d old point(s) deleted", src, where, snap, removed), nil
}

// ownSnapshots returns this task's replication snapshots of src, newest first.
func ownSnapshots(src string, t *store.BackupTask) ([]string, error) {
	snaps, err := storage.SnapshotsOf(src)
	if err != nil {
		return nil, err
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].Creation.After(snaps[j].Creation) || (snaps[i].Creation.Equal(snaps[j].Creation) && snaps[i].Name > snaps[j].Name)
	})
	var out []string
	for _, s := range snaps {
		if _, name, ok := strings.Cut(s.Name, "@"); ok && strings.HasPrefix(name, replPrefix(t)) {
			out = append(out, name)
		}
	}
	return out, nil
}

// Test checks that the destination is reachable with the stored settings.
func Test(ctx context.Context, t store.BackupTask) (string, error) {
	switch {
	case t.Kind == "sync":
		return testSync(ctx, &t)
	case IsPull(&t) && t.Kind == "zfs":
		return testZFSPull(ctx, &t)
	case IsPull(&t):
		return testPull(ctx, &t)
	case t.Kind == "restic":
		if err := ensureRepo(ctx, &t, Progress{Set: func(float64) {}, Log: func(string, ...any) {}}); err != nil {
			return "", err
		}
		return "restic repository reachable", nil
	case t.DestType == "ssh":
		if _, err := PublicKey(); err != nil {
			return "", err
		}
		cmd := "echo ok"
		if t.Kind == "zfs" {
			cmd = "zfs list -H -o name >/dev/null && echo ok"
		}
		out, err := sys.RunCtx(ctx, "ssh", append(sshArgs(&t), t.User+"@"+t.Host, cmd)...)
		if err != nil {
			return "", fmt.Errorf("SSH connection failed (is the StrataOS public key in ~/.ssh/authorized_keys of %s?): %w", t.User, err)
		}
		return "SSH connection successful: " + strings.TrimSpace(out), nil
	case t.Kind == "zfs":
		if _, err := sys.RunCtx(ctx, "zfs", "list", "-H", "-o", "name", strings.SplitN(t.DestPath, "/", 2)[0]); err != nil {
			return "", fmt.Errorf("destination pool not found")
		}
		return "Pool de destination disponible", nil
	default:
		return "Destination locale valide", nil
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
