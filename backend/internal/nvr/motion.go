package nvr

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
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
	// metadata=print only prints the ones that moved.
	filter := fmt.Sprintf("fps=1,select=gt(scene\\,%v),metadata=print", motionThreshold)
	last := time.Time{}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "info",
			"-i", input, "-vf", filter, "-an", "-sn", "-dn", "-f", "null", "-")
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
