package engine

import (
	"sort"
	"strings"
)

type opKind int

const (
	opMoveRemote   opKind = iota // a local rename or move, done on the NAS
	opMoveLocal                  // a rename or move on the NAS, done locally
	opMkdirRemote                //
	opMkdirLocal                 //
	opCompare                    // both sides have a file of the same size: same content?
	opConflict                   // both changed: keep the local version under a conflict name, take the NAS one
	opUpload                     //
	opDownload                   //
	opRecord                     // already the same on both sides
	opForget                     // gone on both sides
	opDeleteRemote               // deleted locally
	opDeleteLocal                // deleted on the NAS
)

var opNames = [...]string{"move-remote", "move-local", "mkdir-remote", "mkdir-local", "compare", "conflict", "upload", "download", "record", "forget", "delete-remote", "delete-local"}

func (k opKind) String() string { return opNames[k] }

type op struct {
	kind opKind
	key  string
	l    *LocalEntry
	r    *RemoteEntry
	b    *BaseEntry
	// Moves: the destination (key, local path, remote path).
	toKey, toLocal, toRemote string
	// Moves: the entries at the destination.
	l2 *LocalEntry
	r2 *RemoteEntry
}

// view is what the planner compares, by key.
type view struct {
	local  map[string]*LocalEntry
	remote map[string]*RemoteEntry
	base   map[string]*BaseEntry
	// unreadable local folders (keys): nothing below them is decided, so
	// that an unreadable folder never looks deleted.
	unreadable []string
	// localID returns the file ID of a local entry (computed lazily on
	// systems where scans do not give it).
	localID func(*LocalEntry) uint64
}

type plan struct {
	ops   []op
	again bool // some changes wait for another round (after a folder move)
}

func localChanged(l *LocalEntry, b *BaseEntry) bool {
	switch {
	case b == nil:
		return l != nil
	case l == nil, l.Dir != b.Dir:
		return true
	case l.Dir:
		return false
	}
	return l.Size != b.Size || l.MTime != b.LocalMTime
}

func remoteChanged(r *RemoteEntry, b *BaseEntry) bool {
	switch {
	case b == nil:
		return r != nil
	case r == nil, r.Dir != b.Dir:
		return true
	case r.Dir:
		return false
	}
	return r.Version != b.Version
}

func depth(k string) int { return strings.Count(k, "/") }

// makePlan decides what to do for every path.
func makePlan(v view) plan {
	var p plan
	keys := map[string]bool{}
	for k := range v.local {
		keys[k] = true
	}
	for k := range v.remote {
		keys[k] = true
	}
	for k := range v.base {
		keys[k] = true
	}
	done := map[string]bool{}
	for k := range keys {
		for _, u := range v.unreadable {
			if k == u || isUnder(k, u) {
				done[k] = true
			}
		}
	}
	idOf := v.localID
	if idOf == nil {
		idOf = func(l *LocalEntry) uint64 { return l.ID }
	}
	// Skip a moved folder's content this round: it is compared again once
	// the base follows the move.
	settle := func(from, to string) {
		for k := range keys {
			if isUnder(k, from) || isUnder(k, to) {
				done[k] = true
			}
		}
		p.again = true
	}

	// Local moves: a synced item gone, a new item with its file ID (and,
	// for files, its size and date), nothing on the NAS at the new place.
	newLocal := map[uint64]string{}
	for k := range keys {
		l, r, b := v.local[k], v.remote[k], v.base[k]
		if l != nil && b == nil && r == nil && !done[k] {
			if id := idOf(l); id != 0 {
				newLocal[id] = k
			}
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		l, r, b := v.local[k], v.remote[k], v.base[k]
		if done[k] || b == nil || l != nil || r == nil || remoteChanged(r, b) || b.LocalID == 0 {
			continue
		}
		k2, ok := newLocal[b.LocalID]
		if !ok || done[k2] {
			continue
		}
		l2 := v.local[k2]
		if l2.Dir != b.Dir || !b.Dir && (l2.Size != b.Size || l2.MTime != b.LocalMTime) {
			continue
		}
		p.ops = append(p.ops, op{kind: opMoveRemote, key: k, b: b, r: r, toKey: k2, toLocal: l2.Path, toRemote: l2.Path, l2: l2})
		done[k], done[k2] = true, true
		delete(newLocal, b.LocalID)
		if b.Dir {
			settle(k, k2)
		}
	}
	// Moves on the NAS: a synced item gone there, a new one with its ID
	// (and, for files, its version: same inode, size and date).
	newRemote := map[string]string{}
	for k := range keys {
		l, r, b := v.local[k], v.remote[k], v.base[k]
		if r != nil && b == nil && l == nil && r.ID != "" && !done[k] {
			newRemote[r.ID] = k
		}
	}
	for _, k := range sorted {
		l, r, b := v.local[k], v.remote[k], v.base[k]
		if done[k] || b == nil || r != nil || l == nil || localChanged(l, b) || b.RemoteID == "" {
			continue
		}
		k2, ok := newRemote[b.RemoteID]
		if !ok || done[k2] {
			continue
		}
		r2 := v.remote[k2]
		if r2.Dir != b.Dir || !b.Dir && r2.Version != b.Version {
			continue
		}
		p.ops = append(p.ops, op{kind: opMoveLocal, key: k, b: b, l: l, toKey: k2, toLocal: r2.Path, toRemote: r2.Path, r2: r2})
		done[k], done[k2] = true, true
		delete(newRemote, b.RemoteID)
		if b.Dir {
			settle(k, k2)
		}
	}

	for _, k := range sorted {
		if done[k] {
			continue
		}
		l, r, b := v.local[k], v.remote[k], v.base[k]
		lc, rc := localChanged(l, b), remoteChanged(r, b)
		add := func(kind opKind) { p.ops = append(p.ops, op{kind: kind, key: k, l: l, r: r, b: b}) }
		switch {
		case !lc && !rc:
			if l == nil || r == nil || b == nil {
				continue
			}
			// Same key, another spelling: a change of case on one side.
			switch {
			case r.Path != b.Path:
				p.ops = append(p.ops, op{kind: opMoveLocal, key: k, l: l, b: b, toKey: k, toLocal: r.Path, toRemote: r.Path, r2: r})
			case l.Path != b.local():
				p.ops = append(p.ops, op{kind: opMoveRemote, key: k, r: r, b: b, toKey: k, toLocal: l.Path, toRemote: l.Path, l2: l})
			}
		case lc && !rc:
			switch {
			case l == nil && b.Dir:
				add(opDeleteRemote) // after its content (deepest first)
			case l == nil:
				add(opDeleteRemote)
			case b != nil && l.Dir != b.Dir:
				// The type changed here: remove the old one on the NAS first.
				add(opDeleteRemote)
				p.again = true
			case l.Dir:
				add(opMkdirRemote)
			default:
				add(opUpload)
			}
		case !lc && rc:
			switch {
			case r == nil:
				add(opDeleteLocal)
			case b != nil && r.Dir != b.Dir:
				add(opDeleteLocal)
				p.again = true
			case r.Dir:
				add(opMkdirLocal)
			default:
				add(opDownload)
			}
		default: // changed on both sides
			switch {
			case l == nil && r == nil:
				add(opForget)
			case l == nil: // deleted here, changed there: the NAS version comes back
				if r.Dir {
					add(opMkdirLocal)
				} else {
					add(opDownload)
				}
			case r == nil: // changed here, deleted there: the local version goes back
				if l.Dir {
					add(opMkdirRemote)
				} else {
					add(opUpload)
				}
			case l.Dir && r.Dir:
				add(opRecord)
			case l.Dir != r.Dir:
				add(opConflict)
			case l.Size == r.Size:
				add(opCompare)
			default:
				add(opConflict)
			}
		}
	}
	prio := func(o op) int {
		switch o.kind {
		case opMoveRemote, opMoveLocal:
			return 0
		case opMkdirRemote, opMkdirLocal:
			return 1
		case opCompare, opConflict:
			return 2
		case opUpload, opDownload:
			return 3
		case opRecord, opForget:
			return 4
		}
		return 5
	}
	sort.SliceStable(p.ops, func(i, j int) bool {
		a, b := p.ops[i], p.ops[j]
		if pa, pb := prio(a), prio(b); pa != pb {
			return pa < pb
		}
		if prio(a) == 5 && depth(a.key) != depth(b.key) {
			return depth(a.key) > depth(b.key) // deletions: content before folders
		}
		if depth(a.key) != depth(b.key) {
			return depth(a.key) < depth(b.key)
		}
		return a.key < b.key
	})
	return p
}

// deletions counts the files a plan deletes on each side.
func (p plan) deletions() (local, remote int) {
	for _, o := range p.ops {
		switch {
		case o.kind == opDeleteLocal && o.b != nil && !o.b.Dir:
			local++
		case o.kind == opDeleteRemote && o.b != nil && !o.b.Dir:
			remote++
		}
	}
	return
}
