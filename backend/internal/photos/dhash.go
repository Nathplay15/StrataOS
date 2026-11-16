package photos

import (
	"context"
	"image/color"
	"image/png"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"time"

	"strataos/internal/sys"
)

// dhash computes a 64-bit perceptual hash of an image: the picture is reduced
// to 9x8 grayscale (vipsthumbnail) and each of the 64 bits compares two
// adjacent pixels (difference hash). It returns 0 when the tools are missing
// or the picture cannot be read: those items are ignored by Duplicates.
func dhash(ctx context.Context, path string) uint64 {
	dir, err := os.MkdirTemp("", "strata-dhash")
	if err != nil {
		return 0
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "h.png")
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// "!": the exact 9x8 size regardless of the aspect ratio.
	if _, err := sys.RunOpts(cctx, sys.Opts{Timeout: time.Minute}, "vipsthumbnail", path, "--colourspace", "b-w", "--size", "9x8!", "-o", out); err != nil {
		return 0
	}
	f, err := os.Open(out)
	if err != nil {
		return 0
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return 0
	}
	b := img.Bounds()
	if b.Dx() < 9 || b.Dy() < 8 {
		return 0
	}
	gray := func(x, y int) uint8 {
		g, _ := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
		return g.Y
	}
	var h uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if gray(x, y) > gray(x+1, y) {
				h |= 1 << uint(y*8+x)
			}
		}
	}
	return h
}

// dupThreshold is the maximum Hamming distance between the hashes of two
// similar photos (bits differing, out of 64).
const dupThreshold = 8

// Duplicates groups the allowed images by visual similarity: pairs of
// pictures whose perceptual hashes differ by at most dupThreshold bits.
// Candidate pairs share one 8-bit band of the hash, which keeps the
// comparison fast on big libraries. Videos and unhashed items are ignored.
// The groups are returned biggest first, each sorted oldest first.
func (x *Index) Duplicates(allow func(string) bool) [][]Item {
	x.mu.RLock()
	var items []Item
	for _, it := range x.items {
		if !it.Video && it.Hash != 0 && allow(it.Path) {
			items = append(items, it)
		}
	}
	x.mu.RUnlock()

	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(a int) int {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	type band struct {
		i int
		v uint64
	}
	bands := map[band][]int{}
	for i, it := range items {
		for b := 0; b < 8; b++ {
			k := band{b, (it.Hash >> uint(b*8)) & 0xff}
			bands[k] = append(bands[k], i)
		}
	}
	for _, idxs := range bands {
		for i := 0; i < len(idxs); i++ {
			for j := i + 1; j < len(idxs); j++ {
				a, b := idxs[i], idxs[j]
				if bits.OnesCount64(items[a].Hash^items[b].Hash) <= dupThreshold {
					union(a, b)
				}
			}
		}
	}
	groups := map[int][]Item{}
	for i, it := range items {
		r := find(i)
		groups[r] = append(groups[r], it)
	}
	var out [][]Item
	for _, g := range groups {
		if len(g) > 1 {
			sort.Slice(g, func(i, j int) bool { return g[i].Date < g[j].Date })
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	if out == nil {
		out = [][]Item{}
	}
	return out
}
