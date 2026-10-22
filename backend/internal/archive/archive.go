// Package archive lists and extracts archives for the file manager: ZIP and
// tar (plain, gzip, bzip2) natively; tar.xz, tar.zst, .xz and .zst through
// the xz/zstd tools; 7z and RAR through 7-Zip when it is installed. Entry
// names are checked before anything is written (no absolute paths, no ".."),
// links and special files are skipped, and the destination is an os.Root,
// so nothing can land outside the chosen folder. Limits stop zip bombs.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Entry is one member of an archive.
type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Packed  int64     `json:"packed,omitempty"`
	Dir     bool      `json:"dir"`
	ModTime time.Time `json:"modTime,omitzero"`
	// Skipped: link, device or unsafe name, never extracted.
	Skipped   string `json:"skipped,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
}

// Listing is the content of an archive.
type Listing struct {
	Format    string  `json:"format"`
	Entries   []Entry `json:"entries"`
	Files     int     `json:"files"`
	Total     int64   `json:"total"` // uncompressed bytes, as the archive declares them
	Truncated bool    `json:"truncated"`
}

// MaxListed bounds the entries returned by List.
const MaxListed = 20000

// Format returns the archive format of a file name, or "".
func Format(name string) string {
	n := strings.ToLower(name)
	for _, f := range []struct{ suffix, format string }{
		{".tar.gz", "tar.gz"}, {".tgz", "tar.gz"}, {".tar.bz2", "tar.bz2"}, {".tbz2", "tar.bz2"}, {".tbz", "tar.bz2"},
		{".tar.xz", "tar.xz"}, {".txz", "tar.xz"}, {".tar.zst", "tar.zst"}, {".tzst", "tar.zst"},
		{".tar", "tar"}, {".zip", "zip"}, {".jar", "zip"}, {".cbz", "zip"}, {".epub", "zip"},
		{".7z", "7z"}, {".rar", "rar"}, {".cbr", "rar"},
		{".gz", "gz"}, {".bz2", "bz2"}, {".xz", "xz"}, {".zst", "zst"},
	} {
		if strings.HasSuffix(n, f.suffix) && len(n) > len(f.suffix) {
			return f.format
		}
	}
	return ""
}

// NeedsSevenZip reports whether a format is read through 7-Zip.
func NeedsSevenZip(format string) bool { return format == "7z" || format == "rar" }

// SafeName cleans an entry name, refusing absolute paths, drive letters and
// any ".." component (also with Windows separators).
func SafeName(name string) (string, bool) {
	n := strings.ReplaceAll(name, "\\", "/")
	if n == "" || strings.ContainsRune(n, 0) || strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') {
		return "", false
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", false
		}
	}
	c := path.Clean(n)
	if c == "." || c == "" || strings.HasPrefix(c, "/") {
		return "", false
	}
	return c, true
}

// singleName is the file inside a compressed single file (report.txt.gz → report.txt).
func singleName(archiveName string) string {
	b := path.Base(strings.ReplaceAll(archiveName, "\\", "/"))
	if i := strings.LastIndexByte(b, '.'); i > 0 {
		b = b[:i]
	}
	return b
}

// reader walks the members of an archive in order; open returns the content
// of the current member.
type member struct {
	Entry
	open func() (io.ReadCloser, error)
}

// walk calls fn for each member of f until fn returns an error.
func walk(ctx context.Context, f *os.File, format, archiveName string, fn func(m member) error) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	switch format {
	case "zip":
		st, err := f.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return fmt.Errorf("not a valid ZIP archive: %w", err)
		}
		for _, zf := range zr.File {
			if err := ctx.Err(); err != nil {
				return err
			}
			mode := zf.Mode()
			e := Entry{Name: zf.Name, Size: int64(zf.UncompressedSize64), Packed: int64(zf.CompressedSize64), Dir: zf.FileInfo().IsDir(), ModTime: zf.Modified, Encrypted: zf.Flags&1 != 0}
			switch {
			case mode&fs.ModeSymlink != 0:
				e.Skipped = "link"
			case !e.Dir && !mode.IsRegular():
				e.Skipped = "special file"
			case e.Encrypted:
				e.Skipped = "encrypted"
			}
			if err := fn(member{Entry: e, open: zf.Open}); err != nil {
				return err
			}
		}
		return nil
	case "tar", "tar.gz", "tar.bz2", "tar.xz", "tar.zst":
		var r io.Reader = bufio.NewReaderSize(f, 1<<16)
		var closer func() error
		switch format {
		case "tar.gz":
			gz, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("not a valid gzip file: %w", err)
			}
			r = gz
		case "tar.bz2":
			r = bzip2.NewReader(r)
		case "tar.xz", "tar.zst":
			rc, wait, err := decompress(ctx, f, format)
			if err != nil {
				return err
			}
			r, closer = rc, wait
		}
		err := walkTar(ctx, r, fn)
		if closer != nil {
			if cerr := closer(); err == nil && cerr != nil && !errors.Is(cerr, errStopped) {
				err = cerr
			}
		}
		return err
	case "gz", "bz2", "xz", "zst":
		e := Entry{Name: singleName(archiveName), Size: -1}
		return fn(member{Entry: e, open: func() (io.ReadCloser, error) {
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			switch format {
			case "gz":
				return gzip.NewReader(bufio.NewReader(f))
			case "bz2":
				return io.NopCloser(bzip2.NewReader(bufio.NewReader(f))), nil
			}
			rc, wait, err := decompress(ctx, f, format)
			if err != nil {
				return nil, err
			}
			return readCloser{rc, wait}, nil
		}})
	case "7z", "rar":
		return walkSevenZip(ctx, f, fn)
	}
	return errors.New("unsupported archive format")
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }

func walkTar(ctx context.Context, r io.Reader, fn func(m member) error) error {
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("damaged tar archive: %w", err)
		}
		e := Entry{Name: h.Name, Size: h.Size, ModTime: h.ModTime}
		switch h.Typeflag {
		case tar.TypeDir:
			e.Dir, e.Size = true, 0
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink:
			e.Skipped = "link"
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		default:
			e.Skipped = "special file"
		}
		if err := fn(member{Entry: e, open: func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }}); err != nil {
			return err
		}
	}
}

// List returns the members of the archive f (name: its file name).
func List(ctx context.Context, f *os.File, name string) (*Listing, error) {
	format := Format(name)
	if format == "" {
		return nil, errors.New("not an archive")
	}
	l := &Listing{Format: format, Entries: []Entry{}}
	err := walk(ctx, f, format, name, func(m member) error {
		e := m.Entry
		if e.Skipped == "" {
			if _, ok := SafeName(e.Name); !ok {
				e.Skipped = "unsafe name"
			}
		}
		if !e.Dir && e.Skipped == "" {
			l.Files++
			if e.Size > 0 {
				l.Total += e.Size
			}
		}
		if len(l.Entries) >= MaxListed {
			l.Truncated = true
			return nil
		}
		l.Entries = append(l.Entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(l.Entries, func(i, j int) bool { return l.Entries[i].Name < l.Entries[j].Name })
	return l, nil
}

// Limits protect the disk from archive bombs.
type Limits struct {
	MaxFiles int   // members written
	MaxBytes int64 // bytes written in all
	// MaxRatio: written bytes over the archive size, checked once 1 GiB has been written.
	MaxRatio    float64
	ArchiveSize int64
}

// ErrLimit is returned when an extraction goes over a limit.
var ErrLimit = errors.New("extraction stopped")

// Stats sums up an extraction.
type Stats struct {
	Files   int   `json:"files"`
	Dirs    int   `json:"dirs"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped"`
}

// Extract writes the members of f into base (a folder relative to dst, which
// must exist). only, when not nil, selects members by name; their folders are
// then dropped (the files land directly in base). Existing files are never
// replaced: they are skipped. created is called for each new file or folder
// (owner, progress).
func Extract(ctx context.Context, f *os.File, name string, dst *os.Root, base string, only map[string]bool, lim Limits, created func(rel string, dir bool)) (Stats, error) {
	format := Format(name)
	if format == "" {
		return Stats{}, errors.New("not an archive")
	}
	var st Stats
	madeDirs := map[string]bool{}
	var mkdirs func(rel string) error
	mkdirs = func(rel string) error {
		if rel == base || rel == "." || madeDirs[rel] {
			return nil
		}
		if err := mkdirs(path.Dir(rel)); err != nil {
			return err
		}
		if err := dst.Mkdir(rel, 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, err := dst.Lstat(rel); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s is not a folder", rel)
		}
		if !madeDirs[rel] {
			madeDirs[rel] = true
			st.Dirs++
			if created != nil {
				created(rel, true)
			}
		}
		return nil
	}
	err := walk(ctx, f, format, name, func(m member) error {
		clean, ok := SafeName(m.Name)
		if !ok || m.Skipped != "" {
			st.Skipped++
			return nil
		}
		if only != nil {
			if !only[m.Name] && !only[clean] {
				return nil
			}
			if m.Dir {
				return nil
			}
			clean = path.Base(clean)
		}
		rel := path.Join(base, clean)
		if m.Dir {
			return mkdirs(rel)
		}
		if st.Files >= lim.MaxFiles {
			return fmt.Errorf("%w: more than %d files", ErrLimit, lim.MaxFiles)
		}
		if err := mkdirs(path.Dir(rel)); err != nil {
			return err
		}
		out, err := dst.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
		// A chosen file next to one of the same name: "name (2).ext".
		for i := 2; only != nil && errors.Is(err, fs.ErrExist) && i < 1000; i++ {
			ext := path.Ext(clean)
			rel = path.Join(base, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(clean, ext), i, ext))
			out, err = dst.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
		}
		if errors.Is(err, fs.ErrExist) {
			st.Skipped++
			return nil
		}
		if err != nil {
			return err
		}
		rc, err := m.open()
		if err != nil {
			out.Close()
			_ = dst.Remove(rel)
			return err
		}
		room := lim.MaxBytes - st.Bytes
		n, err := io.Copy(out, io.LimitReader(rc, room+1))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		st.Bytes += n
		if err == nil && n > room {
			err = fmt.Errorf("%w: more than %d MB would be written", ErrLimit, lim.MaxBytes>>20)
		}
		if err == nil && lim.MaxRatio > 0 && lim.ArchiveSize > 0 && st.Bytes > 1<<30 && float64(st.Bytes) > lim.MaxRatio*float64(lim.ArchiveSize) {
			err = fmt.Errorf("%w: the archive expands more than %.0f times (archive bomb?)", ErrLimit, lim.MaxRatio)
		}
		if err != nil {
			_ = dst.Remove(rel)
			return err
		}
		st.Files++
		if created != nil {
			created(rel, false)
		}
		return nil
	})
	return st, err
}
