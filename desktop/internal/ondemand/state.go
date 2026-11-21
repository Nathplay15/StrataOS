package ondemand

import "errors"

// Status is what the file manager shows for a file.
type Status string

const (
	StCloud   Status = "cloud"   // only on the NAS: downloaded when opened
	StPartial Status = "partial" // parts read so far are on this computer
	StLocal   Status = "local"   // on this computer, may be freed
	StPinning Status = "pinning" // kept on this device, still downloading
	StPinned  Status = "pinned"  // kept on this device
	StDirty   Status = "dirty"   // changed here, not on the NAS yet
)

// FileState is what this computer holds of a file of the NAS: which bytes
// (of which version), whether the user keeps it here, whether it changed
// here. Every change goes through Apply, the state machine below, which
// says what to do next; the callers do the I/O.
type FileState struct {
	Key     string `json:"k"`           // name of the cached copy (Linux)
	Version string `json:"v,omitempty"` // version of the NAS the bytes belong to ("": a new file)
	Size    int64  `json:"s"`
	MTime   int64  `json:"m,omitempty"` // ms
	Ranges  Ranges `json:"r,omitempty"` // bytes present
	Pinned  bool   `json:"p,omitempty"` // "keep on this device"
	Dirty   bool   `json:"d,omitempty"` // changed here, to upload
	Gen     int64  `json:"g,omitempty"` // counts local writes (an upload of an older one keeps Dirty)
	Used    int64  `json:"u,omitempty"` // last use, Unix seconds (eviction order)
	Open    int    `json:"-"`           // open handles (never evicted while open)
}

// EventKind is something that happens to a file.
type EventKind int

const (
	EvFilled        EventKind = iota // bytes [Off, End) downloaded
	EvPin                            // "keep on this device"
	EvUnpin                          // "free up space" (unpins and frees)
	EvEvict                          // the cache needs room (automatic)
	EvOpen                           // a handle opened
	EvClose                          // a handle closed
	EvWrite                          // bytes [Off, End) written here
	EvTruncate                       // size set to Size here
	EvUploaded                       // the NAS has local generation Gen as Version
	EvRemoteChanged                  // the NAS has another Version (Size)
	EvRemoteDeleted                  // the file is gone from the NAS
)

// Event is an EventKind with its values.
type Event struct {
	Kind     EventKind
	Off, End int64
	Size     int64
	Version  string
	Gen      int64
}

// Action is what the caller must do after an event (several may be set,
// in this order: conflict, drop, hydrate, upload).
type Action int

const (
	ActConflict Action = 1 << iota // upload the local bytes under a conflict name, then follow the NAS
	ActDrop                        // delete the cached bytes
	ActHydrate                     // download what is missing
	ActUpload                      // upload the local bytes (Version: the one replaced, "": a new file)
	ActForget                      // the file is gone: forget its state
)

// Errors of refused events.
var (
	ErrBusy     = errors.New("the file is open")
	ErrNotSent  = errors.New("the file has changes not sent to the NAS yet")
	ErrKeptHere = errors.New("the file is kept on this device")
)

// Status of the file for the file manager.
func (f *FileState) Status() Status {
	switch {
	case f.Dirty:
		return StDirty
	case f.Pinned && f.Ranges.Complete(f.Size):
		return StPinned
	case f.Pinned:
		return StPinning
	case f.Size > 0 && f.Ranges.Complete(f.Size):
		return StLocal
	case f.Ranges.Total() > 0:
		return StPartial
	case f.Size == 0 && f.Version != "":
		return StLocal // nothing to download
	}
	return StCloud
}

// Evictable tells whether automatic eviction may free this file.
func (f *FileState) Evictable() bool {
	return !f.Dirty && !f.Pinned && f.Open == 0 && f.Ranges.Total() > 0
}

// Apply changes the state for an event and returns what to do. A refused
// event (freeing a file open or not sent) changes nothing.
func (f *FileState) Apply(ev Event) (Action, error) {
	switch ev.Kind {
	case EvFilled:
		if !f.Dirty {
			f.Ranges = f.Ranges.Add(ev.Off, min(ev.End, f.Size))
		}
	case EvPin:
		f.Pinned = true
		if !f.Dirty && !f.Ranges.Complete(f.Size) {
			return ActHydrate, nil
		}
	case EvUnpin:
		if f.Dirty {
			f.Pinned = false
			return 0, ErrNotSent
		}
		if f.Open > 0 {
			f.Pinned = false
			return 0, ErrBusy
		}
		f.Pinned = false
		if f.Ranges.Total() > 0 {
			f.Ranges = nil
			return ActDrop, nil
		}
	case EvEvict:
		switch {
		case f.Dirty:
			return 0, ErrNotSent
		case f.Open > 0:
			return 0, ErrBusy
		case f.Pinned:
			return 0, ErrKeptHere
		}
		if f.Ranges.Total() > 0 {
			f.Ranges = nil
			return ActDrop, nil
		}
	case EvOpen:
		f.Open++
	case EvClose:
		if f.Open > 0 {
			f.Open--
		}
		if f.Open == 0 && f.Dirty {
			return ActUpload, nil
		}
	case EvWrite:
		f.Dirty = true
		f.Gen++
		f.Ranges = f.Ranges.Add(ev.Off, ev.End)
		f.Size = max(f.Size, ev.End)
	case EvTruncate:
		f.Dirty = true
		f.Gen++
		if ev.Size < f.Size {
			f.Ranges = f.Ranges.Truncate(ev.Size)
		} else {
			f.Ranges = f.Ranges.Add(f.Size, ev.Size) // the new bytes are zeros, here
		}
		f.Size = ev.Size
	case EvUploaded:
		f.Version = ev.Version
		if ev.Gen == f.Gen {
			f.Dirty = false
		} else if f.Open == 0 {
			return ActUpload, nil // written again during the upload
		}
	case EvRemoteChanged:
		if ev.Version == f.Version {
			return 0, nil
		}
		var act Action
		if f.Dirty {
			// Both sides changed: the local bytes go to the NAS under a
			// conflict name, this path follows the NAS.
			act |= ActConflict
			f.Dirty = false
		}
		f.Version, f.Size = ev.Version, ev.Size
		if f.Ranges.Total() > 0 || act != 0 {
			act |= ActDrop
		}
		f.Ranges = nil
		if f.Pinned && f.Size > 0 {
			act |= ActHydrate
		}
		return act, nil
	case EvRemoteDeleted:
		if f.Dirty {
			// Changed here, deleted there: the changes win, as a new file.
			f.Version = ""
			if f.Open == 0 {
				return ActUpload, nil
			}
			return 0, nil
		}
		return ActDrop | ActForget, nil
	}
	return 0, nil
}
