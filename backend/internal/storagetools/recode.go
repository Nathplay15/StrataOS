// Batch video compression: the videos of a folder are re-encoded to H.265
// one by one; the original is replaced only when the result is clearly
// smaller, so a marginal gain never costs a generational loss. Needs the
// media tools component (ffmpeg).

package storagetools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// RecodeVideos re-encodes every file (H.265, CRF 28) and replaces the
// original when the result is at least 20% smaller.
func RecodeVideos(ctx context.Context, files []RecodeFile, progress RecodeProgress) *RecodeReport {
	rep := &RecodeReport{Started: time.Now(), Files: []RecodeFile{}}
	for i, f := range files {
		if cerr := ctx.Err(); cerr != nil {
			rep.Files = append(rep.Files, RecodeFile{Path: f.Path, Old: f.Old, Status: "failed", Detail: "cancelled"})
			break
		}
		info, err := os.Stat(f.Path)
		if err != nil || !info.Mode().IsRegular() {
			rep.Files = append(rep.Files, RecodeFile{Path: f.Path, Old: f.Old, Status: "failed", Detail: "the file disappeared"})
			continue
		}
		nf := RecodeFile{Path: f.Path, Old: info.Size()}
		if size, detail := recodeOne(ctx, f.Path, info); size > 0 {
			nf.New, nf.Status = size, "recoded"
			rep.Saved += nf.Old - size
		} else {
			nf.Status, nf.Detail = "kept", detail
		}
		rep.Files = append(rep.Files, nf)
		if progress != nil {
			progress("recode", int64(i+1), int64(len(files)))
		}
	}
	return rep
}

// recodeOne re-encodes path; it returns the new size (0: keep the original)
// and why the file was kept.
func recodeOne(ctx context.Context, path string, info os.FileInfo) (int64, string) {
	tmp := path + ".strata-recode.tmp"
	os.Remove(tmp)
	defer os.Remove(tmp)
	if err := ffmpegEncode(ctx, path, tmp, filepath.Ext(path)); err != nil {
		return 0, err.Error()
	}
	nf, err := os.Stat(tmp)
	if err != nil || !nf.Mode().IsRegular() {
		return 0, "unreadable result"
	}
	if nf.Size() > int64(RecodeKeepRatio*float64(info.Size())) {
		return 0, "the re-encode is not smaller enough"
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err.Error()
	}
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	return nf.Size(), ""
}

// ffmpegEncode writes the H.265 copy of src to dst. Audio and subtitles are
// streamed as they are; a container refusing one of them gets a second run
// with everything transcoded.
func ffmpegEncode(ctx context.Context, src, dst, ext string) error {
	run := func(extra ...string) error {
		rctx, cancel := context.WithTimeout(ctx, maxRecodeRun)
		defer cancel()
		args := append([]string{"-y", "-i", src, "-map", "0", "-c:v", "libx265", "-crf", "28", "-preset", "medium"}, extra...)
		args = append(args, dst)
		cmd := exec.CommandContext(rctx, "ffmpeg", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := string(out)
			if i := strings.LastIndex(msg, "\n"); i >= 0 {
				msg = msg[i+1:]
			}
			return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(msg))
		}
		return nil
	}
	audio := []string{"-c:a", "copy", "-c:s", "copy"}
	if strings.EqualFold(ext, ".mp4") || strings.EqualFold(ext, ".m4v") || strings.EqualFold(ext, ".mov") {
		audio = append(audio, "-movflags", "+faststart")
	}
	if err := run(audio...); err == nil {
		return nil
	}
	if strings.EqualFold(ext, ".mkv") {
		return run("-c:a", "aac", "-b:a", "192k", "-c:s", "srt")
	}
	return run("-c:a", "aac", "-b:a", "192k", "-c:s", "mov_text", "-movflags", "+faststart")
}
