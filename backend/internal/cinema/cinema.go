// Package cinema builds the video library of the NAS: it scans the shares
// for video files, reads the title (and year, season, episode) from their
// names and matches each one against TMDB for its poster, synopsis, actors
// and rating (tmdb.go). The library itself is kept in a JSON file in the
// data directory, next to the downloaded posters.
package cinema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item is one video file of the library, matched (or not) with TMDB.
type Item struct {
	Path    string `json:"path"` // absolute path of the file
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`
	Kind    string `json:"kind"` // "movie" | "episode"
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
	Size    int64  `json:"size"`
	// TMDB match (0 = not matched):
	TMDB     int      `json:"tmdbId,omitempty"`
	Poster   string   `json:"poster,omitempty"` // cached file name, served by the API
	Overview string   `json:"overview,omitempty"`
	Rating   float64  `json:"rating,omitempty"`
	Runtime  int      `json:"runtime,omitempty"` // minutes
	Genres   []string `json:"genres,omitempty"`
	Cast     []string `json:"cast,omitempty"` // actors, best known for
}

// State is the persisted library (dataDir/cinema.json).
type State struct {
	Key     string    `json:"-"` // the TMDB key is stored apart (key file)
	Running bool      `json:"running"`
	Checked time.Time `json:"checked,omitzero"`
	Scanned int       `json:"scanned"`
	Items   []Item    `json:"items"`
}

// LoadState reads the state file; a missing file is an empty state.
func LoadState(path string) State {
	var st State
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// SaveState writes the state file atomically.
func SaveState(path string, st State) error {
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadKey / SaveKey keep the TMDB key in its own file (0600).
func LoadKey(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func SaveKey(path, key string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(key), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
	".avi": true, ".webm": true, ".ts": true, ".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".3gp": true,
}

// IsVideo reports whether the name has a video extension.
func IsVideo(name string) bool { return videoExts[strings.ToLower(filepath.Ext(name))] }

var (
	yearRe    = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)
	episodeRe = regexp.MustCompile(`(?i)\b[S](\d{1,2})\s*[E](\d{1,3})\b`)
	// release tags stripped from the title
	tagRe = regexp.MustCompile(`(?i)\b(1080p|720p|2160p|4k|uhd|blu-?ray|bdrip|brrip|web-?dl|webrip|hdtv|x264|x265|h\.?264|h\.?265|hevc|aac|dts|hdr|remux|proper|extended|unrated|directors?\.?cut|imax|3d)\b`)
)

// ParseName reads title, year, season and episode from a video file name:
// "The.Matrix.1999.1080p.BluRay.mkv" -> movie 1999; "Show.S01E02.mkv" -> episode.
func ParseName(name string) (title string, year, season, episode int, kind string) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	kind = "movie"
	if m := episodeRe.FindStringSubmatch(stem); m != nil {
		kind = "episode"
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
		stem = episodeRe.ReplaceAllString(stem, " ")
	}
	if m := yearRe.FindStringSubmatch(stem); m != nil {
		year, _ = strconv.Atoi(m[1])
		stem = yearRe.ReplaceAllString(stem, " ")
	}
	stem = tagRe.ReplaceAllString(stem, " ")
	stem = strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ").Replace(stem)
	title = strings.Join(strings.Fields(stem), " ")
	return title, year, season, episode, kind
}

// skipDirs are never scanned.
var skipDirs = map[string]bool{".zfs": true, ".recycle": true, "lost+found": true, ".Trash": true, ".Trashes": true}

// Scan walks root (up to maxDepth levels) and lists the video files with
// their parsed names. Hidden directories and recycle bins are skipped.
func Scan(root string, maxDepth int) []Item {
	var out []Item
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if skipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				walk(p, depth+1)
				continue
			}
			if strings.HasPrefix(e.Name(), ".") || !IsVideo(e.Name()) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			title, year, season, ep, kind := ParseName(e.Name())
			if title == "" {
				// "S01E02.mkv" alone: the series name is the folder's.
				title = filepath.Base(dir)
				if t2, y2, _, _, _ := ParseName(title + filepath.Ext(title)); t2 != "" {
					title, year = t2, y2
				}
				if title == "" {
					continue
				}
			}
			out = append(out, Item{Path: p, Title: title, Year: year, Kind: kind, Season: season, Episode: ep, Size: fi.Size()})
		}
	}
	walk(root, 0)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// SortItems orders movies and episodes by title, year, season, episode.
func SortItems(items []Item) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if strings.ToLower(a.Title) != strings.ToLower(b.Title) {
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		}
		if a.Year != b.Year {
			return a.Year < b.Year
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		return a.Episode < b.Episode
	})
}
