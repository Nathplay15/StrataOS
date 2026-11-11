package ondemand

import "sort"

// Candidate is a cached file that eviction may free.
type Candidate struct {
	Path  string
	Bytes int64 // bytes cached
	Used  int64 // last use (Unix seconds)
}

// PickEvictions chooses the files to free so that the cache (used bytes)
// fits in budget: least recently used first, ties broken by size (larger
// first: fewer files to download again) then path. Only evictable files
// are candidates; it frees down to lowWater × budget so that eviction does
// not run at every read once the cache is full.
func PickEvictions(files map[string]*FileState, used, budget int64, lowWater float64) []string {
	if budget <= 0 || used <= budget {
		return nil
	}
	target := int64(float64(budget) * lowWater)
	var c []Candidate
	for p, f := range files {
		if f.Evictable() {
			c = append(c, Candidate{p, f.Ranges.Total(), f.Used})
		}
	}
	sort.Slice(c, func(i, j int) bool {
		if c[i].Used != c[j].Used {
			return c[i].Used < c[j].Used
		}
		if c[i].Bytes != c[j].Bytes {
			return c[i].Bytes > c[j].Bytes
		}
		return c[i].Path < c[j].Path
	})
	var out []string
	for _, x := range c {
		if used <= target {
			break
		}
		out = append(out, x.Path)
		used -= x.Bytes
	}
	return out
}

// DefaultBudget is the cache size when the user set none: a tenth of the
// disk, between 2 and 50 GiB, and never more than half the free space.
func DefaultBudget(total, free int64) int64 {
	b := min(max(total/10, 2<<30), 50<<30)
	if free > 0 {
		b = min(b, free/2)
	}
	return max(b, 256<<20)
}
