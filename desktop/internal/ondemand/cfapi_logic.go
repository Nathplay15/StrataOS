package ondemand

import (
	"strings"
)

// Pure parts of the Windows provider (cfapi_windows.go), tested on every
// system: the sync root ID, the identity kept in each placeholder, the
// chunks of a hydration request, paths given by the platform, and what a
// folder of the sync root needs to match the NAS.

// cfBlock: data handed to the platform starts and ends on 4 KiB, except at
// the end of the file.
const cfBlock = 4096

// SyncRootID is the ID of the sync root of an account, in the
// provider!SID!account form Windows expects.
func SyncRootID(sid, account string) string {
	return "StrataOS!" + sid + "!" + account
}

// encodeIdentity is the identity stored in a placeholder: whether it is a
// folder and the NAS version its metadata (and contents) belong to.
func encodeIdentity(dir bool, version string) []byte {
	if dir {
		return []byte("d1")
	}
	return []byte("f1:" + version)
}

func decodeIdentity(b []byte) (dir bool, version string, ok bool) {
	s := string(b)
	switch {
	case s == "d1":
		return true, "", true
	case strings.HasPrefix(s, "f1:"):
		return false, s[3:], true
	}
	return false, "", false
}

// transferChunks cuts the range a hydration asks for into the pieces sent
// with TRANSFER_DATA: widened to 4 KiB blocks (clipped to the size), at
// most piece bytes each (a multiple of 4 KiB).
func transferChunks(off, length, size, piece int64) []Range {
	if size <= 0 || off >= size {
		return nil
	}
	if length < 0 || off+length > size {
		length = size - off
	}
	start := off / cfBlock * cfBlock
	end := min((off+length+cfBlock-1)/cfBlock*cfBlock, size)
	if end <= start {
		return nil
	}
	piece = max(piece/cfBlock*cfBlock, cfBlock)
	return Range{start, end}.Split(piece)
}

// winRel turns a full path of the platform ("C:\Users\me\StrataOS - NAS\Docs\a.txt")
// into "Docs/a.txt" below root, ignoring case as Windows does.
func winRel(root, full string) (string, bool) {
	root = strings.TrimRight(strings.ReplaceAll(root, "/", `\`), `\`)
	full = strings.ReplaceAll(full, "/", `\`)
	if len(full) < len(root) || !strings.EqualFold(full[:len(root)], root) {
		return "", false
	}
	rest := full[len(root):]
	if rest == "" {
		return "", true
	}
	if rest[0] != '\\' {
		return "", false
	}
	return strings.ReplaceAll(strings.Trim(rest, `\`), `\`, "/"), true
}

// localEntry is an item found in a folder of the sync root.
type localEntry struct {
	Name        string
	Dir         bool
	Placeholder bool
	InSync      bool
	Version     string // from the identity
	Size        int64
	MTime       int64 // ms
}

// dirOpKind is what to do with one name of a folder.
type dirOpKind int

const (
	opCreate     dirOpKind = iota // a placeholder for the NAS item
	opUpdate                      // the placeholder is behind the NAS: new metadata, dehydrated
	opDeleteHere                  // gone from the NAS: remove the placeholder
	opUpload                      // a file of this computer (new, or changed): send it
	opMkdirThere                  // a folder of this computer: create it on the NAS
	opRecurse                     // a folder on both sides: look inside
)

type dirOp struct {
	Kind dirOpKind
	Name string
	Item *Item // the NAS item (create, update, recurse)
}

// planDir compares a folder of the sync root with the NAS. Windows names
// ignore case: a second NAS name differing only by case is left out.
// Local items not in sync are this computer's changes: they win until
// sent (the upload settles conflicts).
func planDir(local []localEntry, remote []Item) []dirOp {
	byName := map[string]*localEntry{}
	for i := range local {
		byName[strings.ToLower(local[i].Name)] = &local[i]
	}
	var ops []dirOp
	seen := map[string]bool{}
	for i := range remote {
		it := &remote[i]
		k := strings.ToLower(it.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		l := byName[k]
		switch {
		case l == nil:
			ops = append(ops, dirOp{Kind: opCreate, Name: it.Name, Item: it})
		case l.Dir != it.Dir:
			// A file here where the NAS has a folder (or the reverse): leave it.
		case l.Dir:
			if !l.Placeholder {
				ops = append(ops, dirOp{Kind: opMkdirThere, Name: l.Name})
			}
			ops = append(ops, dirOp{Kind: opRecurse, Name: l.Name, Item: it})
		case !l.Placeholder || !l.InSync:
			ops = append(ops, dirOp{Kind: opUpload, Name: l.Name, Item: it})
		case l.Version != it.Version:
			ops = append(ops, dirOp{Kind: opUpdate, Name: l.Name, Item: it})
		}
	}
	for i := range local {
		l := &local[i]
		if seen[strings.ToLower(l.Name)] {
			continue
		}
		switch {
		case l.Placeholder && l.InSync:
			ops = append(ops, dirOp{Kind: opDeleteHere, Name: l.Name})
		case l.Dir:
			ops = append(ops, dirOp{Kind: opMkdirThere, Name: l.Name}, dirOp{Kind: opRecurse, Name: l.Name})
		default:
			ops = append(ops, dirOp{Kind: opUpload, Name: l.Name})
		}
	}
	return ops
}

// ignoredLocal: files Windows and Office create that never go to the NAS.
func ignoredLocal(name string) bool {
	n := strings.ToLower(name)
	return n == "desktop.ini" || n == "thumbs.db" || strings.HasPrefix(n, "~$") || strings.HasSuffix(n, ".tmp") || strings.HasPrefix(n, "~wr")
}
