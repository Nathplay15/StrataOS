package backup

// ZFS pull replication: this NAS asks a remote host to snapshot a dataset and
// send it (incrementally) over SSH, and receives it into a local dataset. The
// source holds no credentials for this NAS: compromised, it cannot reach or
// delete its backups.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

func validateZFSPull(t *store.BackupTask) error {
	if t.SourceType != "ssh" {
		return fmt.Errorf("ZFS replication pulls over SSH only")
	}
	if len(t.Sources) != 1 || storage.ValidDS(t.Sources[0]) != nil || strings.Contains(t.Sources[0], "@") {
		return fmt.Errorf("enter the remote ZFS dataset to replicate (pool/dataset)")
	}
	if storage.ValidDS(t.DestPath) != nil || strings.Contains(t.DestPath, "@") {
		return fmt.Errorf("the destination must be a ZFS dataset name (pool/backup/...)")
	}
	if err := validRemote(t, true); err != nil {
		return err
	}
	t.DestType, t.RemotePassword, t.Domain = "local", "", ""
	if t.KeepLast <= 0 {
		t.KeepLast = 7
	}
	return nil
}

func pullPrefix(t *store.BackupTask) string { return "strata-pull-" + t.ID + "-" }

// remotePath finds zfs in sbin for accounts other than root, and on FreeBSD.
const remotePath = `PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin" `

// remoteCommand quotes a command for the remote shell.
func remoteCommand(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

func sshZFS(ctx context.Context, t *store.BackupTask, args ...string) (string, error) {
	full := append(sshArgs(t), t.User+"@"+t.Host, remotePath+remoteCommand(append([]string{"zfs"}, args...)...))
	return sys.RunCtx(ctx, "ssh", full...)
}

// zfsPullSend is the remote send command (shell-quoted).
func zfsPullSend(t *store.BackupTask, base, snap string) string {
	args := []string{"zfs", "send"}
	if t.Recursive {
		args = append(args, "-R")
	}
	src := t.Sources[0]
	if base != "" {
		args = append(args, "-I", src+"@"+base)
	}
	return remotePath + remoteCommand(append(args, src+"@"+snap)...)
}

// ownNames keeps the snapshot names of a "zfs list -o name" output that
// carry prefix, oldest first (names end with a sortable time stamp).
func ownNames(list, prefix string) []string {
	var out []string
	for _, l := range strings.Fields(list) {
		if _, s, ok := strings.Cut(l, "@"); ok && strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func runZFSPull(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if !storage.ZFSAvailable() {
		return "", fmt.Errorf("ZFS is not installed")
	}
	if _, err := PublicKey(); err != nil {
		return "", err
	}
	src, dest := t.Sources[0], t.DestPath
	snap := pullPrefix(t) + time.Now().Format("20060102-150405")
	remoteList := func() ([]string, error) {
		out, err := sshZFS(ctx, t, "list", "-H", "-t", "snapshot", "-o", "name", "-d", "1", src)
		return ownNames(out, pullPrefix(t)), err
	}
	before, err := remoteList()
	if err != nil {
		return "", fmt.Errorf("cannot list the snapshots of %s on %s: %w", src, t.Host, err)
	}
	local := map[string]bool{}
	if out, err := sys.RunCtx(ctx, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-d", "1", dest); err == nil {
		for _, s := range ownNames(out, pullPrefix(t)) {
			local[s] = true
		}
	}
	base := ""
	for i := len(before) - 1; i >= 0; i-- {
		if local[before[i]] {
			base = before[i]
			break
		}
	}
	p.Log("Snapshot %s@%s on %s", src, snap, t.Host)
	snapArgs := []string{"snapshot"}
	if t.Recursive {
		snapArgs = append(snapArgs, "-r")
	}
	if _, err := sshZFS(ctx, t, append(snapArgs, src+"@"+snap)...); err != nil {
		return "", fmt.Errorf("the remote account cannot snapshot %s (it needs zfs allow snapshot,send,hold,destroy,mount): %w", src, err)
	}
	if base != "" {
		p.Log("Incremental receive from %s", base)
	} else {
		p.Log("Full receive (first replication)")
		if parent := filepath.Dir(dest); parent != "." && parent != dest && strings.Contains(dest, "/") {
			_, _ = sys.RunCtx(ctx, "zfs", "create", "-p", parent)
		}
	}
	// Every value reaches bash as a positional parameter; the remote command
	// is quoted for the remote shell.
	script := `set -o pipefail; ssh_opts=$1; remote=$2; dest=$3; ssh $ssh_opts "$remote" "$REMOTE_SEND" | zfs recv -F -u "$dest"`
	o := sys.Opts{Timeout: 48 * time.Hour, Env: []string{"REMOTE_SEND=" + zfsPullSend(t, base, snap)}, Output: &lineWriter{fn: func(l string) { p.Log("%s", l) }}}
	if _, err := sys.RunOpts(ctx, o, "bash", "-c", script, "strata-zfs-pull", strings.Join(sshArgs(t), " "), t.User+"@"+t.Host, dest); err != nil {
		// Leave the new snapshot out of the chain: it was never received.
		args := []string{"destroy"}
		if t.Recursive {
			args = append(args, "-r")
		}
		_, _ = sshZFS(ctx, t, append(args, src+"@"+snap)...)
		return "", err
	}
	// Retention: the newest KeepLast replication points, on both sides.
	removed := 0
	all := append(before, snap)
	for i, s := range all {
		if i >= len(all)-t.KeepLast {
			break
		}
		if storage.DestroyDataset(dest+"@"+s, t.Recursive) == nil {
			removed++
		}
		args := []string{"destroy"}
		if t.Recursive {
			args = append(args, "-r")
		}
		if _, err := sshZFS(ctx, t, append(args, src+"@"+s)...); err != nil {
			p.Log("warning: %s@%s could not be deleted on %s: %v", src, s, t.Host, err)
		}
	}
	return fmt.Sprintf("Replication of %s:%s to %s finished (%s), %d old point(s) deleted", t.Host, src, dest, snap, removed), nil
}

func testZFSPull(ctx context.Context, t *store.BackupTask) (string, error) {
	if _, err := PublicKey(); err != nil {
		return "", err
	}
	out, err := sshZFS(ctx, t, "list", "-H", "-o", "name,used", t.Sources[0])
	if err != nil {
		return "", fmt.Errorf("SSH connection or dataset failed (is the StrataOS public key in ~/.ssh/authorized_keys of %s?): %w", t.User, err)
	}
	f := strings.Fields(out)
	used := ""
	if len(f) > 1 {
		used = f[1]
	}
	return fmt.Sprintf("Connection successful: dataset %s found (%s)", t.Sources[0], used), nil
}
