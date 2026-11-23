package nvr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Event clips: around each motion event, a short video is cut out of the
// closed recording segments (a few seconds before and after the event) and
// kept in the "clips" subfolder of the camera, pruned by age.

var clipRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{2}h\d{2}m\d{2}s)\.(mp4|mkv)$`)

// ClipLayout is the strftime-free pattern of clip names (local event time).
const ClipLayout = "2006-01-02_15h04m05s"

// ParseClipName returns the event time encoded in a clip name.
func ParseClipName(name string) (time.Time, bool) {
	m := clipRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(ClipLayout, m[1], time.Local)
	return t, err == nil
}

// ClipWindow picks the closed segments of dir covering [at-pre, at+post] and
// returns their paths, the offset in the first one and the clip duration. The
// segment being written is skipped (an MP4/MKV is not readable before it is
// closed); the window is clamped to what is closed, so a clip is still cut
// when the event sits near the end of the newest closed segment.
func ClipWindow(dir string, segs []Segment, at time.Time, pre, post time.Duration) (files []string, from, dur time.Duration, ok bool) {
	lo, hi := at.Add(-pre), at.Add(post)
	var sel []Segment
	for _, s := range segs { // segs are sorted by start time
		if s.Open {
			continue
		}
		if s.Start.Before(hi) && s.End.After(lo) {
			sel = append(sel, s)
		}
	}
	if len(sel) == 0 {
		return nil, 0, 0, false
	}
	first, last := sel[0], sel[len(sel)-1]
	start := lo
	if first.Start.After(start) {
		start = first.Start
	}
	end := hi
	if last.End.Before(end) {
		end = last.End
	}
	if !end.After(start) {
		return nil, 0, 0, false
	}
	for _, s := range sel {
		files = append(files, filepath.Join(dir, s.Name))
	}
	return files, start.Sub(first.Start), end.Sub(start), true
}

// CutEventClip cuts [at-pre, at+post] out of the closed segments of dir into
// out (its extension chooses the container), without re-encoding. Segments
// spanning the window are concatenated first when there is more than one.
func CutEventClip(ctx context.Context, dir, out string, at time.Time, pre, post time.Duration) error {
	segs, err := ListSegments(dir, true)
	if err != nil {
		return err
	}
	files, from, dur, ok := ClipWindow(dir, segs, at, pre, post)
	if !ok {
		return errors.New("no closed recording covers the event")
	}
	fromSec := int(from.Seconds())
	durSec := int(math.Ceil(dur.Seconds()))
	if durSec < 1 {
		durSec = 1
	}
	args := ClipFileArgs(files[0], out, fromSec, durSec)
	if len(files) > 1 {
		list, err := ConcatFiles(files)
		if err != nil {
			return err
		}
		defer os.Remove(list)
		args = ConcatClipArgs(list, out, fromSec, durSec)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	msg, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, Redact(string(msg)))
	}
	return nil
}

// ClipExt returns the container of the event clips of a camera folder, from
// its segments (.mp4 by default).
func ClipExt(dir string) string {
	segs, err := ListSegments(dir, false)
	if err != nil || len(segs) == 0 {
		return ".mp4"
	}
	if filepath.Ext(segs[0].Name) == ".mkv" {
		return ".mkv"
	}
	return ".mp4"
}

// clipFile is one event clip on disk.
type clipFile struct {
	path string
	mod  time.Time
}

// PruneClips deletes the event clips of dir older than keepDays (0 = 30) and,
// beyond maxClips (0 = 500), the oldest ones. It returns how many were deleted.
func PruneClips(dir string, keepDays, maxClips int, now time.Time) int {
	if keepDays <= 0 {
		keepDays = 30
	}
	if maxClips <= 0 {
		maxClips = 500
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var clips []clipFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t, ok := ParseClipName(e.Name())
		if !ok {
			continue
		}
		mod := t
		if fi, err := e.Info(); err == nil {
			mod = fi.ModTime()
		}
		clips = append(clips, clipFile{filepath.Join(dir, e.Name()), mod})
	}
	cutoff := now.AddDate(0, 0, -keepDays)
	deleted := 0
	kept := make([]clipFile, 0, len(clips))
	for _, c := range clips {
		if c.mod.Before(cutoff) {
			_ = os.Remove(c.path)
			deleted++
		} else {
			kept = append(kept, c)
		}
	}
	if len(kept) > maxClips {
		sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
		for _, c := range kept[:len(kept)-maxClips] {
			_ = os.Remove(c.path)
			deleted++
		}
	}
	return deleted
}
