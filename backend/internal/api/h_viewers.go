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
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"strataos/internal/archive"
	"strataos/internal/cinema"
	"strataos/internal/jobs"
	"strataos/internal/mediaplay"
	"strataos/internal/preview"
	"strataos/internal/rootio"
	"strataos/internal/store"
	"strataos/internal/stt"
	"strataos/internal/subs"
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
		plan = mediaplay.WithHeight(plan, mediaplay.HeightFrom(r)) // quality chosen in the mobile app
		subs := info.Subtitles()
		subs = append(subs, mediaplay.Sidecars(t.abs)...) // .srt/.vtt files next to the video
		return map[string]any{"installed": true, "plan": plan, "duration": info.Duration, "format": info.Format,
			"audioTracks": info.AudioTracks(), "subtitles": subs, "vaapi": mediaplay.Default.HW().VAAPI, "stt": stt.Installed()}, nil
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
		if idx < 0 {
			writeErr(w, badRequest("invalid subtitle track"))
			return
		}
		c := preview.DefaultCache
		key := preview.Key(t.abs, fi.ModTime(), fi.Size(), "vtt-"+strconv.Itoa(idx))
		var data []byte
		if idx >= mediaplay.SidecarBase {
			// A subtitle file next to the video: read directly, converted to
			// WebVTT in Go when it is SubRip (no ffmpeg needed).
			if data, err = readSidecar(t, idx); err != nil {
				writeErr(w, err)
				return
			}
		} else if p, ok := c.Get(key, ".vtt"); ok {
			data, err = os.ReadFile(p)
		} else if !mediaplay.Installed() {
			writeErr(w, badRequest("ffmpeg is not installed"))
			return
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

	// Transcribe the audio of a video or voice note with the local model
	// (whisper.cpp when installed): writes <name>.srt (picked up as a
	// subtitle sidecar) and <name>.transcript.txt next to the file.
	var transcribeMu sync.Mutex
	s.user("POST /api/files/transcribe", func(r *http.Request) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !stt.Installed() {
			return nil, &Error{Status: http.StatusConflict, Msg: "local speech-to-text is not installed (whisper.cpp + ffmpeg)"}
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write || t.protected() || t.rel == "." {
			return nil, errForbidden
		}
		if !stt.IsMedia(filepath.Ext(t.abs)) {
			return nil, badRequest("not a video or audio file")
		}
		transcribeMu.Lock()
		defer transcribeMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		work := filepath.Join(s.dataDir, "tmp", "stt")
		srt, text, err := stt.Transcribe(ctx, t.abs, work, "")
		if err != nil {
			return nil, &Error{Status: http.StatusBadGateway, Msg: err.Error()}
		}
		stem := strings.TrimSuffix(t.rel, path.Ext(t.rel))
		if err := s.writeSidecar(r, t, stem+".srt", srt); err != nil {
			return nil, err
		}
		if err := s.writeSidecar(r, t, stem+".transcript.txt", text); err != nil {
			return nil, err
		}
		s.audit(r, "stt", "transcribed: "+req.Path)
		return map[string]any{"text": string(text), "srt": true}, nil
	})

	// Fetch subtitles for a video from OpenSubtitles with the account stored
	// in the settings: search by IMDb id (known from the cinema library) or
	// by the file's moviehash, download the best match in the preferred
	// languages, write <name>.<lang>.srt next to the video (a sidecar).
	var fetchSubsMu sync.Mutex
	s.user("POST /api/files/subtitles/fetch", func(r *http.Request) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var set store.Settings
		s.store.Read(func(st *store.State) { set = st.Settings })
		if set.OSApiKey == "" || set.OSUser == "" || set.OSPass == "" {
			return nil, &Error{Status: http.StatusConflict, Msg: "OpenSubtitles credentials are not configured"}
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write || t.protected() || t.rel == "." {
			return nil, errForbidden
		}
		if !stt.IsMedia(filepath.Ext(t.abs)) {
			return nil, badRequest("not a video or audio file")
		}
		fetchSubsMu.Lock()
		defer fetchSubsMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		langs := subs.ParseLangs(set.SubLangs)
		if langs == "" {
			langs = "en"
		}
		imdb := ""
		statePath, _, _ := s.cinemaPaths()
		for _, it := range cinema.LoadState(statePath).Items {
			if it.Path == t.abs && it.IMDB != "" {
				imdb = it.IMDB
				break
			}
		}
		hash := ""
		if imdb == "" {
			if hash, err = subs.MovieHash(t.abs); err != nil {
				return nil, err
			}
		}
		cl := &subs.Client{APIKey: set.OSApiKey, User: set.OSUser, Password: set.OSPass}
		results, err := cl.Search(ctx, imdb, hash, langs)
		if err != nil {
			return nil, &Error{Status: http.StatusBadGateway, Msg: err.Error()}
		}
		best := subs.PickBest(results, langs)
		if best < 0 {
			return nil, &Error{Status: http.StatusNotFound, Msg: "no subtitle found for this video"}
		}
		data, err := cl.Download(ctx, results[best].FileID)
		if err != nil {
			return nil, &Error{Status: http.StatusBadGateway, Msg: err.Error()}
		}
		// The language comes from the API: sanitize it, it goes in a file name.
		lang := subs.SanitizeLang(results[best].Lang)
		out := strings.TrimSuffix(t.rel, path.Ext(t.rel)) + "." + lang + ".srt"
		if err := s.writeSidecar(r, t, out, data); err != nil {
			return nil, err
		}
		s.audit(r, "subs", "fetched subtitles: "+req.Path+" ("+lang+")")
		return map[string]any{"lang": lang, "name": path.Base(out)}, nil
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

// maxSidecar caps a subtitle file read next to a video.
const maxSidecar = 5 << 20

// writeSidecar writes a file next to the media t (rel is relative to t's
// root) through the root, owned like the caller's other writes, never
// through a planted symlink.
func (s *Server) writeSidecar(r *http.Request, t target, rel string, data []byte) error {
	rt, err := t.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	owner := s.ownerFor(r)
	if cur, _, ok := rootio.OwnerOf(rt, rel); ok {
		owner = cur
	}
	return rootio.WriteFile(rt, rel, data, 0o644, owner)
}

// readSidecar reads the subtitle file idx next to the video t through the
// share's root (a link out of the share, a device or a huge file is
// refused), converted to WebVTT when it is SubRip.
func readSidecar(t target, idx int) ([]byte, error) {
	name, ok := mediaplay.SidecarName(t.abs, idx)
	if !ok {
		return nil, notFound("subtitle file not found")
	}
	rt, err := t.open()
	if err != nil {
		return nil, err
	}
	defer rt.Close()
	raw, err := rootio.ReadFile(rt, path.Join(path.Dir(t.rel), name), maxSidecar)
	if err != nil {
		return nil, notFound("subtitle file not found")
	}
	if strings.HasSuffix(name, ".srt") {
		return mediaplay.SRTtoVTT(raw), nil
	}
	return raw, nil
}
