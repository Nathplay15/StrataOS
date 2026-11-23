package nvr

import (
	"strings"
	"testing"
	"time"
)

func TestSceneScore(t *testing.T) {
	line := []byte("[Parsed_metadata_2 @ 0x55c0f1a2b3c0] lavfi.scene_score=0.411203")
	m := sceneRe.FindSubmatch(line)
	if m == nil || string(m[1]) != "0.411203" {
		t.Errorf("score not parsed: %q", m)
	}
	if sceneRe.FindSubmatch([]byte("[Parsed_metadata_2 @ 0x1] frame:1 pts:1 pts_time:1")) != nil {
		t.Error("a line without a score matched")
	}
	if sceneRe.FindSubmatch([]byte("[Parsed_metadata_2 @ 0x1] lavfi.scene_score=0.028000")) == nil {
		t.Error("a quiet score line should still match (the caller filters)")
	}
}

func TestMotionFilterEscapesTheComma(t *testing.T) {
	// The filter is passed to exec (no shell): only ffmpeg's own escaping.
	f := strings.Replace("fps=1,select=gt(scene\\,0.35),metadata=print", "{0}", "0.35", 1)
	if !strings.Contains(f, `gt(scene\,0.35)`) {
		t.Errorf("comma not escaped: %s", f)
	}
}

func TestMotionEveryNotZero(t *testing.T) {
	if motionEvery < 10*time.Second || motionThreshold <= 0 || motionThreshold >= 1 {
		t.Error("motion constants out of range")
	}
}

func TestParseZones(t *testing.T) {
	if z, err := ParseZones(""); err != nil || z != nil {
		t.Errorf("empty: z=%v err=%v", z, err)
	}
	if z, err := ParseZones("   "); err != nil || z != nil {
		t.Errorf("blank: z=%v err=%v", z, err)
	}
	z, err := ParseZones(`[{"x":0.1,"y":0.2,"w":0.5,"h":0.6}]`)
	if err != nil || len(z) != 1 || z[0] != (Zone{X: 0.1, Y: 0.2, W: 0.5, H: 0.6}) {
		t.Errorf("one zone: z=%v err=%v", z, err)
	}
	if _, err := ParseZones(`[{"x":0,"y":0,"w":0.5,"h":0.5}]`); err != nil {
		t.Errorf("valid array rejected: %v", err)
	}
	for bad, why := range map[string]string{
		`not json`:                           "not json",
		`[{}]`:                               "missing fields count as zero size",
		`[{"x":0,"y":0,"w":0.01,"h":0.5}]`:   "too narrow",
		`[{"x":0,"y":0,"w":0.5,"h":0.01}]`:   "too flat",
		`[{"x":-0.1,"y":0,"w":0.5,"h":0.5}]`: "negative x",
		`[{"x":0.9,"y":0,"w":0.5,"h":0.5}]`:  "x+w > 1",
		`[{"x":0,"y":0.9,"w":0.5,"h":0.5}]`:  "y+h > 1",
		`[{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2}]`: "too many",
	} {
		if _, err := ParseZones(bad); err == nil {
			t.Errorf("%s: accepted %q", why, bad)
		}
	}
	// 4 zones are fine.
	four := `[{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0.3,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0.3,"w":0.2,"h":0.2},{"x":0.3,"y":0.3,"w":0.2,"h":0.2}]`
	if z, err := ParseZones(four); err != nil || len(z) != 4 {
		t.Errorf("four zones: z=%v err=%v", z, err)
	}
}

func TestMotionArgsNoZonesUnchanged(t *testing.T) {
	want := []string{"-hide_banner", "-nostdin", "-loglevel", "info", "-i", "rtsp://cam",
		"-vf", `fps=1,select=gt(scene\,0.35),metadata=print`, "-an", "-sn", "-dn", "-f", "null", "-"}
	got := motionArgs("rtsp://cam", nil)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("no-zone args changed:\n got %v\nwant %v", got, want)
	}
}

func TestMotionArgsWithZones(t *testing.T) {
	zones := []Zone{{X: 0.1, Y: 0.2, W: 0.5, H: 0.6}, {X: 0, Y: 0, W: 0.3, H: 0.4}}
	args := motionArgs("rtsp://cam", zones)
	fc := ""
	for i, a := range args {
		if a == "-filter_complex" && i+1 < len(args) {
			fc = args[i+1]
		}
	}
	if fc == "" {
		t.Fatalf("no -filter_complex in %v", args)
	}
	if !strings.Contains(fc, "[0:v]fps=1,split=2[s0][s1];") {
		t.Errorf("split missing/wrong: %s", fc)
	}
	if !strings.Contains(fc, "[s0]crop=iw*0.5000:ih*0.6000:iw*0.1000:ih*0.2000,select=gt(scene\\,0.35),metadata=print[o0];") {
		t.Errorf("crop of zone 0 wrong: %s", fc)
	}
	if !strings.Contains(fc, "[s1]crop=iw*0.3000:ih*0.4000:iw*0.0000:ih*0.0000,select=gt(scene\\,0.35),metadata=print[o1]") {
		t.Errorf("crop of zone 1 wrong: %s", fc)
	}
	maps := 0
	for i, a := range args {
		if a == "-map" && i+1 < len(args) && strings.HasPrefix(args[i+1], "[o") {
			maps++
		}
	}
	if maps != 2 {
		t.Errorf("want one -map [oI] per zone, got %d in %v", maps, args)
	}
	nulls := 0
	for i, a := range args {
		if a == "-f" && i+1 < len(args) && args[i+1] == "null" {
			nulls++
		}
	}
	if nulls != 2 {
		t.Errorf("want one -f null - per zone, got %d in %v", nulls, args)
	}
	// No plain -vf with zones.
	for _, a := range args {
		if a == "-vf" {
			t.Errorf("-vf must not be used with zones: %v", args)
		}
	}
}
