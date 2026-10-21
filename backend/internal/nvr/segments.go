package nvr

import (
	"os"
	"regexp"
	"sort"
	"time"
)

// Segment is one recorded file of a camera.
type Segment struct {
	Name  string    `json:"name"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Size  int64     `json:"size"`
	// Open: the file the recorder is writing (not playable before it is closed).
	Open bool `json:"open,omitempty"`
}

// FileInfo is what segment listing needs from a directory entry.
type FileInfo struct {
	Name  string
	Size  int64
	MTime time.Time
}

var segRe = regexp.MustCompile(`^(\d{8}-\d{6})\.(mp4|mkv)$`)

// ParseSegmentName returns the start time encoded in a segment name.
func ParseSegmentName(name string, loc *time.Location) (time.Time, bool) {
	m := segRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102-150405", m[1], loc)
	return t, err == nil
}

// maxSegment bounds the end of a segment whose next one is missing.
const maxSegment = 2 * time.Hour

// BuildSegments turns directory entries into segments sorted by start time.
// A segment ends where the next one starts, or at its last write; open marks
// the newest one as being written.
func BuildSegments(files []FileInfo, loc *time.Location, open bool) []Segment {
	segs := []Segment{}
	for _, f := range files {
		if t, ok := ParseSegmentName(f.Name, loc); ok {
			segs = append(segs, Segment{Name: f.Name, Start: t, End: f.MTime, Size: f.Size})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start.Before(segs[j].Start) })
	for i := range segs {
		s := &segs[i]
		if i+1 < len(segs) && segs[i+1].Start.Before(s.End) {
			s.End = segs[i+1].Start
		}
		if s.End.Before(s.Start) {
			s.End = s.Start
		}
		if s.End.Sub(s.Start) > maxSegment {
			s.End = s.Start.Add(maxSegment)
		}
	}
	if open && len(segs) > 0 {
		segs[len(segs)-1].Open = true
	}
	return segs
}

// ListSegments reads the segments of a camera folder.
func ListSegments(dir string, open bool) ([]Segment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Segment{}, nil
		}
		return nil, err
	}
	files := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, FileInfo{Name: e.Name(), Size: info.Size(), MTime: info.ModTime()})
		}
	}
	return BuildSegments(files, time.Local, open), nil
}

// Day returns the segments overlapping the given local day (YYYY-MM-DD).
func Day(segs []Segment, day string, loc *time.Location) ([]Segment, error) {
	from, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return nil, err
	}
	to := from.AddDate(0, 0, 1)
	out := []Segment{}
	for _, s := range segs {
		if s.Start.Before(to) && !s.End.Before(from) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Days lists the local days that have recordings, newest first.
func Days(segs []Segment, loc *time.Location) []string {
	seen := map[string]bool{}
	out := []string{}
	for i := len(segs) - 1; i >= 0; i-- {
		d := segs[i].Start.In(loc).Format("2006-01-02")
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// Expired selects the segments to delete, oldest first: those ended more than
// keepDays ago, then the oldest until the total fits in maxBytes. The segment
// being written is never selected. Zero disables a limit.
func Expired(segs []Segment, now time.Time, keepDays int, maxBytes int64) []Segment {
	var total int64
	for _, s := range segs {
		total += s.Size
	}
	out := []Segment{}
	cutoff := now.AddDate(0, 0, -keepDays)
	for _, s := range segs { // sorted oldest first
		if s.Open {
			break
		}
		old := keepDays > 0 && s.End.Before(cutoff)
		big := maxBytes > 0 && total > maxBytes
		if !old && !big {
			break
		}
		out = append(out, s)
		total -= s.Size
	}
	return out
}
