package nvr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Motion detection for the built-in engine: ffmpeg decodes the detection
// stream at one picture per second and keeps only the scene changes, then a
// snapshot is taken for the notification. Nothing leaves the NAS unencrypted;
// the watcher needs no extra component.

const (
	// Scene scores above this are movement; below, noise (0 = identical).
	motionThreshold = 0.35
	// At most one event per camera and period: leaves time to look.
	motionEvery = 90 * time.Second
)

var sceneRe = regexp.MustCompile(`lavfi\.scene_score=([0-9.]+)`)

// Zone is a rectangle of the detection picture, in fractions of its size.
type Zone struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// maxZones caps the rectangles drawn for one camera.
const maxZones = 4

// ParseZones decodes the JSON zones of a camera; "" means the whole picture.
func ParseZones(s string) ([]Zone, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var zones []Zone
	if err := json.Unmarshal([]byte(s), &zones); err != nil {
		return nil, fmt.Errorf("invalid zones: %w", err)
	}
	if len(zones) > maxZones {
		return nil, fmt.Errorf("at most %d zones", maxZones)
	}
	for _, z := range zones {
		if z.W < 0.02 || z.H < 0.02 || z.X < 0 || z.Y < 0 || z.X+z.W > 1.01 || z.Y+z.H > 1.01 {
			return nil, fmt.Errorf("a zone is outside the picture or too small")
		}
	}
	return zones, nil
}

// motionArgs builds the ffmpeg command of the movement watcher. With zones,
// each zone is cropped out of the detection stream and watched on its own
// (split), so movement outside them is ignored.
func motionArgs(input string, zones []Zone) []string {
	base := []string{"-hide_banner", "-nostdin", "-loglevel", "info", "-i", input}
	if len(zones) == 0 {
		return append(base, "-vf", fmt.Sprintf("fps=1,select=gt(scene\\,%v),metadata=print", motionThreshold), "-an", "-sn", "-dn", "-f", "null", "-")
	}
	f := strconv.FormatFloat(motionThreshold, 'f', -1, 64)
	var fc strings.Builder
	fmt.Fprintf(&fc, "[0:v]fps=1,split=%d", len(zones))
	for i := range zones {
		fmt.Fprintf(&fc, "[s%d]", i)
	}
	fc.WriteString(";")
	for i, z := range zones {
		fmt.Fprintf(&fc, "[s%d]crop=iw*%s:ih*%s:iw*%s:ih*%s,select=gt(scene\\,%s),metadata=print[o%d];",
			i,
			strconv.FormatFloat(z.W, 'f', 4, 64), strconv.FormatFloat(z.H, 'f', 4, 64),
			strconv.FormatFloat(z.X, 'f', 4, 64), strconv.FormatFloat(z.Y, 'f', 4, 64),
			f, i)
	}
	args := append([]string{}, base...)
	args = append(args, "-filter_complex", strings.TrimSuffix(fc.String(), ";"))
	for i := range zones {
		args = append(args, "-map", fmt.Sprintf("[o%d]", i), "-an", "-sn", "-dn", "-f", "null", "-")
	}
	return args
}

// motion watches the detection stream until ctx ends, reporting each event.
func (r *recorder) motion(ctx context.Context) {
	stream := r.cfg.Detection
	if stream == "" {
		stream = r.cfg.Stream
	}
	input, err := WriteInput(r.cfg.ID, "motion", stream)
	if err != nil {
		slog.Warn("motion: no input file", "camera", r.cfg.ID, "err", err)
		return
	}
	// fps=1 keeps the search cheap; select drops the quiet frames so that
	// metadata=print only prints the ones that moved. With zones, each zone
	// is watched on its own crop.
	args := motionArgs(input, r.cfg.Zones)
	last := time.Time{}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			slog.Warn("motion: ffmpeg pipe", "camera", r.cfg.ID, "err", err)
			return
		}
		if err := cmd.Start(); err != nil {
			slog.Warn("motion: ffmpeg start", "camera", r.cfg.ID, "err", err)
			return
		}
		rd := bufio.NewScanner(stderr)
		rd.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for rd.Scan() {
			m := sceneRe.FindSubmatch(rd.Bytes())
			if m == nil {
				continue
			}
			if score, err := strconv.ParseFloat(string(m[1]), 64); err == nil && score > motionThreshold && time.Since(last) >= motionEvery {
				last = time.Now()
				r.reportMotion(ctx, stream, score)
			}
		}
		cmd.Wait()
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second): // camera off: try again later, the recorder logs the outage
		}
	}
}

// reportMotion grabs one picture and hands the event over (OnMotion may be nil).
func (r *recorder) reportMotion(ctx context.Context, stream string, score float64) {
	m := r.m
	f := m.OnMotion
	if f == nil {
		return
	}
	var picture []byte
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	picture, err := Snapshot(sctx, stream)
	if err != nil {
		slog.Warn("motion: no picture", "camera", r.cfg.ID, "err", err)
	}
	slog.Info("motion detected", "camera", r.cfg.ID, "score", score)
	f(r.cfg.ID, r.cfg.Name, time.Now(), picture)
}
