package restore

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"strataos/internal/protect"
)

// Conflict policies of a restore, for files that exist and differ.
const (
	PolicyOverwrite = "overwrite" // the restored file replaces it
	PolicySkip      = "skip"      // the current file stays
	PolicyRename    = "rename"    // the restored file goes next to it, renamed
)

// Plan is a restore to run.
type Plan struct {
	Items  []protect.Entry // selected files and folders (original paths)
	DstDir string          // root of the destination (a volume folder, or a scratch folder)
	// Dest maps an original path to its destination (absolute, under DstDir).
	Dest   func(abs string) string
	Policy string
}

// Result counts what a restore did.
type Result struct {
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Same    int      `json:"same"`
	Skipped int      `json:"skipped"`
	Renamed int      `json:"renamed"`
	Errors  int      `json:"errors"`
	First   []string `json:"firstErrors,omitempty"`
}

func (r *Result) fail(format string, a ...any) {
	r.Errors++
	if len(r.First) < 10 {
		r.First = append(r.First, fmt.Sprintf(format, a...))
	}
}

// Summary is a line for the job log and the history.
func (r Result) Summary() string {
	s := fmt.Sprintf("%d file(s) restored (%s)", r.Files, human(r.Bytes))
	if r.Same > 0 {
		s += fmt.Sprintf(", %d already identical", r.Same)
	}
	if r.Skipped > 0 {
		s += fmt.Sprintf(", %d kept as they were", r.Skipped)
	}
	if r.Renamed > 0 {
		s += fmt.Sprintf(", %d restored under another name", r.Renamed)
	}
	if r.Errors > 0 {
		s += fmt.Sprintf(", %d error(s)", r.Errors)
	}
	return s
}

func human(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

type dirMeta struct {
	rel string
	fi  fs.FileInfo
}

// Apply copies the selection from where loc says it is to the destination.
func Apply(ctx context.Context, loc Locator, plan Plan, p Progress) (Result, error) {
	var res Result
	dst, err := os.OpenRoot(plan.DstDir)
	if err != nil {
		return res, err
	}
	defer dst.Close()
	// First pass: sizes, for the progress.
	var total int64
	for _, it := range plan.Items {
		t, ok := loc(it.Path)
		if !ok {
			return res, errOutside
		}
		if it.Dir {
			list, _, _ := t.list(ctx, it.Path, true, 1<<30)
			for _, e := range list {
				total += e.Size
			}
		} else {
			total += it.Size
		}
	}
	var done int64
	now := time.Now()
	var dirs []dirMeta
	for _, it := range plan.Items {
		t, _ := loc(it.Path)
		rel, ok := t.rel(it.Path)
		if !ok {
			return res, errOutside
		}
		src, err := os.OpenRoot(t.Root)
		if err != nil {
			return res, err
		}
		err = fs.WalkDir(src.FS(), rel, func(sp string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				res.fail("%s: %v", sp, err)
				return nil
			}
			if sp != rel && skipName(d.Name()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			sub := strings.TrimPrefix(strings.TrimPrefix(sp, rel), "/")
			if rel == "." {
				sub = sp
				if sp == "." {
					sub = ""
				}
			}
			abs := it.Path
			if sub != "" {
				abs = path.Join(it.Path, sub)
			}
			to, ok := strings.CutPrefix(plan.Dest(abs), plan.DstDir+"/")
			if !ok {
				return fmt.Errorf("%s: destination outside %s", abs, plan.DstDir)
			}
			fi, err := src.Lstat(sp)
			if err != nil {
				res.fail("%s: %v", abs, err)
				return nil
			}
			switch {
			case fi.IsDir():
				cur, err := dst.Lstat(to)
				if err == nil && !cur.IsDir() {
					res.fail("%s: a file is in the way of the folder", to)
					return fs.SkipDir
				}
				if err != nil {
					if err := dst.MkdirAll(to, 0o755); err != nil {
						res.fail("%s: %v", to, err)
						return fs.SkipDir
					}
					dirs = append(dirs, dirMeta{to, fi})
				}
			case fi.Mode().IsRegular():
				n, err := restoreFile(src, sp, fi, dst, to, plan.Policy, now, &res)
				done += fi.Size()
				if err != nil {
					res.fail("%s: %v", to, err)
				} else {
					res.Bytes += n
				}
				if total > 0 && p.Set != nil {
					p.Set(float64(done) * 100 / float64(total))
				}
			case fi.Mode()&fs.ModeSymlink != 0:
				if err := restoreLink(src, sp, dst, to, plan.Policy); err != nil {
					res.fail("%s: %v", to, err)
				}
			}
			return nil
		})
		src.Close()
		if err != nil {
			return res, err
		}
	}
	// Folders made by the restore get their owner, rights and date last
	// (writing into them changed the date), deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		setMeta(dst, dirs[i].rel, dirs[i].fi)
	}
	return res, nil
}

func setMeta(dst *os.Root, rel string, fi fs.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = dst.Lchown(rel, int(st.Uid), int(st.Gid))
	}
	_ = dst.Chmod(rel, fi.Mode().Perm()|fi.Mode()&(fs.ModeSetgid|fs.ModeSetuid|fs.ModeSticky))
	_ = dst.Chtimes(rel, fi.ModTime(), fi.ModTime())
}

// restoreFile copies one file, following the policy when one is there.
func restoreFile(src *os.Root, sp string, fi fs.FileInfo, dst *os.Root, to, policy string, now time.Time, res *Result) (int64, error) {
	if cur, err := dst.Lstat(to); err == nil {
		e := entryOf(to, fi)
		if protect.Action(e, entryOf(to, cur), true) == protect.ActSame {
			res.Same++
			return 0, nil
		}
		if cur.IsDir() {
			return 0, fmt.Errorf("a folder is in the way of the file")
		}
		switch policy {
		case PolicySkip:
			res.Skipped++
			return 0, nil
		case PolicyRename:
			to = protect.RenamedPath(to, now)
			res.Renamed++
		}
	}
	in, err := src.Open(sp)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := dst.MkdirAll(path.Dir(to), 0o755); err != nil {
		return 0, err
	}
	tmp := path.Join(path.Dir(to), fmt.Sprintf(".%s.strata-restore-%d", path.Base(to), time.Now().UnixNano()))
	out, err := dst.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		setMeta(dst, tmp, fi)
		err = dst.Rename(tmp, to)
	}
	if err != nil {
		_ = dst.Remove(tmp)
		return 0, err
	}
	res.Files++
	return n, nil
}

func restoreLink(src *os.Root, sp string, dst *os.Root, to, policy string) error {
	target, err := src.Readlink(sp)
	if err != nil {
		return err
	}
	if cur, err := dst.Readlink(to); err == nil {
		if cur == target || policy != PolicyOverwrite {
			return nil
		}
		if err := dst.Remove(to); err != nil {
			return err
		}
	} else if _, err := dst.Lstat(to); err == nil {
		return nil // a real file there: left alone
	}
	if err := dst.MkdirAll(path.Dir(to), 0o755); err != nil {
		return err
	}
	return dst.Symlink(target, to)
}

// Preview compares the selection with what is at the destination.
// listing is the recursive content of the selected folders.
func Preview(items []protect.Entry, listing []protect.Entry, dest func(string) string) protect.Preview {
	all := make([]protect.Entry, 0, len(items)+len(listing))
	for _, it := range items {
		if !it.Dir {
			all = append(all, it)
		}
	}
	all = append(all, listing...)
	pv := protect.Compare(all, dest, func(p string) (protect.Entry, bool) {
		fi, err := os.Lstat(p)
		if err != nil {
			return protect.Entry{}, false
		}
		return entryOf(p, fi), true
	})
	for _, e := range all {
		if !e.Dir && configFileName(e.Name) {
			pv.ConfigFile = dest(e.Path)
			break
		}
	}
	return pv
}

// configFileName recognises the exports of the configuration backup.
func configFileName(name string) bool {
	return strings.HasPrefix(name, "strataos-config-") && strings.HasSuffix(name, ".json")
}
