package api

// File viewers of the file manager: details (owner, SHA-256), tail of large
// logs, media playback of any codec (mediaplay), pictures and office
// documents converted for the browser (preview), archives listed and
// extracted (archive). Everything is read with the caller's rights through
// locate and os.Root, like downloads.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"strataos/internal/archive"
	"strataos/internal/jobs"
	"strataos/internal/mediaplay"
	"strataos/internal/preview"
	"strataos/internal/sys"
)

// docPreviewSize is shown next to the install button of the component.
const docPreviewSize = "≈ 400 MB"

func init() {
	archive.SevenZipCopy = func(f *os.File) (string, func(), error) {
		uid, gid := sys.SandboxOwner()
		dir, err := preview.DefaultCache.WorkDir(uid, gid)
		if err != nil {
			return "", nil, err
		}
		cleanup := func() { os.RemoveAll(dir) }
		p, err := archive.TempCopy(f, dir, uid, gid)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		return p, cleanup, nil
	}
}

// openRead opens a regular file the caller may read. The file stays usable
// after the root is closed.
func (s *Server) openRead(r *http.Request, p string) (target, *os.File, fs.FileInfo, error) {
	t, err := s.locate(r, p)
	if err != nil || !t.acc.read || t.acc.ancestor {
		return t, nil, nil, firstErr(err, errForbidden)
	}
	rt, err := t.open()
	if err != nil {
		return t, nil, nil, err
	}
	defer rt.Close()
	f, err := rt.Open(t.rel)
	if err != nil {
		return t, nil, nil, notFound("file not found")
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return t, nil, nil, badRequest("not a regular file")
	}
	return t, f, fi, nil
}

// looksText: UTF-8 without NUL bytes (a cut rune at the end is fine).
func looksText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return utf8.Valid(b) && !strings.ContainsRune(string(b), 0)
}

func ownerName(id uint32, group bool) string {
	n := strconv.FormatUint(uint64(id), 10)
	if group {
		if g, err := user.LookupGroupId(n); err == nil {
			return g.Name
		}
		return n
	}
	if u, err := user.LookupId(n); err == nil {
		return u.Username
	}
	return n
}

// ctxReader stops a long read when the client goes away.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func convertErr(err error) error {
	switch {
	case errors.Is(err, preview.ErrNotInstalled):
		return &Error{Status: http.StatusConflict, Msg: err.Error()}
	case errors.Is(err, preview.ErrTooLarge):
		return &Error{Status: http.StatusRequestEntityTooLarge, Msg: err.Error()}
	case errors.Is(err, mediaplay.ErrBusy):
		return &Error{Status: http.StatusServiceUnavailable, Msg: err.Error()}
	case errors.Is(err, context.Canceled):
		return err
	}
	return &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
}

// serveCached sends a converted file with Range support (PDF viewer).
func serveCached(w http.ResponseWriter, r *http.Request, p, ctype, name string) {
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, notFound("preview not found"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, notFound("preview not found"))
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// archiveStem: "photos.tar.gz" → "photos".
func archiveStem(name string) string {
	lower := strings.ToLower(name)
	for _, suf := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tgz", ".tbz2", ".tbz", ".txz", ".tzst", ".tar", ".zip", ".jar", ".cbz", ".epub", ".7z", ".rar", ".cbr", ".gz", ".bz2", ".xz", ".zst"} {
		if strings.HasSuffix(lower, suf) && len(name) > len(suf) {
			return name[:len(name)-len(suf)]
		}
	}
	return name
}

// Limits of an extraction: archive bombs stop at the first of these.
const (
	extractMaxFiles = 50000
	extractMaxBytes = 50 << 30
	extractMaxRatio = 200
)

func (s *Server) viewerRoutes() {
	go preview.DefaultCache.CleanWork()

	// What the viewers can do on this NAS.
	s.user("GET /api/files/viewers", func(r *http.Request) (any, error) {
		tools := preview.InstalledTools()
		media := mediaplay.Installed()
		return map[string]any{
			"admin":          isAdmin(r),
			"docPreview":     preview.OfficeInstalled(),
			"docPreviewSize": docPreviewSize,
			"media":          media,
			"vaapi":          media && mediaplay.Default.HW().VAAPI,
			"images":         tools.Vips || tools.FFmpeg,
			"raw":            tools.Exiftool,
			"sevenZip":       archive.SevenZipInstalled(),
		}, nil
	})

	// Details of a file: owner, type sniffed from the content, text or not.
	s.user("GET /api/files/info", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			return nil, firstErr(err, errForbidden)
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		lfi, err := rt.Lstat(t.rel)
		if err != nil {
			return nil, notFound("file not found")
		}
		out := map[string]any{"name": filepath.Base(t.abs), "path": t.abs, "size": lfi.Size(), "mode": lfi.Mode().String(), "modTime": lfi.ModTime(),
			"dir": lfi.IsDir(), "link": lfi.Mode()&fs.ModeSymlink != 0, "writable": s.readOnlyReason(t) == "", "archive": archive.Format(t.abs)}
		if st, ok := lfi.Sys().(*syscall.Stat_t); ok {
			out["owner"], out["group"] = ownerName(st.Uid, false), ownerName(st.Gid, true)
			out["changed"] = statCtime(st)
		}
		if f, err := rt.Open(t.rel); err == nil {
			if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
				head := make([]byte, 64<<10)
				n, _ := io.ReadFull(f, head)
				out["mime"] = http.DetectContentType(head[:n])
				out["text"] = looksText(head[:n])
				out["size"] = fi.Size()
			}
			f.Close()
		}
		return out, nil
	})

	// SHA-256 on demand (reads the whole file).
	s.user("GET /api/files/hash", func(r *http.Request) (any, error) {
		_, f, _, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, ctxReader{r.Context(), f}); err != nil {
			return nil, err
		}
		return map[string]string{"sha256": hex.EncodeToString(h.Sum(nil))}, nil
	})

	// The end of a large text file (logs), from a whole line.
	s.user("GET /api/files/tail", func(r *http.Request) (any, error) {
		_, f, fi, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		n := int64(qInt(r, "bytes", 256<<10))
		n = min(max(n, 4<<10), 8<<20)
		off := max(fi.Size()-n, 0)
		buf := make([]byte, fi.Size()-off)
		m, err := f.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return nil, err
		}
		buf = buf[:m]
		if off > 0 {
			if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
				buf = buf[i+1:]
				off += int64(i + 1)
			}
		}
		return map[string]any{"content": strings.ToValidUTF8(string(buf), "�"), "offset": off, "size": fi.Size(), "modTime": fi.ModTime()}, nil
	})

	// ---- Audio and video ----
	s.user("GET /api/files/probe", func(r *http.Request) (any, error) {
		t, f, _, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if !mediaplay.Installed() {
			return map[string]any{"installed": false}, nil
		}
		info, err := mediaplay.Probe(r.Context(), f)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		plan, err := mediaplay.Decide(info, t.abs, mediaplay.CapsFrom(r), qInt(r, "audio", -1))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]any{"installed": true, "plan": plan, "duration": info.Duration, "format": info.Format,
			"audioTracks": info.AudioTracks(), "subtitles": info.Subtitles(), "vaapi": mediaplay.Default.HW().VAAPI}, nil
	})
	s.userRaw("GET /api/files/play", func(w http.ResponseWriter, r *http.Request) {
		t, f, _, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		if !mediaplay.Installed() {
			writeErr(w, &Error{Status: http.StatusConflict, Msg: "ffmpeg is not installed"})
			return
		}
		if err := mediaplay.ServeFile(w, r, f, t.abs); err != nil && r.Context().Err() == nil {
			writeErr(w, convertErr(err))
		}
	})
	s.userRaw("GET /api/files/subtitle", func(w http.ResponseWriter, r *http.Request) {
		t, f, fi, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		idx := qInt(r, "index", -1)
		if idx < 0 || !mediaplay.Installed() {
			writeErr(w, badRequest("invalid subtitle track"))
			return
		}
		c := preview.DefaultCache
		key := preview.Key(t.abs, fi.ModTime(), fi.Size(), "vtt-"+strconv.Itoa(idx))
		var data []byte
		if p, ok := c.Get(key, ".vtt"); ok {
			data, err = os.ReadFile(p)
		} else if data, err = mediaplay.Subtitles(r.Context(), f, idx); err == nil {
			if tmp, terr := os.CreateTemp(c.Dir, ".sub-*"); terr == nil {
				_, _ = tmp.Write(data)
				tmp.Close()
				if _, perr := c.Put(key, ".vtt", tmp.Name()); perr != nil {
					os.Remove(tmp.Name())
				}
			}
		}
		if err != nil {
			writeErr(w, convertErr(err))
			return
		}
		start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(mediaplay.ShiftVTT(data, start))
	})

	// ---- Pictures and documents converted for the browser ----
	s.userRaw("GET /api/files/image", func(w http.ResponseWriter, r *http.Request) {
		t, f, fi, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		px := min(max(qInt(r, "size", 2560), 128), 8192)
		p, ctype, err := preview.DefaultCache.Image(r.Context(), f, t.abs, fi, px)
		if err != nil {
			writeErr(w, convertErr(err))
			return
		}
		serveCached(w, r, p, ctype, strings.TrimSuffix(filepath.Base(t.abs), filepath.Ext(t.abs))+filepath.Ext(p))
	})
	s.userRaw("GET /api/files/docpreview", func(w http.ResponseWriter, r *http.Request) {
		t, f, fi, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		p, err := preview.DefaultCache.Office(r.Context(), f, t.abs, fi)
		if err != nil {
			writeErr(w, convertErr(err))
			return
		}
		serveCached(w, r, p, "application/pdf", strings.TrimSuffix(filepath.Base(t.abs), filepath.Ext(t.abs))+".pdf")
	})

	// ---- Archives ----
	s.user("GET /api/files/archive", func(r *http.Request) (any, error) {
		t, f, _, err := s.openRead(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		l, err := archive.List(r.Context(), f, t.abs)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return l, nil
	})
	// Extracts the whole archive into a new folder next to it, or the chosen
	// members into the archive's folder.
	s.user("POST /api/files/archive/extract", func(r *http.Request) (any, error) {
		var req struct {
			Path    string   `json:"path"`
			Entries []string `json:"entries"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		src, f, fi, err := s.openRead(r, req.Path)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(src.abs)
		if archive.Format(name) == "" {
			f.Close()
			return nil, badRequest("not an archive")
		}
		dst, err := s.locate(r, filepath.Dir(src.abs))
		if err == nil && (!dst.acc.write || dst.acc.ancestor) {
			err = &Error{Status: http.StatusForbidden, Msg: "read-only access"}
		}
		if err == nil {
			if reason := s.readOnlyReason(dst); reason != "" {
				err = &Error{Status: http.StatusForbidden, Msg: reason}
			}
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		var only map[string]bool
		if len(req.Entries) > 0 {
			only = map[string]bool{}
			for _, e := range req.Entries {
				only[e] = true
			}
		}
		lim := archive.Limits{MaxFiles: extractMaxFiles, MaxBytes: extractMaxBytes, MaxRatio: extractMaxRatio, ArchiveSize: fi.Size()}
		if room := s.quotaRoom(userOf(r), isAdmin(r), dst.abs); room >= 0 && room < lim.MaxBytes {
			lim.MaxBytes = room
		}
		var sfs syscall.Statfs_t
		if syscall.Statfs(dst.abs, &sfs) == nil {
			free := int64(sfs.Bavail)*int64(sfs.Bsize) - 512<<20
			lim.MaxBytes = max(min(lim.MaxBytes, free), 0)
		}
		me, admin := userOf(r), isAdmin(r)
		s.audit(r, "files", "Extracted "+src.abs)
		return s.jobs.Start("extract", "Extract "+name, me, func(ctx context.Context, j *jobs.Job) error {
			defer f.Close()
			rt, err := dst.open()
			if err != nil {
				return err
			}
			defer rt.Close()
			base := dst.rel
			newDir := ""
			if only == nil {
				d := uniqueName(func(n string) bool {
					_, err := rt.Lstat(path.Join(dst.rel, n))
					return !errors.Is(err, fs.ErrNotExist)
				}, archiveStem(name))
				newDir = path.Join(dst.rel, d)
				if err := rt.Mkdir(newDir, 0o775); err != nil {
					return err
				}
				if !admin {
					s.chownTo(me, rt, newDir)
				}
				base = newDir
				j.Logf("Extracting into %s", filepath.Join(dst.abs, d))
			}
			st, err := archive.Extract(ctx, f, name, rt, base, only, lim, func(rel string, dir bool) {
				if !admin {
					s.chownTo(me, rt, rel)
				}
			})
			j.Logf("%d file(s), %d folder(s), %d MB written, %d skipped", st.Files, st.Dirs, st.Bytes>>20, st.Skipped)
			if err != nil && newDir != "" && errors.Is(err, archive.ErrLimit) {
				_ = rt.RemoveAll(newDir) // nothing half-extracted left behind
			}
			if err == nil && only != nil && st.Files == 0 {
				err = errors.New("nothing extracted (the file exists already or is not in the archive)")
			}
			return err
		}), nil
	})
}

func statCtime(st *syscall.Stat_t) time.Time {
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec))
}
