package nvr

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseURL(t *testing.T) {
	for _, c := range []struct{ in, clean, user, pass, err string }{
		{in: "rtsp://192.168.1.20:554/Streaming/Channels/101", clean: "rtsp://192.168.1.20:554/Streaming/Channels/101"},
		{in: " RTSP://cam.local/live?channel=1&subtype=0 ", clean: "rtsp://cam.local/live?channel=1&subtype=0"},
		{in: "rtsp://admin:s3cret@10.0.0.5/h264", clean: "rtsp://10.0.0.5/h264", user: "admin", pass: "s3cret"},
		{in: "rtsp://admin:p%40ss%3Aw@10.0.0.5/h264", clean: "rtsp://10.0.0.5/h264", user: "admin", pass: "p@ss:w"},
		{in: "http://[fe80::1]:8080/video.mjpg", clean: "http://[fe80::1]:8080/video.mjpg"},
		{in: "https://cam.example.com/mjpeg", clean: "https://cam.example.com/mjpeg"},
		{in: "", err: "empty"},
		{in: "file:///etc/shadow", err: "must start with"},
		{in: "concat:/etc/passwd", err: "must start with"},
		{in: "rtmp://cam/live", err: "must start with"},
		{in: "rtsp://cam/live stream", err: "spaces"},
		{in: "rtsp://cam/live'x", err: "invalid characters"},
		// Characters that are unsafe in YAML, ffconcat or Frigate's {FRIGATE_} syntax are escaped.
		{in: "rtsp://cam/{FRIGATE_X}", clean: "rtsp://cam/%7BFRIGATE_X%7D"},
		{in: "rtsp://cam/a\\b", clean: "rtsp://cam/a%5Cb"},
		{in: "rtsp://cam_bad!/x", err: "invalid camera address"},
		{in: "rtsp://cam:99999/x", err: "port"},
		{in: "rtsp://cam/x#frag", err: "invalid stream URL"},
		{in: "rtsp://cam/é", err: "spaces or invalid"},
	} {
		clean, user, pass, err := ParseURL(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: want error %q, got %v (%q)", c.in, c.err, err, clean)
			}
			continue
		}
		if err != nil || clean != c.clean || user != c.user || pass != c.pass {
			t.Errorf("%q: got %q %q %q %v", c.in, clean, user, pass, err)
		}
	}
}

func TestWithCredentials(t *testing.T) {
	for _, c := range []struct{ user, pass, want string }{
		{"", "", "rtsp://cam:554/live"},
		{"admin", "", "rtsp://admin@cam:554/live"},
		{"admin", "pass", "rtsp://admin:pass@cam:554/live"},
		{"ad min", "p@ss:w/rd?#'\"{}%é", "rtsp://ad%20min:p%40ss%3Aw%2Frd%3F%23%27%22%7B%7D%25%C3%A9@cam:554/live"},
	} {
		got := WithCredentials("rtsp://cam:554/live", c.user, c.pass)
		if got != c.want {
			t.Errorf("%q/%q: got %s", c.user, c.pass, got)
		}
		// The result round-trips through ParseURL.
		_, u, p, err := ParseURL(got)
		if err != nil || u != c.user || p != c.pass {
			t.Errorf("%q/%q: parsed back as %q/%q %v", c.user, c.pass, u, p, err)
		}
	}
	if err := ValidCredential("a\nb"); err == nil {
		t.Error("newline accepted in a credential")
	}
}

func TestRedact(t *testing.T) {
	got := Redact("[rtsp @ 0x1] method DESCRIBE failed: 401 rtsp://admin:p%40ss@10.0.0.5/h264")
	if strings.Contains(got, "admin") || strings.Contains(got, "p%40ss") || !strings.Contains(got, "rtsp://***@10.0.0.5/h264") {
		t.Errorf("not redacted: %s", got)
	}
}

func TestConcatList(t *testing.T) {
	l := ConcatList("rtsp://u:p%27@cam/live")
	for _, want := range []string{"ffconcat version 1.0\n", "file 'rtsp://u:p%27@cam/live'\n", "option protocol_whitelist rtsp,rtp,udp,tcp\n", "option rtsp_transport tcp\n"} {
		if !strings.Contains(l, want) {
			t.Errorf("missing %q in %s", want, l)
		}
	}
	if strings.Contains(l, "file,") || strings.Contains(l, ",file") {
		t.Error("the camera stream may open local files")
	}
	if l := ConcatList("http://cam/x'y"); !strings.Contains(l, `file 'http://cam/x'\''y'`) || !strings.Contains(l, "protocol_whitelist http,tcp\n") || !strings.Contains(l, "use_wallclock_as_timestamps 1") {
		t.Errorf("http list: %s", l)
	}
}

func TestRecordArgs(t *testing.T) {
	a := strings.Join(RecordArgs(RecordOpts{Input: "/run/strata/nvr/c1/main.ffconcat", Dir: "/srv/strata/main/cams/c1", Ext: ".mp4"}), " ")
	for _, want := range []string{
		"-protocol_whitelist file -f concat -safe 0 -i /run/strata/nvr/c1/main.ffconcat",
		"-map 0:v:0 -an -c:v copy",
		"-f segment -segment_time 600 -segment_atclocktime 1 -reset_timestamps 1 -strftime 1 -segment_format mp4",
		"/srv/strata/main/cams/c1/%Y%m%d-%H%M%S.mp4",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
	if strings.Contains(a, "rtsp://") || strings.Contains(a, "libx264") {
		t.Errorf("stream URL or transcoding in the command line: %s", a)
	}
	a = strings.Join(RecordArgs(RecordOpts{Input: "in", Dir: "/d", Ext: ".mkv", SegmentSeconds: 10, Audio: true}), " ")
	for _, want := range []string{"-map 0:a:0? -c:v copy -c:a aac", "-segment_time 10 ", "-segment_format matroska", "/d/%Y%m%d-%H%M%S.mkv"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
	if Ext("http://cam/mjpg") != ".mkv" || Ext("rtsp://cam/x") != ".mp4" {
		t.Error("wrong container")
	}
	if a := strings.Join(LiveArgs("in", "/run/x/live.jpg", false), " "); !strings.Contains(a, "-skip_frame nokey -f concat") || !strings.Contains(a, "-update 1 -atomic_writing 1") {
		t.Errorf("live main stream: %s", a)
	}
	if a := strings.Join(LiveArgs("in", "o.jpg", true), " "); strings.Contains(a, "skip_frame") {
		t.Errorf("live sub-stream skips frames: %s", a)
	}
	if a := strings.Join(SnapshotArgs("in", "o.jpg"), " "); !strings.Contains(a, "-skip_frame nokey") || !strings.Contains(a, "-frames:v 1") {
		t.Errorf("snapshot: %s", a)
	}
	if a := strings.Join(ClipArgs("/d/x.mp4", 30, 60), " "); !strings.Contains(a, "-ss 30 -i /d/x.mp4 -t 60") || !strings.Contains(a, "-c copy") {
		t.Errorf("clip: %s", a)
	}
}

func mk(name string, size int64, mtime string) FileInfo {
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", mtime, time.UTC)
	return FileInfo{Name: name, Size: size, MTime: t}
}

func TestSegments(t *testing.T) {
	if ts, ok := ParseSegmentName("20261021-235000.mp4", time.UTC); !ok || ts.Format(time.RFC3339) != "2026-10-21T23:50:00Z" {
		t.Errorf("parse: %v %v", ts, ok)
	}
	for _, bad := range []string{"20261021-2350.mp4", "x20261021-235000.mp4", "20261021-235000.avi", "20261341-235000.mp4", "../20261021-235000.mp4"} {
		if _, ok := ParseSegmentName(bad, time.UTC); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	files := []FileInfo{
		mk("20261022-000000.mp4", 300, "2026-10-22 00:04:00"), // newest, being written
		mk("20261021-235000.mp4", 200, "2026-10-22 00:00:01"),
		mk("20261021-234000.mp4", 100, "2026-10-21 23:50:00"),
		mk("notes.txt", 5, "2026-10-21 23:00:00"),
		mk("20261020-120000.mkv", 50, "2026-10-21 23:00:00"), // gap: ends at maxSegment
	}
	segs := BuildSegments(files, time.UTC, true)
	if len(segs) != 4 {
		t.Fatalf("got %d segments", len(segs))
	}
	got := []string{}
	for _, s := range segs {
		got = append(got, fmt.Sprintf("%s %s-%s %v", s.Name, s.Start.Format("01-02T15:04"), s.End.Format("01-02T15:04"), s.Open))
	}
	want := "20261020-120000.mkv 10-20T12:00-10-20T14:00 false|20261021-234000.mp4 10-21T23:40-10-21T23:50 false|20261021-235000.mp4 10-21T23:50-10-22T00:00 false|20261022-000000.mp4 10-22T00:00-10-22T00:04 true"
	if strings.Join(got, "|") != want {
		t.Errorf("segments:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	if d := Days(segs, time.UTC); strings.Join(d, ",") != "2026-10-22,2026-10-21,2026-10-20" {
		t.Errorf("days: %v", d)
	}
	day, _ := Day(segs, "2026-10-21", time.UTC)
	if len(day) != 2 || day[0].Name != "20261021-234000.mp4" || day[1].Name != "20261021-235000.mp4" {
		t.Errorf("day 21 (segment ending at midnight included): %v", day)
	}
	if _, err := Day(segs, "21/10/2026", time.UTC); err == nil {
		t.Error("bad day accepted")
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 5, 0, 0, time.UTC)
	files := []FileInfo{
		mk("20261010-000000.mp4", 1000, "2026-10-10 00:10:00"),
		mk("20261015-000000.mp4", 1000, "2026-10-15 00:10:00"),
		mk("20261021-000000.mp4", 1000, "2026-10-21 00:10:00"),
		mk("20261022-000000.mp4", 5000, "2026-10-22 00:04:00"),
	}
	names := func(s []Segment) string {
		var n []string
		for _, x := range s {
			n = append(n, x.Name[:8])
		}
		return strings.Join(n, ",")
	}
	open := BuildSegments(files, time.UTC, true)
	closed := BuildSegments(files, time.UTC, false)
	for _, c := range []struct {
		segs  []Segment
		days  int
		max   int64
		want  string
		label string
	}{
		{open, 0, 0, "", "no limit"},
		{open, 7, 0, "20261010", "7 days (the 15th ended less than 7 days ago)"},
		{open, 6, 0, "20261010,20261015", "6 days"},
		{open, 1, 0, "20261010,20261015", "1 day"},
		{open, 0, 7000, "20261010", "size"},
		{open, 0, 6000, "20261010,20261015", "size 6000"},
		{open, 0, 100, "20261010,20261015,20261021", "size never takes the open segment"},
		{closed, 0, 100, "20261010,20261015,20261021,20261022", "stopped camera"},
		{open, 30, 7000, "20261010", "days and size"},
	} {
		if got := names(Expired(c.segs, now, c.days, c.max)); got != c.want {
			t.Errorf("%s: got %q want %q", c.label, got, c.want)
		}
	}
}

func TestFrigateNames(t *testing.T) {
	n := FrigateNames([]string{"a", "b", "c", "d", "e"}, []string{"Front door", "front-door", "Été 2", "123", "!!!"})
	want := map[string]string{"a": "front_door", "b": "front_door_2", "c": "t_2", "d": "cam_123", "e": "cam"}
	for k, v := range want {
		if n[k] != v {
			t.Errorf("%s: got %q want %q", k, n[k], v)
		}
	}
}

func TestFrigateConfig(t *testing.T) {
	o := FrigateOptions{Detector: "openvino", Objects: []string{"person", "dog"}, Cameras: []FrigateCamera{
		{Name: "front_door", Main: "rtsp://10.0.0.5:554/main", Sub: "rtsp://10.0.0.5:554/sub", User: "admin", Pass: "p@ss'\"{x}", KeepDays: 14},
		{Name: "garden", Main: "rtsp://10.0.0.6/live"},
	}}
	y := FrigateConfig(o)
	for _, want := range []string{
		"mqtt:\n  enabled: false\n",
		"  ov:\n    type: openvino\n    device: GPU\n",
		"path: /openvino-model/ssdlite_mobilenet_v2.xml",
		"  track:\n    - \"person\"\n    - \"dog\"\n",
		"  front_door:\n    enabled: true\n",
		`- path: "rtsp://{FRIGATE_FRONT_DOOR_USER}:{FRIGATE_FRONT_DOOR_PASS}@10.0.0.5:554/main"` + "\n          roles:\n            - record\n",
		`- path: "rtsp://{FRIGATE_FRONT_DOOR_USER}:{FRIGATE_FRONT_DOOR_PASS}@10.0.0.5:554/sub"` + "\n          roles:\n            - detect\n",
		"      continuous:\n        days: 14\n",
		`- path: "rtsp://10.0.0.6/live"` + "\n          roles:\n            - detect\n            - record\n",
		"        days: 3650\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in:\n%s", want, y)
		}
	}
	if strings.Contains(y, "p@ss") || strings.Contains(y, "admin") {
		t.Errorf("credentials in the configuration:\n%s", y)
	}
	sec := FrigateSecrets(o)
	if len(sec) != 2 || sec["FRIGATE_FRONT_DOOR_USER"] != "admin" || sec["FRIGATE_FRONT_DOOR_PASS"] != "p%40ss%27%22%7Bx%7D" {
		t.Errorf("secrets: %v", sec)
	}
	if y := FrigateConfig(FrigateOptions{Detector: "cpu"}); !strings.Contains(y, "type: cpu") || !strings.Contains(y, "cameras: {}") || !strings.Contains(y, `- "person"`) {
		t.Errorf("empty config:\n%s", y)
	}
	if y := FrigateConfig(FrigateOptions{Detector: "edgetpu"}); !strings.Contains(y, "type: edgetpu\n    device: usb") {
		t.Errorf("coral:\n%s", y)
	}
	for _, c := range []struct {
		choice, gpu string
		coral       bool
		want        string
	}{{"", "", false, "cpu"}, {"", "/dev/dri/renderD128", false, "openvino"}, {"", "/dev/dri/renderD128", true, "edgetpu"}, {"cpu", "/dev/dri/renderD128", true, "cpu"}, {"bogus", "", false, "cpu"}} {
		if got := ResolveDetector(c.choice, c.gpu, c.coral); got != c.want {
			t.Errorf("detector %+v: %s", c, got)
		}
	}
}

func TestEventFilter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := NewEventFilter(now)
	ev := func(id, cam string, at int) FrigateEvent {
		return FrigateEvent{ID: id, Camera: cam, Label: "person", StartTime: float64(now.Unix() + int64(at))}
	}
	// Events from before StrataOS started polling are not reported.
	if got := f.New([]FrigateEvent{ev("old", "a", -3600), ev("e1", "a", 5), ev("e2", "b", 8)}); len(got) != 2 || got[0].ID != "e1" || got[1].ID != "e2" {
		t.Fatalf("first poll: %+v", got)
	}
	if f.After() != float64(now.Unix()+8-eventOverlap) {
		t.Errorf("cursor: %v", f.After())
	}
	// The overlap lists e1 and e2 again: they are not repeated.
	if got := f.New([]FrigateEvent{ev("e3", "a", 20), ev("e2", "b", 8), ev("e1", "a", 5)}); len(got) != 1 || got[0].ID != "e3" {
		t.Errorf("second poll: %+v", got)
	}
	// An event that started late but within the overlap is still reported once.
	if got := f.New([]FrigateEvent{ev("late", "b", 10), ev("", "b", 30)}); len(got) != 1 || got[0].ID != "late" {
		t.Errorf("late event: %+v", got)
	}
	// Memory stays bounded.
	f.New([]FrigateEvent{ev("far", "a", 2*eventOverlap)})
	if len(f.seen) != 1 {
		t.Errorf("seen not pruned: %d", len(f.seen))
	}
	// Rate limit per camera.
	every := 10 * time.Minute
	if !f.ShouldNotify("a", now, every) || f.ShouldNotify("a", now.Add(time.Minute), every) || !f.ShouldNotify("b", now.Add(time.Minute), every) || !f.ShouldNotify("a", now.Add(11*time.Minute), every) {
		t.Error("rate limit")
	}
	e := FrigateEvent{}
	e.Data.TopScore = 0.87
	if e.Score() != 0.87 {
		t.Error("score")
	}
}
