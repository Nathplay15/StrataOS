package system

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"strataos/internal/sys"
)

// ArchiveDir writes a gzip tarball of the regular files and folders of dir
// (the firmware partition is FAT: nothing else lives there).
func ArchiveDir(dir, dest string) (err error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dest+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(dest + ".part")
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			h.Name += "/"
		}
		h.Uname, h.Gname, h.Uid, h.Gid = "", "", 0, 0
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(dest+".part", dest)
}

// RestoreDir empties dir (a mount point: the folder itself stays) and puts
// back the content of a tarball written by ArchiveDir.
func RestoreDir(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	// Read it whole once before deleting anything: a damaged archive must
	// not leave an empty partition.
	if err := walkTar(f, func(*tar.Header, io.Reader) error { return nil }); err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	err = walkTar(f, func(h *tar.Header, r io.Reader) error {
		p := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(h.Name, "/")))
		if h.Typeflag == tar.TypeDir {
			return os.MkdirAll(p, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		return err
	}
	syncFS(dir)
	return nil
}

// walkTar calls fn for each folder and regular file of a gzip tarball,
// refusing names that leave the archive's root.
func walkTar(r io.Reader, fn func(*tar.Header, io.Reader) error) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("unexpected name %q", h.Name)
		}
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg {
			continue
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

// CopyFile copies a regular file (FAT keeps no owner nor mode).
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// syncFS flushes the file system holding dir (FAT has no journal: the
// firmware must find complete files after a power cut).
func syncFS(dir string) { _, _ = sys.Run("sync", "-f", dir) }

// FreeBytes returns the space left on the file system holding path.
func FreeBytes(path string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// DirSize returns the bytes used by the regular files under dir.
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}
