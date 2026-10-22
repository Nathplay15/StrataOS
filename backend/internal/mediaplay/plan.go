// Package mediaplay plays any audio or video file in a web browser. Files the
// browser decodes are served as they are; the others are rewritten on the fly
// by ffmpeg into fragmented MP4: remuxed (stream copy, cheap) when their codecs
// are playable, otherwise transcoded to H.264/AAC (VAAPI when available).
// Seeking restarts the stream at the requested time (start parameter).
//
// Usage from an HTTP handler, with a file the caller opened and checked:
//
//	mediaplay.ServeFile(w, r, f, name) // native, remux or transcode, from r's query
//
// or, step by step: Probe, Decide, then Default.Stream.
package mediaplay

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Stream is one track of a media file, as ffprobe describes it.
type Stream struct {
	Index      int    `json:"index"`
	Type       string `json:"type"` // video, audio, subtitle
	Codec      string `json:"codec"`
	Profile    string `json:"profile,omitempty"`
	PixFmt     string `json:"pixFmt,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Lang       string `json:"lang,omitempty"`
	Title      string `json:"title,omitempty"`
	Default    bool   `json:"default,omitempty"`
	Interlaced bool   `json:"interlaced,omitempty"`
	// Picture: cover art of an audio file, not a video.
	Picture bool `json:"-"`
}

// Info is what Probe learns about a file.
type Info struct {
	Format   string   `json:"format"`
	Duration float64  `json:"duration"`
	Streams  []Stream `json:"streams"`
}

// ParseProbe reads the JSON output of ffprobe -show_format -show_streams.
func ParseProbe(data []byte) (*Info, error) {
	var raw struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Profile     string            `json:"profile"`
			PixFmt      string            `json:"pix_fmt"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			Channels    int               `json:"channels"`
			FieldOrder  string            `json:"field_order"`
			Duration    string            `json:"duration"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unreadable media information: %w", err)
	}
	info := &Info{Format: raw.Format.Name}
	info.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		st := Stream{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile, PixFmt: s.PixFmt, Width: s.Width, Height: s.Height, Channels: s.Channels,
			Lang: s.Tags["language"], Title: s.Tags["title"], Default: s.Disposition["default"] == 1, Picture: s.Disposition["attached_pic"] == 1}
		switch s.FieldOrder {
		case "tt", "bb", "tb", "bt":
			st.Interlaced = true
		}
		if st.Type != "video" && st.Type != "audio" && st.Type != "subtitle" {
			continue
		}
		if info.Duration == 0 {
			info.Duration, _ = strconv.ParseFloat(s.Duration, 64)
		}
		info.Streams = append(info.Streams, st)
	}
	if math.IsNaN(info.Duration) || info.Duration < 0 {
		info.Duration = 0
	}
	return info, nil
}

// Caps are optional codecs the browser announced it can decode (MediaSource
// isTypeSupported): HEVC on Safari, Edge and Chrome with hardware support…
type Caps struct {
	HEVC bool
	AV1  bool
}

// Mode says how a file reaches the browser.
type Mode string

const (
	Native    Mode = "native"    // the file itself, with Range requests
	Remux     Mode = "remux"     // stream copy into fragmented MP4
	Transcode Mode = "transcode" // at least one track re-encoded
)

// Plan is the decision for one file.
type Plan struct {
	Mode      Mode    `json:"mode"`
	AudioOnly bool    `json:"audioOnly"`
	Video     *Stream `json:"video,omitempty"`
	Audio     *Stream `json:"audio,omitempty"`
	CopyVideo bool    `json:"copyVideo"`
	CopyAudio bool    `json:"copyAudio"`
	// Type is the Content-Type sent: the file's own for Native.
	Type string `json:"type"`
}

// Containers browsers play from a plain URL, by extension.
var nativeVideo = map[string]string{".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg"}
var nativeAudio = map[string]string{".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".mp4": "audio/mp4", ".flac": "audio/flac",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav", ".webm": "audio/webm"}

// videoOK reports whether browsers decode a video track (8-bit H.264, VP8/9…).
func videoOK(s *Stream, c Caps) bool {
	switch s.Codec {
	case "h264":
		// 10-bit and 4:2:2/4:4:4 H.264 play almost nowhere.
		if s.PixFmt != "" && s.PixFmt != "yuv420p" && s.PixFmt != "yuvj420p" {
			return false
		}
		return !strings.Contains(s.Profile, "10") && !strings.Contains(s.Profile, "4:2:2") && !strings.Contains(s.Profile, "4:4:4")
	case "vp8", "vp9":
		return true
	case "av1":
		return c.AV1
	case "hevc":
		// Main and Main 10 in 4:2:0 only.
		return c.HEVC && (s.PixFmt == "" || s.PixFmt == "yuv420p" || s.PixFmt == "yuv420p10le") && (s.Profile == "" || s.Profile == "Main" || s.Profile == "Main 10")
	}
	return false
}

// audioInMP4 lists the audio codecs browsers play inside MP4.
func audioInMP4(codec string) bool {
	switch codec {
	case "aac", "mp3", "opus", "flac":
		return true
	}
	return false
}

// audioNative: codecs played natively in their own container.
func audioNative(ext, codec string) bool {
	switch ext {
	case ".mp3":
		return codec == "mp3"
	case ".m4a", ".mp4", ".aac":
		return codec == "aac" || codec == "mp3" || codec == "flac" || codec == "opus"
	case ".flac":
		return codec == "flac"
	case ".ogg", ".oga", ".opus":
		return codec == "vorbis" || codec == "opus" || codec == "flac"
	case ".webm":
		return codec == "vorbis" || codec == "opus"
	case ".wav":
		return codec == "pcm_s16le" || codec == "pcm_u8" || codec == "pcm_s24le" || codec == "pcm_f32le"
	}
	return false
}

// Decide chooses native, remux or transcode for a file of extension ext.
// audio picks an audio track by stream index (-1: the default one).
func Decide(info *Info, name string, c Caps, audio int) (Plan, error) {
	ext := strings.ToLower(filepath.Ext(name))
	var v, a *Stream
	var firstAudio *Stream
	for i := range info.Streams {
		s := &info.Streams[i]
		switch s.Type {
		case "video":
			if v == nil && !s.Picture {
				v = s
			}
		case "audio":
			if firstAudio == nil || (s.Default && !firstAudio.Default) {
				firstAudio = s
			}
			if s.Index == audio {
				a = s
			}
		}
	}
	if audio >= 0 && a == nil {
		return Plan{}, fmt.Errorf("no audio track %d", audio)
	}
	chosen := a != nil && a != firstAudio
	if a == nil {
		a = firstAudio
	}
	if v == nil && a == nil {
		return Plan{}, fmt.Errorf("no audio or video track")
	}
	p := Plan{Video: v, Audio: a}
	if v == nil {
		p.AudioOnly = true
		if t, ok := nativeAudio[ext]; ok && audioNative(ext, a.Codec) && !chosen {
			p.Mode, p.Type = Native, t
			return p, nil
		}
		p.Type = "audio/mp4"
		p.CopyAudio = audioInMP4(a.Codec)
		p.Mode = Transcode
		if p.CopyAudio {
			p.Mode = Remux
		}
		return p, nil
	}
	p.Type = "video/mp4"
	vok := videoOK(v, c)
	aok := a == nil || audioInMP4(a.Codec)
	if t, ok := nativeVideo[ext]; ok && vok && !chosen {
		native := false
		switch ext {
		case ".mp4", ".m4v":
			native = v.Codec != "vp8" && aok
		case ".webm":
			native = (v.Codec == "vp8" || v.Codec == "vp9" || v.Codec == "av1") && (a == nil || a.Codec == "opus" || a.Codec == "vorbis")
		case ".ogv":
			native = false // Theora: transcoded below anyway
		}
		if native {
			p.Mode, p.Type = Native, t
			return p, nil
		}
	}
	p.CopyVideo = vok && v.Codec != "vp8" // VP8 cannot go in MP4
	p.CopyAudio = a == nil || audioInMP4(a.Codec)
	p.Mode = Transcode
	if p.CopyVideo && p.CopyAudio {
		p.Mode = Remux
	}
	return p, nil
}

// HW describes the video encoder available.
type HW struct {
	VAAPI  bool
	Device string // render node, e.g. /dev/dri/renderD128
}

// MaxHeight bounds transcoded pictures: 1080p is plenty for a browser window
// and keeps a small NAS CPU real time.
const MaxHeight = 1080

// Args returns the ffmpeg arguments that write p as fragmented MP4 to stdout,
// reading the file from stdin (fd: protocol, seekable) from start seconds.
func Args(p Plan, start float64, hw HW) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	encodeVideo := !p.AudioOnly && !p.CopyVideo
	vaapi := encodeVideo && hw.VAAPI && hw.Device != ""
	if vaapi {
		args = append(args, "-init_hw_device", "vaapi=va:"+hw.Device, "-filter_hw_device", "va",
			"-hwaccel", "vaapi", "-hwaccel_device", "va", "-hwaccel_output_format", "vaapi")
	}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", "fd:")
	if p.AudioOnly {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Audio.Index), "-vn")
	} else {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Video.Index))
		if p.Audio != nil {
			args = append(args, "-map", "0:"+strconv.Itoa(p.Audio.Index))
		}
	}
	args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
	if !p.AudioOnly {
		switch {
		case p.CopyVideo:
			args = append(args, "-c:v", "copy")
			if p.Video.Codec == "hevc" {
				args = append(args, "-tag:v", "hvc1") // what Safari and Chrome expect in MP4
			}
		case vaapi:
			// hwupload passes hardware frames through and uploads frames that
			// were decoded in software (codecs the GPU does not decode).
			vf := "format=nv12|vaapi,hwupload"
			if p.Video.Interlaced {
				vf += ",deinterlace_vaapi"
			}
			vf += fmt.Sprintf(",scale_vaapi=w=-2:h='min(ih,%d)':format=nv12", MaxHeight)
			args = append(args, "-vf", vf, "-c:v", "h264_vaapi", "-qp", "24", "-g", "60")
		default:
			vf := ""
			if p.Video.Interlaced {
				vf = "yadif,"
			}
			vf += fmt.Sprintf("scale=-2:'min(ih,%d)',format=yuv420p", MaxHeight)
			args = append(args, "-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-g", "60", "-profile:v", "high")
		}
	}
	if p.Audio != nil {
		if p.CopyAudio {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		}
	}
	return append(args, "-strict", "experimental", "-max_muxing_queue_size", "4096",
		"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "2000000", "pipe:1")
}

// textSubtitles are the subtitle codecs ffmpeg turns into WebVTT; picture
// subtitles (DVD, Blu-ray PGS) would need burning into the video.
var textSubtitles = map[string]bool{"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true, "microdvd": true}

// Subtitles lists the text subtitle tracks of a file.
func (i *Info) Subtitles() []Stream {
	out := []Stream{}
	for _, s := range i.Streams {
		if s.Type == "subtitle" && textSubtitles[s.Codec] {
			out = append(out, s)
		}
	}
	return out
}

// AudioTracks lists the audio tracks of a file.
func (i *Info) AudioTracks() []Stream {
	out := []Stream{}
	for _, s := range i.Streams {
		if s.Type == "audio" {
			out = append(out, s)
		}
	}
	return out
}
