package backup

// Reading a restic repository for the guided restore: listing a snapshot,
// extracting chosen paths, streaming one file or a folder as a zip.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"strataos/internal/protect"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// MaxListing bounds a recursive listing (previews, restore tests).
const MaxListing = 200000

func validSnapPath(snapshot, p string) error {
	if !snapIDRe.MatchString(snapshot) {
		return fmt.Errorf("invalid snapshot id")
	}
	if p != "" && (!strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\n\r") || path.Clean(p) != p) {
		return fmt.Errorf("invalid path")
	}
	return nil
}

// withRepo runs fn with the repository settings restic understands (a
// vault is reached through its local proxy).
func withRepo(t store.BackupTask, fn func(t *store.BackupTask) error) error {
	if t.Kind != "restic" {
		return fmt.Errorf("not a versioned backup")
	}
	if t.DestType == "strataos" {
		p, err := startVaultProxy(&t)
		if err != nil {
			return err
		}
		defer p.close()
		t.DestType, t.DestPath = "rest", p.url
	}
	return fn(&t)
}

// ResticList lists dir in a snapshot: its direct children, or everything
// below it when recursive (at most MaxListing entries, truncated is then set).
func ResticList(ctx context.Context, t store.BackupTask, snapshot, dir string, recursive bool) (entries []protect.Entry, truncated bool, err error) {
	if err := validSnapPath(snapshot, dir); err != nil {
		return nil, false, err
	}
	args := []string{"ls", "--json", snapshot}
	if recursive {
		args = append(args, "--recursive")
	}
	if dir != "" {
		args = append(args, "--", dir)
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := withRepo(t, func(t *store.BackupTask) error {
			return sys.Stream(ctx, sys.Opts{Env: resticEnv(t), Timeout: 30 * time.Minute}, pw, "restic", resticArgs(t, args...)...)
		})
		pw.CloseWithError(err)
		done <- err
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var n struct {
			Type       string    `json:"type"`
			Path       string    `json:"path"`
			Name       string    `json:"name"`
			Size       int64     `json:"size"`
			MTime      time.Time `json:"mtime"`
			StructType string    `json:"struct_type"`
			Message    string    `json:"message_type"`
		}
		if json.Unmarshal(sc.Bytes(), &n) != nil || n.StructType == "snapshot" || n.Message == "snapshot" || n.Path == "" {
			continue
		}
		if n.Path == dir {
			continue // the folder itself
		}
		if len(entries) >= MaxListing {
			truncated = true
			continue // keep reading: restic must not block on a full pipe
		}
		entries = append(entries, protect.Entry{Path: n.Path, Name: n.Name, Dir: n.Type == "dir", Link: n.Type == "symlink", Size: n.Size, MTime: n.MTime})
	}
	if err := <-done; err != nil {
		return nil, false, err
	}
	return entries, truncated, nil
}

// ResticExtract restores paths of a snapshot under target, keeping their
// absolute path (target/srv/strata/...). verify re-reads what was written.
func ResticExtract(ctx context.Context, t store.BackupTask, snapshot string, paths []string, target string, verify bool, p Progress) error {
	if err := validSnapPath(snapshot, ""); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	args := []string{"restore", "--json", snapshot, "--target", target}
	if verify {
		args = append(args, "--verify")
	}
	for _, x := range paths {
		if err := validSnapPath(snapshot, x); err != nil {
			return err
		}
		args = append(args, "--include", x)
	}
	lw := &lineWriter{fn: func(l string) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			if strings.TrimSpace(l) != "" {
				p.Log("%s", l)
			}
			return
		}
		if pd, ok := m["percent_done"].(float64); ok && p.Set != nil {
			p.Set(pd * 100)
		}
		if m["message_type"] == "error" {
			p.Log("%v", m["error"])
		}
	}}
	return withRepo(t, func(t *store.BackupTask) error {
		_, err := sys.RunOpts(ctx, sys.Opts{Env: resticEnv(t), Timeout: 48 * time.Hour, Output: lw}, "restic", resticArgs(t, args...)...)
		return err
	})
}

// ResticDump writes one file of a snapshot to w, or a folder as a zip.
func ResticDump(ctx context.Context, t store.BackupTask, snapshot, p string, zip bool, w io.Writer) error {
	if err := validSnapPath(snapshot, p); err != nil || p == "" {
		return firstErr(err, fmt.Errorf("invalid path"))
	}
	args := []string{"dump"}
	if zip {
		args = append(args, "--archive", "zip")
	}
	args = append(args, snapshot, p)
	return withRepo(t, func(t *store.BackupTask) error {
		return sys.Stream(ctx, sys.Opts{Env: resticEnv(t), Timeout: 12 * time.Hour}, w, "restic", resticArgs(t, args...)...)
	})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
