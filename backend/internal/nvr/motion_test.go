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
