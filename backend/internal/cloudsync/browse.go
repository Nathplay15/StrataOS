package cloudsync

// Reading the cloud copy of an upload task for the guided restore and the
// restore tests. Paths are relative to the task folder (LocalPath on the
// NAS, RemotePath in the cloud).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Item is a file or folder of the cloud copy.
type Item struct {
	Path    string    `json:"Path"` // relative to the folder listed
	Name    string    `json:"Name"`
	Size    int64     `json:"Size"`
	ModTime time.Time `json:"ModTime"`
	IsDir   bool      `json:"IsDir"`
}

func validRel(rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	if strings.ContainsAny(rel, "\n\r") || strings.HasPrefix(rel, "/") || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("invalid path")
	}
	return nil
}

func remotePath(t *store.CloudSync, rel string) string {
	if rel == "" || rel == "." {
		return target(t)
	}
	return remote + ":" + strings.TrimRight(t.RemotePath, "/") + "/" + rel
}

// List lists the folder rel of the cloud copy (everything below it when
// recursive). Paths returned are relative to rel.
func List(ctx context.Context, t store.CloudSync, rel string, recursive bool) ([]Item, error) {
	if !Installed() {
		return nil, fmt.Errorf("rclone is not installed")
	}
	if err := validRel(rel); err != nil {
		return nil, err
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return nil, err
	}
	args := []string{"--config", conf, "lsjson", "--no-mimetype", "--exclude", ".recycle/**", "--exclude", "#recycle/**"}
	if recursive {
		args = append(args, "-R")
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 30 * time.Minute}, "rclone", append(args, remotePath(&t, rel))...)
	if err != nil {
		if strings.Contains(err.Error(), "directory not found") {
			return []Item{}, nil
		}
		return nil, fmt.Errorf("%s", lastLine(err.Error()))
	}
	var items []Item
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Fetch copies files or folders of the cloud copy (relative paths) to dest,
// keeping their relative path.
func Fetch(ctx context.Context, t store.CloudSync, rels []string, dirs []bool, dest string, p Progress) error {
	if !Installed() {
		return fmt.Errorf("rclone is not installed")
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return err
	}
	var files []string
	for i, rel := range rels {
		if err := validRel(rel); err != nil {
			return err
		}
		if i < len(dirs) && dirs[i] {
			to := filepath.Join(dest, filepath.FromSlash(rel))
			p.Log("rclone copy %s", rel)
			if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 48 * time.Hour}, "rclone", "--config", conf, "copy", remotePath(&t, rel), to); err != nil {
				return fmt.Errorf("%s", lastLine(err.Error()))
			}
			continue
		}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return nil
	}
	list, err := os.CreateTemp("", "strata-files-")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())
	_, err = io.WriteString(list, strings.Join(files, "\n")+"\n")
	if cerr := list.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 48 * time.Hour}, "rclone", "--config", conf, "copy", "--files-from-raw", list.Name(), "--no-traverse", target(&t), dest); err != nil {
		return fmt.Errorf("%s", lastLine(err.Error()))
	}
	return nil
}

// Cat writes one file of the cloud copy to w.
func Cat(ctx context.Context, t store.CloudSync, rel string, w io.Writer) error {
	if err := validRel(rel); err != nil || rel == "" {
		return fmt.Errorf("invalid path")
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return err
	}
	return sys.Stream(ctx, sys.Opts{Timeout: 12 * time.Hour}, w, "rclone", "--config", conf, "cat", remotePath(&t, rel))
}
