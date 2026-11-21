package ondemand

import (
	"path"
	"sort"
	"strings"
	"sync"

	"strataos-sync/internal/engine"
)

// Tree is what one folder of the NAS holds (paths relative to it, with
// "/"), followed with its change feed. Items created here and not on the
// NAS yet are in it too (Local). End-to-end encrypted folders are left
// out: their names and contents are ciphertext without the key, and on
// demand does not decrypt (they are listed in Hidden for the window).
type Tree struct {
	mu     sync.RWMutex
	nodes  map[string]*Node
	kids   map[string]map[string]bool // folder → names below it ("" = the root)
	hidden map[string]bool            // encrypted folders left out
	cursor string
}

// Node is an item of the tree.
type Node struct {
	engine.RemoteEntry
	Local bool `json:"l,omitempty"` // created here, not on the NAS yet
}

// ChangeKind says how an item changed.
type ChangeKind int

const (
	Added ChangeKind = iota
	Modified
	Removed
)

// Change is an item that changed in a tree.
type Change struct {
	Path string
	Kind ChangeKind
	Dir  bool
	Old  *Node // what it was (Modified, Removed)
}

// NewTree returns an empty tree.
func NewTree() *Tree {
	return &Tree{nodes: map[string]*Node{}, kids: map[string]map[string]bool{"": {}}, hidden: map[string]bool{}}
}

// Cursor of the change feed this tree is at.
func (t *Tree) Cursor() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cursor
}

func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// hiddenLocked tells whether p is inside (or is) an encrypted folder.
func (t *Tree) hiddenLocked(p string) bool {
	for d := p; d != ""; d = parentOf(d) {
		if t.hidden[d] {
			return true
		}
	}
	return false
}

func (t *Tree) putLocked(n *Node) {
	t.nodes[n.Path] = n
	for p := n.Path; p != ""; p = parentOf(p) {
		par := parentOf(p)
		k := t.kids[par]
		if k == nil {
			k = map[string]bool{}
			t.kids[par] = k
		}
		k[path.Base(p)] = true
		if _, ok := t.nodes[par]; ok || par == "" {
			break
		}
	}
	if n.Dir && t.kids[n.Path] == nil {
		t.kids[n.Path] = map[string]bool{}
	}
}

// removeLocked removes p and what is below it, appending the changes.
func (t *Tree) removeLocked(p string, out *[]Change) {
	n, ok := t.nodes[p]
	if !ok && t.kids[p] == nil {
		return
	}
	for name := range t.kids[p] {
		t.removeLocked(p+"/"+name, out)
	}
	delete(t.kids, p)
	delete(t.nodes, p)
	if k := t.kids[parentOf(p)]; k != nil {
		delete(k, path.Base(p))
	}
	if ok {
		*out = append(*out, Change{Path: p, Kind: Removed, Dir: n.Dir, Old: n})
	}
}

func same(a, b engine.RemoteEntry) bool {
	return a.Dir == b.Dir && a.Size == b.Size && a.MTime == b.MTime && a.Version == b.Version && a.ID == b.ID
}

// Apply applies a page of the change feed and returns what changed. A
// reset page lists everything: items missing from it are removed (those
// created here and not sent yet stay).
func (t *Tree) Apply(c engine.Changes, reset bool) []Change {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Change
	var seen map[string]bool
	if reset {
		seen = map[string]bool{}
		t.hidden = map[string]bool{}
	}
	for _, e := range c.Entries {
		if e.Path == "" || e.Path == "." {
			continue
		}
		if e.Dir && e.E2E {
			t.hidden[e.Path] = true
			t.removeLocked(e.Path, &out)
			continue
		}
		if t.hiddenLocked(e.Path) {
			continue
		}
		if seen != nil {
			seen[e.Path] = true
		}
		old, ok := t.nodes[e.Path]
		switch {
		case !ok:
			t.putLocked(&Node{RemoteEntry: e})
			out = append(out, Change{Path: e.Path, Kind: Added, Dir: e.Dir})
		case old.Dir != e.Dir:
			t.removeLocked(e.Path, &out)
			t.putLocked(&Node{RemoteEntry: e})
			out = append(out, Change{Path: e.Path, Kind: Added, Dir: e.Dir})
		case old.Local || !same(old.RemoteEntry, e):
			t.putLocked(&Node{RemoteEntry: e})
			out = append(out, Change{Path: e.Path, Kind: Modified, Dir: e.Dir, Old: old})
		}
	}
	for _, d := range c.Deleted {
		if _, ok := t.nodes[d]; ok && t.nodes[d].Local {
			continue
		}
		delete(t.hidden, d)
		t.removeLocked(d, &out)
	}
	if reset {
		var gone []string
		for p, n := range t.nodes {
			if !seen[p] && !n.Local {
				gone = append(gone, p)
			}
		}
		sort.Strings(gone) // parents first: their removal takes the children
		for _, p := range gone {
			t.removeLocked(p, &out)
		}
	}
	if c.Cursor != "" {
		t.cursor = c.Cursor
	}
	return out
}

// Get returns an item ("" is the root folder).
func (t *Tree) Get(p string) (Node, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if p == "" {
		return Node{RemoteEntry: engine.RemoteEntry{Dir: true}}, true
	}
	n, ok := t.nodes[p]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// List returns the items of a folder, sorted by name.
func (t *Tree) List(dir string) ([]Node, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if dir != "" {
		if n, ok := t.nodes[dir]; !ok || !n.Dir {
			return nil, false
		}
	}
	names := make([]string, 0, len(t.kids[dir]))
	for n := range t.kids[dir] {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Node, 0, len(names))
	for _, n := range names {
		p := n
		if dir != "" {
			p = dir + "/" + n
		}
		if x, ok := t.nodes[p]; ok {
			out = append(out, *x)
		}
	}
	return out, true
}

// Put sets an item (after an operation of this computer: an upload, a new
// folder, a move), without waiting for the change feed.
func (t *Tree) Put(n Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.nodes[n.Path]; ok && old.Dir && !n.Dir {
		var out []Change
		t.removeLocked(n.Path, &out)
	}
	t.putLocked(&n)
}

// Remove removes an item and what is below it.
func (t *Tree) Remove(p string) []Change {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Change
	t.removeLocked(p, &out)
	return out
}

// Move renames an item and what is below it (after a move on the NAS).
func (t *Tree) Move(from, to string) []Change {
	t.mu.Lock()
	defer t.mu.Unlock()
	var moved []*Node
	for p, n := range t.nodes {
		if p == from || strings.HasPrefix(p, from+"/") {
			moved = append(moved, n)
		}
	}
	var out []Change
	t.removeLocked(from, &out)
	sort.Slice(moved, func(i, j int) bool { return moved[i].Path < moved[j].Path })
	for _, n := range moved {
		c := *n
		c.Path = to + strings.TrimPrefix(n.Path, from)
		t.putLocked(&c)
		out = append(out, Change{Path: c.Path, Kind: Added, Dir: c.Dir})
	}
	return out
}

// Hidden lists the encrypted folders left out.
func (t *Tree) Hidden() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.hidden))
	for p := range t.hidden {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Snapshot returns every item (to save it).
func (t *Tree) Snapshot() ([]Node, []string, string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Node, 0, len(t.nodes))
	for _, n := range t.nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	h := make([]string, 0, len(t.hidden))
	for p := range t.hidden {
		h = append(h, p)
	}
	return out, h, t.cursor
}

// Restore fills an empty tree from a saved snapshot.
func (t *Tree) Restore(nodes []Node, hidden []string, cursor string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	for i := range nodes {
		n := nodes[i]
		t.putLocked(&n)
	}
	for _, h := range hidden {
		t.hidden[h] = true
	}
	t.cursor = cursor
}
