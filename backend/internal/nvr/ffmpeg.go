package nvr

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SegmentLayout is the strftime pattern of segment names (local start time).
const SegmentLayout = "%Y%m%d-%H%M%S"

// DefaultSegmentSeconds: 10-minute segments.
const DefaultSegmentSeconds = 600

// Ext is the container of a camera's segments: MP4 for RTSP (H.264 plays in
// every browser), Matroska for HTTP streams (MJPEG and other codecs MP4 does
// not hold well).
func Ext(clean string) string {
	if HTTPStream(clean) {
		return ".mkv"
	}
	return ".mp4"
}

// RecordOpts describes a continuous recording.
type RecordOpts struct {
	Input          string // ffconcat file (see ConcatList)
	Dir            string // segment folder of the camera
	Ext            string // .mp4 | .mkv
	SegmentSeconds int
	// Audio is re-encoded to AAC (cameras often send G.711, which MP4 cannot hold);
	// the video is always copied as is.
	Audio bool
	// Stdin leaves ffmpeg's stdin connected: the recorder can then be told to
	// roll its segment ('q', bound to a split by the segment muxer) so an
	// event clip can be cut from closed files.
	Stdin bool
}

func inputArgs(input string, keyOnly bool) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file"}
	if keyOnly {
		a = append(a, "-skip_frame", "nokey")
	}
	return append(a, "-f", "concat", "-safe", "0", "-i", input)
}

// RecordArgs builds the ffmpeg command line of a recording: stream copy into
// segments cut on the clock (10:00, 10:10…) and named by their start time.
func RecordArgs(o RecordOpts) []string {
	secs := o.SegmentSeconds
	if secs <= 0 {
		secs = DefaultSegmentSeconds
	}
	a := inputArgs(o.Input, false)
	if o.Stdin {
		a = a[1:] // drop -nostdin: the pipe is how the segment is rolled on demand
	}
	a = append(a, "-map", "0:v:0")
	if o.Audio {
		a = append(a, "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "64k")
	} else {
		a = append(a, "-an", "-c:v", "copy")
	}
	format := "mp4"
	if o.Ext == ".mkv" {
		format = "matroska"
	}
	return append(a, "-f", "segment", "-segment_time", strconv.Itoa(secs), "-segment_atclocktime", "1",
		"-reset_timestamps", "1", "-strftime", "1", "-segment_format", format,
		filepath.Join(o.Dir, SegmentLayout+o.Ext))
}

// SnapshotArgs grabs one key frame (a clean picture) into out (JPEG).
func SnapshotArgs(input, out string) []string {
	a := inputArgs(input, true)
	return append(a, "-an", "-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", "-q:v", "5", "-f", "image2", "-y", out)
}

// LiveArgs keeps out (JPEG) updated with one picture per second for the live
// view. A sub-stream is small enough to decode fully; a main stream only has
// its key frames decoded, which costs little even on a small NAS.
func LiveArgs(input, out string, sub bool) []string {
	a := inputArgs(input, !sub)
	return append(a, "-an", "-vf", "fps=1,scale='min(960,iw)':-2", "-q:v", "6",
		"-f", "image2", "-update", "1", "-atomic_writing", "1", "-y", out)
}

// ClipArgs cuts [from, from+dur) seconds out of a segment, without re-encoding,
// as fragmented MP4 written to stdout (streamed to the browser as it comes).
func ClipArgs(segment string, from, dur int) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file", "-ss", strconv.Itoa(from), "-i", segment,
		"-t", strconv.Itoa(dur), "-map", "0", "-c", "copy", "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1"}
}

// clipOutArgs cuts [from, from+dur) out of one input (a segment or a concat
// list of segments) into the file out, without re-encoding, in the container
// matching out's extension.
func clipOutArgs(input, out string, from, dur int, concat bool) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file"}
	if concat {
		a = append(a, "-f", "concat", "-safe", "0")
	}
	a = append(a, "-ss", strconv.Itoa(from), "-i", input, "-t", strconv.Itoa(dur), "-map", "0", "-c", "copy")
	if filepath.Ext(out) == ".mkv" {
		return append(a, "-f", "matroska", "-y", out)
	}
	return append(a, "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "-y", out)
}

// ClipFileArgs cuts [from, from+dur) seconds out of one segment into out.
func ClipFileArgs(segment, out string, from, dur int) []string {
	return clipOutArgs(segment, out, from, dur, false)
}

// ConcatClipArgs cuts [from, from+dur) seconds out of a concat list of
// segments (an ffconcat file of local files) into out.
func ConcatClipArgs(list, out string, from, dur int) []string {
	return clipOutArgs(list, out, from, dur, true)
}

// ConcatFiles writes the ffconcat list of local files (unique name: cameras
// cut their clips at the same time) and returns its path. Remove it after use.
func ConcatFiles(files []string) (string, error) {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("file '" + strings.ReplaceAll(f, "'", `'\''`) + "'\n")
	}
	if err := os.MkdirAll(RunDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(RunDir, "clip-*.ffconcat")
	if err != nil {
		return "", err
	}
	p := f.Name()
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(p)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return "", err
	}
	_ = os.Chmod(p, 0o600)
	return p, nil
}
