package storagetools

import (
	"container/heap"
	"sort"
	"time"
)

// FileEntry is a file kept in a report (largest files, duplicates…).
type FileEntry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// fileHeap is a min-heap on size: the smallest of the kept files is evicted first.
type fileHeap []FileEntry

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].Size < h[j].Size }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *fileHeap) Push(x any)        { *h = append(*h, x.(FileEntry)) }
func (h *fileHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// topN keeps the n largest files seen, in O(n) memory whatever the number of files.
type topN struct {
	n int
	h fileHeap
}

func newTopN(n int) *topN { return &topN{n: n} }

func (t *topN) push(f FileEntry) {
	if t.n <= 0 {
		return
	}
	if len(t.h) < t.n {
		heap.Push(&t.h, f)
		return
	}
	if f.Size > t.h[0].Size {
		t.h[0] = f
		heap.Fix(&t.h, 0)
	}
}

// sorted returns the kept files, largest first.
func (t *topN) sorted() []FileEntry {
	out := append([]FileEntry{}, t.h...)
	sort.Slice(out, func(a, b int) bool {
		if out[a].Size != out[b].Size {
			return out[a].Size > out[b].Size
		}
		return out[a].Path < out[b].Path
	})
	return out
}
