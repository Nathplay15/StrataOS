package backup

// Two-way sync between a folder of this NAS and a folder of another one,
// through rclone bisync over SFTP with the StrataOS SSH key. Changes, and
// deletions, go both ways; a snapshot of the local side before each run
// (Btrfs/ZFS) lets a mistaken deletion be recovered.

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

	"strataos/internal/store"
	"strataos/internal/sys"
)

// SyncDir holds the rclone configuration and bisync state of two-way syncs.
var SyncDir = "/var/lib/strata/backup-sync"

func validateSync(t *store.BackupTask) error {
	if err := validExtras(t); err != nil {
		return err
	}
	if len(t.Sources) != 1 {
		return fmt.Errorf("choose one folder to synchronise")
	}
	src, err := localDest(t.Sources[0])
	if err != nil {
		return fmt.Errorf("the local folder must be inside a volume (/srv/strata/...)")
	}
	t.Sources[0] = src
	if err := validRemote(t, true); err != nil {
		return err
	}
	c := path.Clean(t.DestPath)
	if !strings.HasPrefix(c, "/") || c == "/" || strings.ContainsAny(c, "\n\r\x00") {
		return fmt.Errorf("the remote folder must be an absolute path, e.g. /srv/strata/main/Shared")
	}
	t.DestPath, t.DestType, t.SourceType, t.RemotePassword, t.Domain, t.Delete = c, "sftp", "", "", "", false
	if t.KeepLast == 0 && t.KeepDays == 0 {
		t.KeepLast = 7
	}
	return nil
}

func syncConf(id string) string { return filepath.Join(SyncDir, id+".conf") }
func syncWork(id string) string { return filepath.Join(SyncDir, "bisync-"+id) }

// rcloneConfig is the SFTP remote of a two-way sync (key authentication only).
func rcloneConfig(t *store.BackupTask) string {
	return fmt.Sprintf("[strata]\ntype = sftp\nhost = %s\nuser = %s\nport = %s\nkey_file = %s\nknown_hosts_file = %s\nshell_type = none\n",
		t.Host, t.User, portOr(t.Port, 22), keyPath(), knownHostsPath())
}

// bisyncArgs builds the rclone command line. resync initialises the sync:
// both sides are merged, nothing is deleted.
func bisyncArgs(t *store.BackupTask, conf string, resync bool) []string {
	args := []string{"bisync", t.Sources[0], "strata:" + t.DestPath, "--config", conf, "--workdir", syncWork(t.ID),
		"--stats", "2s", "--stats-one-line", "--stats-log-level", "NOTICE", "-v", "--retries", "3"}
	if resync {
		args = append(args, "--resync")
	}
	if t.BWLimit != "" {
		args = append(args, "--bwlimit", t.BWLimit)
	}
	for _, e := range t.Excludes {
		args = append(args, "--exclude", e)
	}
	return append(args, "--exclude", ".recycle/**", "--exclude", "#recycle/**", "--exclude", ".snapshots/**", "--exclude", ".DS_Store", "--exclude", "@eaDir/**")
}

func writeSyncConf(ctx context.Context, t *store.BackupTask) (string, error) {
	if !sys.Has("rclone") {
		return "", fmt.Errorf("rclone is not installed (Settings → Components)")
	}
	if _, err := PublicKey(); err != nil {
		return "", err
	}
	if _, err := KnownHost(ctx, t.Host, portOr(t.Port, 22)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(SyncDir, 0o700); err != nil {
		return "", err
	}
	id := t.ID
	if id == "" {
		id = "test"
	}
	conf := syncConf(id)
	return conf, sys.WriteFileAtomic(conf, []byte(rcloneConfig(t)), 0o600)
}

var (
	syncPctRe   = regexp.MustCompile(`, ([0-9]{1,3})%`)
	syncLevelRe = regexp.MustCompile(`^(<[0-9]>)?(DEBUG|INFO|NOTICE|ERROR|CRITICAL)? *: *`)
)

func runSync(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if fi, err := os.Stat(t.Sources[0]); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s does not exist", t.Sources[0])
	}
	conf, err := writeSyncConf(ctx, t)
	if err != nil {
		return "", err
	}
	// Recovery point before anything is changed or deleted here.
	if snap, err := snapshotFolder(t.Sources[0], "sync-"+t.ID+"-", t.KeepLast, t.KeepDays, p); err != nil {
		return "", fmt.Errorf("snapshot before the sync failed: %w", err)
	} else if snap != "" {
		p.Log("%s", snap)
	}
	resync := !t.Synced
	if resync {
		p.Log("First sync: both sides are merged (nothing is deleted)")
		if err := os.RemoveAll(syncWork(t.ID)); err != nil {
			return "", err
		}
	}
	var transferred, last, firstErr string
	out := &lineWriter{fn: func(l string) {
		l = strings.TrimSpace(l)
		if l == "" {
			return
		}
		isErr := strings.Contains(l, "ERROR")
		l = syncLevelRe.ReplaceAllString(l, "")
		if m := syncPctRe.FindStringSubmatch(l); m != nil && strings.Contains(l, "ETA") {
			if n, err := strconv.Atoi(m[1]); err == nil {
				p.Set(float64(n))
			}
			transferred = l
			return
		}
		if isErr && firstErr == "" && !strings.HasPrefix(l, "Attempt ") {
			firstErr = l
		}
		last = l
		p.Log("%s", l)
	}}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 48 * time.Hour, Output: out}, "rclone", bisyncArgs(t, conf, resync)...); err != nil {
		if strings.Contains(firstErr+last, "--resync") {
			return "", fmt.Errorf("the two-way sync must be initialised again: use Resync (%s)", last)
		}
		if firstErr != "" {
			return "", fmt.Errorf("%s", firstErr)
		}
		return "", err
	}
	p.Set(100)
	if transferred == "" {
		return "Synced (nothing to transfer)", nil
	}
	return "Synced: " + transferred, nil
}

func testSync(ctx context.Context, t *store.BackupTask) (string, error) {
	conf, err := writeSyncConf(ctx, t)
	if err != nil {
		return "", err
	}
	if t.ID == "" {
		defer os.Remove(conf)
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 45 * time.Second}, "rclone", "--config", conf, "lsf", "--max-depth", "1", "strata:"+t.DestPath)
	if err != nil {
		if strings.Contains(err.Error(), "directory not found") {
			return "Connection successful (the remote folder will be created)", nil
		}
		return "", fmt.Errorf("SFTP connection failed (is the StrataOS public key in ~/.ssh/authorized_keys of %s?): %w", t.User, err)
	}
	return fmt.Sprintf("Connection successful (%d item(s) in the remote folder)", countLines(out)), nil
}

// RemoveSync deletes the files StrataOS keeps for a two-way sync.
func RemoveSync(id string) {
	_ = os.Remove(syncConf(id))
	_ = os.RemoveAll(syncWork(id))
}
