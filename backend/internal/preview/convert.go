package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/sys"
)

// Limits of the converters.
const (
	OfficeMaxBytes = 100 << 20 // documents larger than this are not converted
	ImageMaxBytes  = 300 << 20
	officeTimeout  = 2 * time.Minute
	imageTimeout   = time.Minute
)

// ErrNotInstalled: the converter of this type is not installed.
var ErrNotInstalled = errors.New("document previews are not installed")

// ErrTooLarge: the file is over the converter's size limit.
var ErrTooLarge = errors.New("file too large to preview")

// sofficePaths: where Debian installs LibreOffice.
var sofficePaths = []string{"/usr/bin/soffice", "/usr/lib/libreoffice/program/soffice"}

func soffice() string {
	for _, p := range sofficePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// OfficeInstalled reports whether LibreOffice (the "Document previews" component) is present.
func OfficeInstalled() bool { return soffice() != "" }

// OfficeTypes are the extensions LibreOffice converts to PDF.
var OfficeTypes = map[string]bool{
	".doc": true, ".docx": true, ".docm": true, ".dot": true, ".dotx": true, ".odt": true, ".ott": true, ".fodt": true, ".rtf": true, ".wpd": true, ".wps": true, ".pages": true,
	".xls": true, ".xlsx": true, ".xlsm": true, ".xlsb": true, ".ods": true, ".ots": true, ".fods": true, ".numbers": true,
	".ppt": true, ".pptx": true, ".pptm": true, ".pps": true, ".ppsx": true, ".odp": true, ".otp": true, ".fodp": true, ".key": true,
	".odg": true, ".vsd": true, ".vsdx": true, ".pub": true,
}

// Slots: LibreOffice needs a few hundred MB per conversion.
var officeSlots = make(chan struct{}, 1)
var imageSlots = make(chan struct{}, 2)

// keyLocks makes concurrent requests for the same conversion (the PDF
// viewer's range requests) wait for one run instead of starting several.
var keyLocks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func lockKey(k string) func() {
	keyLocks.Lock()
	l := keyLocks.m[k]
	if l == nil {
		l = &sync.Mutex{}
		keyLocks.m[k] = l
	}
	keyLocks.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		keyLocks.Lock()
		if keyLocks.m[k] == l {
			delete(keyLocks.m, k)
		}
		keyLocks.Unlock()
	}
}

func acquire(ctx context.Context, slots chan struct{}, wait time.Duration) (func(), error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-t.C:
		return nil, errors.New("the converter is busy, try again in a moment")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// copyIn copies f into the work folder as name, readable by the sandbox user.
func copyIn(f *os.File, dir, name string, limit int64, uid, gid int) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(f, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", ErrTooLarge
	}
	if uid >= 0 {
		_ = os.Chown(p, uid, gid)
	}
	return p, nil
}

// run starts a sandboxed converter in dir; stdin, when set, is fed as is.
func run(ctx context.Context, dir string, stdin *os.File, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C.UTF-8", "TMPDIR=" + dir}
	if stdin != nil {
		if _, err := stdin.Seek(0, io.SeekStart); err != nil {
			return err
		}
		cmd.Stdin = stdin
	}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = sys.Sandboxed()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s: timed out", filepath.Base(name))
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s: %s", filepath.Base(name), msg)
	}
	return nil
}

// cached runs convert once per key and returns the cached result.
func (c *Cache) cached(key, ext string, convert func(work string, uid, gid int) (string, error)) (string, error) {
	if p, ok := c.Get(key, ext); ok {
		return p, nil
	}
	unlock := lockKey(key)
	defer unlock()
	if p, ok := c.Get(key, ext); ok {
		return p, nil
	}
	uid, gid := sys.SandboxOwner()
	work, err := c.WorkDir(uid, gid)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	out, err := convert(work, uid, gid)
	if err != nil {
		return "", err
	}
	return c.Put(key, ext, out)
}

// Office returns a PDF of the document f (path abs, for the cache key).
func (c *Cache) Office(ctx context.Context, f *os.File, abs string, info fs.FileInfo) (string, error) {
	bin := soffice()
	if bin == "" {
		return "", ErrNotInstalled
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if !OfficeTypes[ext] {
		return "", errors.New("this file type cannot be converted")
	}
	if info.Size() > OfficeMaxBytes {
		return "", ErrTooLarge
	}
	return c.cached(Key(abs, info.ModTime(), info.Size(), "pdf"), ".pdf", func(work string, uid, gid int) (string, error) {
		release, err := acquire(ctx, officeSlots, officeTimeout)
		if err != nil {
			return "", err
		}
		defer release()
		in, err := copyIn(f, work, "document"+ext, OfficeMaxBytes, uid, gid)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(ctx, officeTimeout)
		defer cancel()
		// A fresh profile per run: nothing persists between documents.
		err = run(ctx, work, nil, io.Discard, bin, "--headless", "--invisible", "--nodefault", "--nofirststartwizard", "--nolockcheck", "--nologo", "--norestore",
			"-env:UserInstallation=file://"+work+"/profile", "--convert-to", "pdf", "--outdir", work, in)
		if err != nil {
			return "", err
		}
		out := filepath.Join(work, "document.pdf")
		if _, err := os.Stat(out); err != nil {
			return "", errors.New("the document could not be converted")
		}
		return out, nil
	})
}

// ---- Pictures ----

// rawTypes are camera RAW formats: their embedded JPEG preview is shown.
var rawTypes = map[string]bool{".cr2": true, ".cr3": true, ".crw": true, ".nef": true, ".nrw": true, ".arw": true, ".srf": true, ".sr2": true, ".dng": true, ".orf": true,
	".rw2": true, ".raf": true, ".pef": true, ".srw": true, ".x3f": true, ".3fr": true, ".kdc": true, ".dcr": true, ".mrw": true, ".erf": true, ".mos": true, ".iiq": true, ".rwl": true}

// convertTypes are pictures browsers do not display; the value says whether
// they may be transparent (then WebP, which keeps the alpha channel).
var convertTypes = map[string]bool{".heic": false, ".heif": false, ".hif": false, ".tif": true, ".tiff": true, ".psd": true, ".psb": true, ".jxl": true,
	".tga": true, ".ppm": false, ".pgm": false, ".pbm": false, ".pnm": false, ".exr": true, ".jp2": true, ".j2k": true, ".dds": true, ".pcx": false, ".sgi": true, ".xcf": true}

// Tools are the image converters present.
type Tools struct{ Vips, FFmpeg, Exiftool bool }

// InstalledTools looks for the converters (installed with Photos).
func InstalledTools() Tools {
	return Tools{Vips: sys.Has("vipsthumbnail"), FFmpeg: sys.Has("ffmpeg"), Exiftool: sys.Has("exiftool")}
}

// ImageConvertible reports whether ext is a picture shown through a conversion.
func ImageConvertible(ext string) bool {
	ext = strings.ToLower(ext)
	_, ok := convertTypes[ext]
	return ok || rawTypes[ext]
}

// ImagePlan returns the converters to try in order for ext (the first one
// that works wins) and the output format, "jpeg" or "webp".
func ImagePlan(ext string, t Tools) (steps []string, format string) {
	ext = strings.ToLower(ext)
	if rawTypes[ext] {
		// The embedded preview, then libvips to size it; ffmpeg reads some DNGs.
		if t.Exiftool {
			steps = append(steps, "exiftool")
		}
		if t.FFmpeg && ext == ".dng" {
			steps = append(steps, "ffmpeg")
		}
		return steps, "jpeg"
	}
	alpha, ok := convertTypes[ext]
	if !ok {
		return nil, ""
	}
	format = "jpeg"
	if alpha {
		format = "webp"
	}
	if t.Vips {
		steps = append(steps, "vips")
	}
	// ffmpeg flattens PSD and reads TIFF, TGA, EXR, JPEG 2000…; not HEIC grids.
	if t.FFmpeg && ext != ".heic" && ext != ".heif" && ext != ".hif" && ext != ".xcf" && ext != ".jxl" {
		steps = append(steps, "ffmpeg")
	}
	return steps, format
}

// Image returns the picture f converted for the browser, at most px pixels wide or high.
func (c *Cache) Image(ctx context.Context, f *os.File, abs string, info fs.FileInfo, px int) (path, ctype string, err error) {
	ext := strings.ToLower(filepath.Ext(abs))
	steps, format := ImagePlan(ext, InstalledTools())
	if len(steps) == 0 {
		return "", "", ErrNotInstalled
	}
	if info.Size() > ImageMaxBytes {
		return "", "", ErrTooLarge
	}
	outExt, ctype := ".jpg", "image/jpeg"
	if format == "webp" {
		outExt, ctype = ".webp", "image/webp"
	}
	p, err := c.cached(Key(abs, info.ModTime(), info.Size(), "img-"+strconv.Itoa(px)+outExt), outExt, func(work string, uid, gid int) (string, error) {
		release, err := acquire(ctx, imageSlots, imageTimeout)
		if err != nil {
			return "", err
		}
		defer release()
		ctx, cancel := context.WithTimeout(ctx, imageTimeout)
		defer cancel()
		out := filepath.Join(work, "out"+outExt)
		var errs []string
		for _, step := range steps {
			if err := convertImage(ctx, step, f, work, ext, out, px, uid, gid); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if st, err := os.Stat(out); err == nil && st.Size() > 0 {
				return out, nil
			}
		}
		return "", fmt.Errorf("the picture could not be converted (%s)", strings.Join(errs, "; "))
	})
	return p, ctype, err
}

func convertImage(ctx context.Context, step string, f *os.File, work, ext, out string, px, uid, gid int) error {
	size := fmt.Sprintf("%dx%d>", px, px)
	q := "[Q=85,strip]"
	switch step {
	case "exiftool":
		// The largest embedded JPEG; exiftool reads the file on its stdin.
		prev := filepath.Join(work, "embedded.jpg")
		for _, tag := range []string{"-JpgFromRaw", "-PreviewImage", "-ThumbnailImage"} {
			var buf bytes.Buffer
			if err := run(ctx, work, f, &limitedBuf{b: &buf, max: 64 << 20}, "exiftool", "-b", tag, "-"); err != nil || buf.Len() < 1024 {
				continue
			}
			if err := os.WriteFile(prev, buf.Bytes(), 0o644); err != nil {
				return err
			}
			if sys.Has("vipsthumbnail") {
				// Resize and apply the orientation of the preview.
				return run(ctx, work, nil, io.Discard, "vipsthumbnail", prev, "--size", size, "-o", out+q)
			}
			return os.Rename(prev, out)
		}
		return errors.New("exiftool: no preview in this file")
	case "vips":
		in, err := copyIn(f, work, "in"+ext, ImageMaxBytes, uid, gid)
		if err != nil {
			return err
		}
		return run(ctx, work, nil, io.Discard, "vipsthumbnail", in, "--size", size, "-o", out+q)
	case "ffmpeg":
		return run(ctx, work, f, io.Discard, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", "fd:", "-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", px, px), "-y", out)
	}
	return errors.New("unknown converter")
}

type limitedBuf struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > l.max {
		return 0, errors.New("output too large")
	}
	return l.b.Write(p)
}
