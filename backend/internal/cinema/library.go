package cinema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// groupKey identifies the works (movie or series) a file belongs to.
func groupKey(it Item) string {
	if it.Kind == "episode" {
		return "tv:" + strings.ToLower(it.Title)
	}
	return fmt.Sprintf("movie:%s:%d", strings.ToLower(it.Title), it.Year)
}

// Root is a scanned folder: Abs on disk, API as the files API spells it
// (the absolute mount path), so items carry paths the player can open.
type Root struct {
	Abs string
	API string
}

// BuildLibrary scans the roots, matches every work with TMDB (when a key is
// set) and downloads the posters into posterDir. Files of one work share
// its match. logf receives progress lines.
func BuildLibrary(ctx context.Context, roots []Root, cl *Client, posterDir string, maxDepth int, logf func(string, ...any)) []Item {
	var items []Item
	seen := map[string]bool{} // a share inside another one: each video once
	for _, root := range roots {
		for _, it := range Scan(root.Abs, maxDepth) {
			rel, err := filepath.Rel(root.Abs, it.Path)
			if err != nil {
				continue
			}
			it.Path = root.API + "/" + filepath.ToSlash(rel)
			if seen[it.Path] {
				continue
			}
			seen[it.Path] = true
			items = append(items, it)
		}
	}
	SortItems(items)
	// Match each work once, from its first file.
	matched := map[string]*Item{}
	for i := range items {
		k := groupKey(items[i])
		if _, ok := matched[k]; ok {
			continue
		}
		kind := "movie"
		if items[i].Kind == "episode" {
			kind = "tv"
		}
		if cl == nil || cl.Key == "" {
			matched[k] = nil
			continue
		}
		res, err := cl.Search(ctx, kind, items[i].Title, items[i].Year)
		if err != nil || len(res) == 0 {
			if err != nil && logf != nil {
				logf("TMDB search failed for {0}: {1}", items[i].Title, err)
			}
			matched[k] = nil
			continue
		}
		// Prefer an exact year match, else the first hit.
		best := res[0]
		for _, r := range res {
			if items[i].Year > 0 && r.Year == items[i].Year {
				best = r
				break
			}
		}
		det, err := cl.Details(ctx, kind, best.ID)
		if err != nil {
			if logf != nil {
				logf("TMDB details failed for {0}: {1}", best.ID, err)
			}
			matched[k] = nil
			continue
		}
		det.Poster = best.Poster
		if det.Poster != "" && posterDir != "" {
			if b, err := cl.Poster(ctx, det.Poster); err == nil {
				_ = os.MkdirAll(posterDir, 0o755)
				_ = os.WriteFile(filepath.Join(posterDir, strconv.Itoa(best.ID)+".jpg"), b, 0o644)
			}
		}
		matched[k] = det
		if logf != nil {
			logf("Matched {0} ({1}) with TMDB {2}: {3}", items[i].Title, items[i].Year, best.ID, det.Title)
		}
	}
	for i := range items {
		if det := matched[groupKey(items[i])]; det != nil {
			items[i].TMDB = det.TMDB
			items[i].Poster = det.Poster
			items[i].Overview = det.Overview
			items[i].Rating = det.Rating
			items[i].Runtime = det.Runtime
			items[i].Genres = det.Genres
			items[i].Cast = det.Cast
			items[i].Collection, items[i].CollectionID = det.Collection, det.CollectionID
			items[i].IMDB = det.IMDB
			if items[i].Kind == "movie" && det.Title != "" {
				items[i].Title = det.Title
				items[i].Year = det.Year
			}
			if items[i].Kind == "episode" && det.Title != "" {
				items[i].Title = det.Title
				items[i].Year = det.Year
			}
		}
	}
	return items
}

// PosterFile is the cache name of the poster of a TMDB id.
func PosterFile(tmdbID int) string { return strconv.Itoa(tmdbID) + ".jpg" }

// SortItemsByTitle sorts by title only.
func SortItemsByTitle(items []Item) {
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Title) < strings.ToLower(items[j].Title) })
}
