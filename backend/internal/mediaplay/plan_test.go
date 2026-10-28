package mediaplay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func info(format string, streams ...Stream) *Info {
	for i := range streams {
		streams[i].Index = i
	}
	return &Info{Format: format, Duration: 60, Streams: streams}
}

var (
	h264   = Stream{Type: "video", Codec: "h264", Profile: "High", PixFmt: "yuv420p"}
	h26410 = Stream{Type: "video", Codec: "h264", Profile: "High 10", PixFmt: "yuv420p10le"}
	hevc   = Stream{Type: "video", Codec: "hevc", Profile: "Main", PixFmt: "yuv420p"}
	vp9    = Stream{Type: "video", Codec: "vp9", PixFmt: "yuv420p"}
	mpeg2  = Stream{Type: "video", Codec: "mpeg2video", PixFmt: "yuv420p", Interlaced: true}
	aac    = Stream{Type: "audio", Codec: "aac", Channels: 2}
	ac3    = Stream{Type: "audio", Codec: "ac3", Channels: 6}
	opus   = Stream{Type: "audio", Codec: "opus", Channels: 2}
	wma    = Stream{Type: "audio", Codec: "wmav2", Channels: 2}
	alac   = Stream{Type: "audio", Codec: "alac", Channels: 2}
	flac   = Stream{Type: "audio", Codec: "flac", Channels: 2}
	cover  = Stream{Type: "video", Codec: "mjpeg", Picture: true}
)

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		in                      *Info
		caps                    Caps
		mode                    Mode
		copyV, copyA, audioOnly bool
	}{
		{"film.mp4", info("mov,mp4", h264, aac), Caps{}, Native, false, false, false},
		{"film.webm", info("matroska,webm", vp9, opus), Caps{}, Native, false, false, false},
		{"film.mkv", info("matroska,webm", h264, aac), Caps{}, Remux, true, true, false},
		{"film.mov", info("mov,mp4", h264, aac), Caps{}, Remux, true, true, false},
		{"film.mkv", info("matroska,webm", h264, ac3), Caps{}, Transcode, true, false, false},
		{"film.mkv", info("matroska,webm", hevc, aac), Caps{}, Transcode, false, true, false},
		{"film.mkv", info("matroska,webm", hevc, aac), Caps{HEVC: true}, Remux, true, true, false},
		{"film.mp4", info("mov,mp4", hevc, aac), Caps{HEVC: true}, Native, false, false, false},
		{"film.mp4", info("mov,mp4", h26410, aac), Caps{}, Transcode, false, true, false},
		{"film.mpg", info("mpeg", mpeg2, Stream{Type: "audio", Codec: "mp2"}), Caps{}, Transcode, false, false, false},
		{"film.wmv", info("asf", Stream{Type: "video", Codec: "wmv3"}, wma), Caps{}, Transcode, false, false, false},
		{"song.mp3", info("mp3", Stream{Type: "audio", Codec: "mp3"}, cover), Caps{}, Native, false, false, true},
		{"song.flac", info("flac", flac), Caps{}, Native, false, false, true},
		{"song.wma", info("asf", wma), Caps{}, Transcode, false, false, true},
		{"song.m4a", info("mov,mp4", alac), Caps{}, Transcode, false, false, true},
		{"song.mka", info("matroska", opus), Caps{}, Remux, false, true, true},
		{"song.wav", info("wav", Stream{Type: "audio", Codec: "pcm_s16le"}), Caps{}, Native, false, false, true},
	} {
		p, err := Decide(tc.in, tc.name, tc.caps, -1)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if p.Mode != tc.mode || (p.Mode != Native && (p.CopyVideo != tc.copyV || p.CopyAudio != tc.copyA)) || p.AudioOnly != tc.audioOnly {
			t.Errorf("%s %v: got %s copyV=%v copyA=%v audioOnly=%v, want %s %v %v %v", tc.name, tc.in.Streams, p.Mode, p.CopyVideo, p.CopyAudio, p.AudioOnly, tc.mode, tc.copyV, tc.copyA, tc.audioOnly)
		}
	}
	// Another audio track than the default one: never native.
	in := info("mov,mp4", h264, aac, ac3)
	p, _ := Decide(in, "film.mp4", Caps{}, 2)
	if p.Mode != Transcode || p.Audio.Index != 2 || !p.CopyVideo {
		t.Errorf("second audio track: %+v", p)
	}
	if _, err := Decide(in, "film.mp4", Caps{}, 7); err == nil {
		t.Error("missing audio track accepted")
	}
	if _, err := Decide(info("data", Stream{Type: "subtitle", Codec: "subrip"}), "x.srt", Caps{}, -1); err == nil {
		t.Error("file without audio or video accepted")
	}
}

func TestArgs(t *testing.T) {
	p, _ := Decide(info("matroska", hevc, ac3), "a.mkv", Caps{}, -1)
	sw := strings.Join(Args(p, 90.5, HW{}), " ")
	for _, want := range []string{"-ss 90.500 -i fd:", "-map 0:0 -map 0:1", "libx264", "min(ih,1080)", "-c:a aac", "frag_keyframe+empty_moov", "pipe:1"} {
		if !strings.Contains(sw, want) {
			t.Errorf("software args miss %q: %s", want, sw)
		}
	}
	hw := strings.Join(Args(p, 0, HW{VAAPI: true, Device: "/dev/dri/renderD128"}), " ")
	for _, want := range []string{"-init_hw_device vaapi=va:/dev/dri/renderD128", "hwupload,scale_vaapi", "h264_vaapi"} {
		if !strings.Contains(hw, want) {
			t.Errorf("VAAPI args miss %q: %s", want, hw)
		}
	}
	if strings.Contains(hw, "-ss") {
		t.Error("start 0 must not seek")
	}
	p, _ = Decide(info("matroska", hevc, aac), "a.mkv", Caps{HEVC: true}, -1)
	cp := strings.Join(Args(p, 0, HW{VAAPI: true, Device: "/dev/dri/renderD128"}), " ")
	if !strings.Contains(cp, "-c:v copy -tag:v hvc1") || strings.Contains(cp, "vaapi") {
		t.Errorf("remux args: %s", cp)
	}
	p, _ = Decide(info("matroska", mpeg2, aac), "a.mpg", Caps{}, -1)
	if a := strings.Join(Args(p, 0, HW{}), " "); !strings.Contains(a, "yadif,") {
		t.Errorf("interlaced video not deinterlaced: %s", a)
	}
	p, _ = Decide(info("asf", wma), "a.wma", Caps{}, -1)
	if a := strings.Join(Args(p, 0, HW{}), " "); !strings.Contains(a, "-vn") || strings.Contains(a, "-c:v") {
		t.Errorf("audio args: %s", a)
	}
}

func TestParseProbe(t *testing.T) {
	in, err := ParseProbe([]byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","profile":"Main","pix_fmt":"yuv420p","width":1920,"height":1080,"field_order":"tt"},
{"index":1,"codec_type":"audio","codec_name":"ac3","channels":6,"tags":{"language":"fre"},"disposition":{"default":1}},
{"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng","title":"Full"}},
{"index":3,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle"},
{"index":4,"codec_type":"attachment","codec_name":"ttf"}],"format":{"format_name":"matroska,webm","duration":"5400.25"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Duration != 5400.25 || len(in.Streams) != 4 || !in.Streams[0].Interlaced || in.Streams[1].Lang != "fre" || !in.Streams[1].Default {
		t.Errorf("parsed %+v", in)
	}
	if subs := in.Subtitles(); len(subs) != 1 || subs[0].Index != 2 || subs[0].Title != "Full" {
		t.Errorf("text subtitles: %+v", subs)
	}
}

func TestShiftVTT(t *testing.T) {
	in := "WEBVTT\n\n00:00:05.000 --> 00:00:07.000\nfirst\n\n00:01:00.500 --> 00:01:02.000 align:start\nsecond\n\n01:00:00.000 --> 01:00:01.000\nthird"
	got := string(ShiftVTT([]byte(in), 60))
	want := "WEBVTT\n\n00:00:00.500 --> 00:00:02.000 align:start\nsecond\n\n00:59:00.000 --> 00:59:01.000\nthird"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if string(ShiftVTT([]byte(in), 0)) != in {
		t.Error("offset 0 changed the file")
	}
}

// TestStreamWithFFmpeg plays a real HEVC/AC-3 Matroska file when ffmpeg is installed.
func TestStreamWithFFmpeg(t *testing.T) {
	if !Installed() {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mkv")
	gen := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=f=440", "-t", "4", "-c:v", "mpeg4", "-c:a", "ac3", "-y", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot generate a test file: %v %s", err, out)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := Probe(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if in.Duration < 3.5 {
		t.Errorf("duration %v", in.Duration)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/play?path=x&start=2", nil)
	if err := ServeFile(rec, req, f, src); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.Bytes()
	if rec.Header().Get("Content-Type") != "video/mp4" || len(body) < 1000 || string(body[4:8]) != "ftyp" || !strings.Contains(string(body[:4096]), "moof") {
		t.Errorf("not a fragmented MP4: %q %d bytes", rec.Header().Get("Content-Type"), len(body))
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

func TestWithHeight(t *testing.T) {
	tall := h264
	tall.Height = 2160
	p, _ := Decide(info("mov,mp4", tall, aac), "film.mp4", Caps{}, -1)
	if p.Mode != Native {
		t.Fatalf("film.mp4: %s", p.Mode)
	}
	q := WithHeight(p, 720)
	if q.Mode != Transcode || q.CopyVideo || !q.CopyAudio || q.Height != 720 {
		t.Errorf("720p: %+v", q)
	}
	if a := strings.Join(Args(q, 0, HW{}), " "); !strings.Contains(a, "min(ih,720)") {
		t.Errorf("720p args: %s", a)
	}
	if WithHeight(p, 0).Mode != Native || WithHeight(p, 123).Mode != Native {
		t.Error("no or unknown quality must keep the plan")
	}
	small := h264
	small.Height = 480
	p, _ = Decide(info("mov,mp4", small, aac), "film.mp4", Caps{}, -1)
	if WithHeight(p, 720).Mode != Native {
		t.Error("a video smaller than the quality asked must not be transcoded")
	}
	p, _ = Decide(info("mp3", Stream{Type: "audio", Codec: "mp3"}), "song.mp3", Caps{}, -1)
	if WithHeight(p, 360).Mode != Native {
		t.Error("audio must not be transcoded for a picture height")
	}
}
