package storagetools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// usageSkipped are the folders the space of a share leaves out, wherever
// they are: its recycle bin and its snapshots.
var usageSkipped = map[string]bool{".recycle": true, ".snapshots": true}

type inode struct{ dev, ino uint64 }

// MeasureShares measures the shared folders of one file system together,
// at low priority, like du -sbx over each of them but with every file
// counted once: a file hard-linked into several shares counts in the first
// one (in path order) that holds it. A share nested in another is measured
// on its own and left out of its parent; the walk stays on the file system
// of each share (no mount, Btrfs sub-volume or snapshot below it), and skips
// the recycle bins and the snapshot folders. Sizes are apparent sizes of the
// files and folders. Entries that cannot be read are skipped. When ctx ends
// the shares not measured yet are missing from the answer.
func MeasureShares(ctx context.Context, paths []string) (map[string]int64, error) {
	out := map[string]int64{}
	var err error
	inBackground(func() { err = measureShares(ctx, paths, out) })
	return out, err
}

func measureShares(ctx context.Context, paths []string, out map[string]int64) error {
	paths = slices.Clone(paths)
	for i := range paths {
		paths[i] = filepath.Clean(paths[i])
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	roots := map[string]bool{}
	for _, p := range paths {
		roots[p] = true
	}
	seen := map[inode]bool{}
	for _, root := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fi, err := os.Lstat(root)
		if err != nil || !fi.IsDir() {
			continue
		}
		dev := devOf(fi)
		var total int64
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if d != nil && d.IsDir() && p != root {
					return fs.SkipDir
				}
				return nil
			}
			if p != root {
				if d.IsDir() && (roots[p] || usageSkipped[d.Name()]) {
					return fs.SkipDir
				}
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil
			}
			if d.IsDir() && uint64(st.Dev) != dev {
				return fs.SkipDir // another file system mounted below
			}
			if !d.IsDir() && st.Nlink > 1 {
				k := inode{uint64(st.Dev), uint64(st.Ino)}
				if seen[k] {
					return nil
				}
				seen[k] = true
			}
			total += info.Size()
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.SkipDir) {
			return walkErr
		}
		out[root] = total
	}
	return nil
}

func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}
