package mediaplay

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SidecarBase is the synthetic stream index where sidecar subtitle files
// start (container tracks keep their real index, below 1000).
const SidecarBase = 1000

var sidecarRe = regexp.MustCompile(`^(.+?)\.(?:([a-z]{2,3}(?:-[A-Z]{2})?)\.)?(srt|vtt)$`)

// Sidecars lists the subtitle files sitting next to a video (movie.srt,
// movie.vtt, movie.fr.srt, movie.en-US.vtt…) as synthetic streams whose
// index starts at SidecarBase. The order is stable (by file name).
func Sidecars(videoPath string) []Stream {
	dir, base := filepath.Split(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), stem+".") {
			continue
		}
		m := sidecarRe.FindStringSubmatch(e.Name())
		if m == nil || m[1] != stem {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]Stream, 0, len(names))
	for i, n := range names {
		m := sidecarRe.FindStringSubmatch(n)
		codec := "webvtt"
		if m[3] == "srt" {
			codec = "subrip"
		}
		out = append(out, Stream{Index: SidecarBase + i, Type: "subtitle", Codec: codec, Lang: m[2], Title: n})
	}
	return out
}

// SidecarName resolves a synthetic index (SidecarBase+i) back to its file
// name in the video's folder, recomputed from the directory listing (the
// index alone is not trusted). Callers read it through the share's os.Root.
func SidecarName(videoPath string, idx int) (string, bool) {
	if idx < SidecarBase {
		return "", false
	}
	subs := Sidecars(videoPath)
	i := idx - SidecarBase
	if i < 0 || i >= len(subs) {
		return "", false
	}
	return subs[i].Title, true
}

var srtTimeRe = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2}),(\d{3})`)

// SRTtoVTT converts a SubRip (.srt) payload to WebVTT: same cues, dots
// instead of commas in the timestamps, and the WEBVTT header. Unknown or
// malformed cues are passed through unchanged.
func SRTtoVTT(srt []byte) []byte {
	out := bytes.NewBufferString("WEBVTT\n\n")
	for _, line := range strings.Split(string(srt), "\n") {
		line = strings.TrimRight(line, "\r")
		line = srtTimeRe.ReplaceAllStringFunc(line, func(m string) string {
			p := srtTimeRe.FindStringSubmatch(m)
			return p[1] + ":" + p[2] + ":" + p[3] + "." + p[4]
		})
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
