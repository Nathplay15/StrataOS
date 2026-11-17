// Batch video compression: the videos of a folder are re-encoded to H.265
// one by one; the original is replaced only when the result is clearly
// smaller, so a marginal gain never costs a generational loss, and only when
// the result checks out (same duration, decodes without error). The new file
// takes the original's owner, group, mode, ACL and extended attributes and
// dates; the original goes to the share's recycle bin. Hard-linked files are
// left alone (their other names would keep the old content). Needs the media
// tools component (ffmpeg).

package storagetools

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// VideoExt: containers the re-encode writes back (mp4 and mkv accept every
// stream a phone or a camera produces; avi, ts and webm are left out).
var VideoExt = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
}

const (
	// Videos smaller than this cannot save enough to be worth the CPU.
	DefaultRecodeMinSize = 100 << 20
	// The re-encode replaces the original only under this ratio.
	RecodeKeepRatio = 0.8
	// One ffmpeg run at most.
	maxRecodeRun = 6 * time.Hour
)

// RecodeFile is one video of the report.
type RecodeFile struct {
	Path string `json:"path"`
	Old  int64  `json:"old"`
	New  int64  `json:"new,omitempty"`
	// recoded | kept (the re-encode was not smaller) | failed
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// RecodeReport is the result of a batch.
type RecodeReport struct {
	Started time.Time    `json:"started"`
	Files   []RecodeFile `json:"files"`
	Saved   int64        `json:"saved"`
}

// RecodeProgress reports the batch: stage "scan" then "recode".
type RecodeProgress func(stage string, done, total int64)

// FFmpegAvailable tells whether the media tools are installed.
func FFmpegAvailable() bool {
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			return false
		}
	}
	return true
}

// PlanRecode lists the videos of root a re-encode can shrink: big enough and
// not already H.265/HEVC.
func PlanRecode(ctx context.Context, root string, minSize int64, progress RecodeProgress) ([]RecodeFile, error) {
	var out []RecodeFile
	err := walkFiles(ctx, root, minSize, func(c candidate) {
		if !VideoExt[strings.ToLower(filepath.Ext(c.path))] {
			return
		}
		codec := videoCodec(c.path)
		if codec == "hevc" || codec == "h265" {
			return
		}
		out = append(out, RecodeFile{Path: c.path, Old: c.size, Detail: codec})
	})
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress("scan", int64(len(out)), 0)
	}
	return out, nil
}

// videoCodec returns the codec of the first video stream, "" when unknown.
func videoCodec(path string) string {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// RecodeOptions says what becomes of the originals.
type RecodeOptions struct {
	// HasBin tells whether the original can be kept (its shared folder has
	// a recycle bin), and Keep puts it there before the new file takes its
	// place (an error keeps the original in place).
	HasBin func(path string) bool
	Keep   func(path string) error
	// Discard: originals without a recycle bin may be replaced and lost
	// (asked for explicitly).
	Discard bool
}

func (o RecodeOptions) binFor(path string) bool {
	return o.HasBin != nil && o.Keep != nil && o.HasBin(path)
}

// RecodeVideos re-encodes every file (H.265, CRF 28) and replaces the
// original when the result is at least 20% smaller and checks out.
func RecodeVideos(ctx context.Context, files []RecodeFile, o RecodeOptions, progress RecodeProgress) *RecodeReport {
	rep := &RecodeReport{Started: time.Now(), Files: []RecodeFile{}}
	for i, f := range files {
		if cerr := ctx.Err(); cerr != nil {
			rep.Files = append(rep.Files, RecodeFile{Path: f.Path, Old: f.Old, Status: "failed", Detail: "cancelled"})
			break
		}
		info, err := os.Lstat(f.Path)
		if err != nil || !info.Mode().IsRegular() {
			rep.Files = append(rep.Files, RecodeFile{Path: f.Path, Old: f.Old, Status: "failed", Detail: "the file disappeared"})
			continue
		}
		nf := RecodeFile{Path: f.Path, Old: info.Size()}
		size, detail, failed := recodeOne(ctx, f.Path, info, o)
		switch {
		case size > 0:
			nf.New, nf.Status = size, "recoded"
			rep.Saved += nf.Old - size
		case failed:
			nf.Status, nf.Detail = "failed", detail
		default:
			nf.Status, nf.Detail = "kept", detail
		}
		rep.Files = append(rep.Files, nf)
		if progress != nil {
			progress("recode", int64(i+1), int64(len(files)))
		}
	}
	return rep
}

// muxer is the ffmpeg output format of a container (the temporary file's
// name ends in .tmp: ffmpeg cannot guess it).
func muxer(ext string) string {
	switch strings.ToLower(ext) {
	case ".mkv":
		return "matroska"
	case ".mov":
		return "mov"
	}
	return "mp4"
}

// recodeOne re-encodes path; it returns the new size (0: the original stays),
// and why it stayed (failed: an error rather than a choice).
func recodeOne(ctx context.Context, path string, info os.FileInfo, o RecodeOptions) (int64, string, bool) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return 0, "hard-linked: its other names would keep the old video", false
	}
	bin := o.binFor(path)
	if !bin && !o.Discard {
		return 0, "no recycle bin to keep the original", false
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".strata-recode.tmp")
	os.Remove(tmp)
	defer os.Remove(tmp)
	if err := ffmpegEncode(ctx, path, tmp, filepath.Ext(path)); err != nil {
		return 0, err.Error(), true
	}
	nf, err := os.Lstat(tmp)
	if err != nil || !nf.Mode().IsRegular() {
		return 0, "unreadable result", true
	}
	if nf.Size() > int64(RecodeKeepRatio*float64(info.Size())) {
		return 0, "the re-encode is not smaller enough", false
	}
	if err := checkRecode(ctx, path, tmp); err != nil {
		return 0, err.Error(), true
	}
	// The file may have changed during the encode (hours): keep it then.
	if now, err := os.Lstat(path); err != nil || !now.ModTime().Equal(info.ModTime()) || now.Size() != info.Size() {
		return 0, "the video changed during the re-encode", false
	}
	if err := copyFileMeta(path, info, tmp); err != nil {
		return 0, "cannot copy the owner and permissions: " + err.Error(), true
	}
	if bin {
		if err := o.Keep(path); err != nil {
			return 0, "the original could not be put in the recycle bin: " + err.Error(), true
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err.Error(), true
	}
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	return nf.Size(), "", false
}

// checkRecode makes sure the new file is the same video: an H.265 stream,
// the same duration (within 1 s or 1%), and a full decode without error.
func checkRecode(ctx context.Context, orig, out string) error {
	if c := videoCodec(out); c != "hevc" {
		return fmt.Errorf("the result has no H.265 video (%q)", c)
	}
	d1, d2 := duration(orig), duration(out)
	if !sameDuration(d1, d2) {
		return fmt.Errorf("the result lasts %.1f s instead of %.1f s", d2, d1)
	}
	rctx, cancel := context.WithTimeout(ctx, maxRecodeRun)
	defer cancel()
	cmd := exec.CommandContext(rctx, "ffmpeg", "-v", "error", "-xerror", "-i", out, "-map", "0:v:0", "-f", "null", "-")
	if b, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(b)) != "" {
		return fmt.Errorf("the result does not decode cleanly: %s", lastLine(string(b), err))
	}
	return nil
}

// sameDuration: within 1 second or 1% (containers round differently).
func sameDuration(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	return math.Abs(a-b) <= math.Max(1, a/100)
}

// duration of a file in seconds, 0 when unknown.
func duration(path string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

func lastLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" && err != nil {
		s = err.Error()
	}
	return s
}

// copyFileMeta gives dst the owner, group, mode and extended attributes
// (POSIX ACLs included: system.posix_acl_*) of src.
func copyFileMeta(src string, info os.FileInfo, dst string) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Lchown(dst, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	if err := os.Chmod(dst, info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky)); err != nil {
		return err
	}
	sz, err := unix.Llistxattr(src, nil)
	if err != nil || sz == 0 {
		return nil // no extended attributes (or a file system without them)
	}
	buf := make([]byte, sz)
	n, err := unix.Llistxattr(src, buf)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		vs, err := unix.Lgetxattr(src, name, nil)
		if err != nil {
			continue
		}
		val := make([]byte, vs)
		m, err := unix.Lgetxattr(src, name, val)
		if err != nil {
			continue
		}
		if err := unix.Lsetxattr(dst, name, val[:m], 0); err != nil && strings.HasPrefix(name, "system.posix_acl") {
			return fmt.Errorf("ACL %s: %v", name, err)
		}
	}
	return nil
}

// ffmpegEncode writes the H.265 copy of src to dst. Audio and subtitles are
// streamed as they are; a container refusing one of them gets a second run
// with everything transcoded.
func ffmpegEncode(ctx context.Context, src, dst, ext string) error {
	run := func(extra ...string) error {
		rctx, cancel := context.WithTimeout(ctx, maxRecodeRun)
		defer cancel()
		args := append([]string{"-y", "-nostdin", "-i", src, "-map", "0", "-c:v", "libx265", "-crf", "28", "-preset", "medium"}, extra...)
		// hvc1: the tag Apple players need for H.265 in MP4/MOV.
		if muxer(ext) != "matroska" {
			args = append(args, "-tag:v", "hvc1")
		}
		args = append(args, "-f", muxer(ext), dst)
		cmd := exec.CommandContext(rctx, "ffmpeg", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg: %s", lastLine(string(out), err))
		}
		return nil
	}
	audio := []string{"-c:a", "copy", "-c:s", "copy", "-c:d", "copy"}
	if muxer(ext) != "matroska" {
		audio = append(audio, "-movflags", "+faststart")
	}
	if err := run(audio...); err == nil {
		return nil
	}
	if muxer(ext) == "matroska" {
		return run("-c:a", "aac", "-b:a", "192k", "-c:s", "srt", "-dn")
	}
	return run("-c:a", "aac", "-b:a", "192k", "-c:s", "mov_text", "-dn", "-movflags", "+faststart")
}
