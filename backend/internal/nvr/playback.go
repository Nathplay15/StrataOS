package nvr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

// Browser playback of recordings. Browsers play H.264 (with AAC or MP3
// sound) in MP4; cameras also send H.265/HEVC, and HTTP cameras MJPEG in
// Matroska. Such a recording is sent through ffmpeg as fragmented MP4, the
// video copied when it is H.264 and transcoded to H.264 otherwise (VA-API
// when the NAS has a render node, else libx264).
//
// Everything that runs ffmpeg for playback is here, in the same terms as the
// shared player package of the Files viewer (internal/mediaplay: Probe, a
// plan, a start offset): switching to it later only changes this file.

// Probe is what playback needs to know about a file.
type Probe struct {
	Video    string  `json:"video"` // codec name (h264, hevc, mjpeg…), "" without video
	Audio    string  `json:"audio,omitempty"`
	Duration float64 `json:"duration"`
}

// ProbeArgs is the ffprobe command line reading the streams of a file.
func ProbeArgs(path string) []string {
	return []string{"-v", "error", "-protocol_whitelist", "file", "-show_entries", "stream=codec_type,codec_name:format=duration", "-of", "json", "-i", path}
}

// ParseProbe reads the JSON output of ProbeArgs (the first video and audio streams).
func ParseProbe(b []byte) (Probe, error) {
	var out struct {
		Streams []struct {
			Type  string `json:"codec_type"`
			Codec string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Probe{}, errors.New("unreadable recording")
	}
	var p Probe
	for _, s := range out.Streams {
		switch {
		case s.Type == "video" && p.Video == "":
			p.Video = s.Codec
		case s.Type == "audio" && p.Audio == "":
			p.Audio = s.Codec
		}
	}
	if p.Video == "" {
		return p, errors.New("the recording has no video")
	}
	p.Duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	return p, nil
}

// Direct reports whether a browser plays the file as it is (no ffmpeg).
func (p Probe) Direct(name string) bool {
	return strings.HasSuffix(name, ".mp4") && p.Video == "h264" && browserAudio(p.Audio)
}

func browserAudio(codec string) bool { return codec == "" || codec == "aac" || codec == "mp3" }

// PlayOpts is how a recording is sent to the browser.
type PlayOpts struct {
	Start float64 // seconds from the start of the file
	// VAAPI is the render node used to encode H.264 (empty: libx264).
	VAAPI string
}

// PlaybackArgs builds the ffmpeg command line writing the recording to
// stdout as fragmented MP4 (playable while it is written).
func PlaybackArgs(path string, p Probe, o PlayOpts) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file"}
	copyVideo := p.Video == "h264"
	if !copyVideo && o.VAAPI != "" {
		a = append(a, "-vaapi_device", o.VAAPI)
	}
	if o.Start > 0 {
		a = append(a, "-ss", strconv.FormatFloat(o.Start, 'f', 3, 64))
	}
	a = append(a, "-i", path, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn")
	switch {
	case copyVideo:
		a = append(a, "-c:v", "copy")
	case o.VAAPI != "":
		a = append(a, "-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi", "-qp", "24", "-g", "50")
	default:
		// Cameras can be 4K: the browser view does not need more than 1080p.
		a = append(a, "-vf", "scale='min(1920,iw)':-2", "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
			"-crf", "23", "-pix_fmt", "yuv420p", "-g", "50")
	}
	if browserAudio(p.Audio) {
		a = append(a, "-c:a", "copy")
	} else {
		a = append(a, "-c:a", "aac", "-b:a", "96k")
	}
	return append(a, "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
}

// RenderNode is the first GPU render node (VA-API), or "".
func RenderNode() string {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	sort.Strings(nodes)
	if len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}

// probeCache keeps the probes of recordings: a closed segment never changes,
// so it is probed once (keyed by path, size and modification time).
var probeCache = struct {
	sync.Mutex
	m map[string]Probe
}{m: map[string]Probe{}}

// ProbeFile probes a recording, from the cache when it is known.
func ProbeFile(ctx context.Context, path string, info os.FileInfo) (Probe, error) {
	key := path + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	probeCache.Lock()
	p, ok := probeCache.m[key]
	probeCache.Unlock()
	if ok {
		return p, nil
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 20 * time.Second}, "ffprobe", ProbeArgs(path)...)
	if err != nil {
		return Probe{}, errors.New("unreadable recording")
	}
	if p, err = ParseProbe([]byte(out)); err != nil {
		return p, err
	}
	probeCache.Lock()
	if len(probeCache.m) > 20000 {
		probeCache.m = map[string]Probe{}
	}
	probeCache.m[key] = p
	probeCache.Unlock()
	return p, nil
}

// countWriter counts what ffmpeg wrote.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// Play writes the recording to w as fragmented MP4. A VA-API encoder that
// fails before writing anything (driver without H.264 encoding) falls back
// to libx264.
func Play(ctx context.Context, w io.Writer, path string, p Probe, start float64) error {
	o := PlayOpts{Start: start}
	if p.Video != "h264" {
		o.VAAPI = RenderNode()
	}
	for {
		cw := &countWriter{w: w}
		cmd := exec.CommandContext(ctx, "ffmpeg", PlaybackArgs(path, p, o)...)
		cmd.Stdout = cw
		var stderr strings.Builder
		cmd.Stderr = &limitedWriter{b: &stderr, max: 4096}
		err := cmd.Run()
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if o.VAAPI != "" && cw.n == 0 {
			o.VAAPI = ""
			continue
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New("ffmpeg: " + msg)
	}
}

type limitedWriter struct {
	b   *strings.Builder
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
