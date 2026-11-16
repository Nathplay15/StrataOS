package photos

import "testing"

// set sets the bits of a hash from a list of positions.
func set(h uint64, bits ...uint) uint64 {
	for _, b := range bits {
		h |= 1 << b
	}
	return h
}

func TestDuplicates(t *testing.T) {
	x := &Index{byPath: map[string]int{}}
	x.items = []Item{
		{Path: "/a.jpg", Hash: set(0, 1, 2)},           // base
		{Path: "/b.jpg", Hash: set(0, 1, 3)},           // 2 bits from a: duplicate
		{Path: "/c.jpg", Hash: set(0, 1, 2, 63, 62)},   // 3 bits from a: duplicate
		{Path: "/d.jpg", Hash: set(11, 22, 33, 44, 55, 5, 6, 7, 8, 9)}, // far from a
		{Path: "/e.jpg", Hash: 0},                      // not hashed: ignored
		{Path: "/f.mp4", Video: true, Hash: set(0, 1, 2)},
	}
	allow := func(string) bool { return true }
	groups := x.Duplicates(allow)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if len(g) != 3 {
		t.Fatalf("group = %+v", g)
	}
	if g[0].Path != "/a.jpg" || g[1].Path != "/b.jpg" || g[2].Path != "/c.jpg" {
		t.Fatalf("group order = %v %v %v", g[0].Path, g[1].Path, g[2].Path)
	}

	// Permissions apply: an item outside the allowed roots is not grouped.
	groups = x.Duplicates(func(p string) bool { return p == "/a.jpg" })
	if len(groups) != 0 {
		t.Fatalf("groups with one allowed item = %+v", groups)
	}

	// No duplicates: empty list, not nil.
	groups = x.Duplicates(func(p string) bool { return false })
	if groups == nil || len(groups) != 0 {
		t.Fatalf("empty groups = %+v", groups)
	}
}
