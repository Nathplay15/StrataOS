// Package engine synchronizes local folders with folders of the NAS in both
// directions. Each folder pair keeps three views: the local tree (scanned),
// the remote tree (followed with the change feed of the NAS) and the state
// both sides had at the last synchronization (the base). Comparing the
// three tells which side changed: a change on one side is copied to the
// other, changes on both sides keep both versions (a conflict copy), and
// deletions go to the trash or the NAS recycle bin. Nothing is overwritten
// or deleted without checking it is still what was planned.
package engine

import (
	"context"
	"errors"
	"io"
	"time"
)

// Pair is a local folder synchronized with a NAS folder.
type Pair struct {
	ID      string   `json:"id"`
	Local   string   `json:"local"`             // absolute local folder
	Remote  string   `json:"remote"`            // absolute NAS folder (/srv/strata/…)
	Exclude []string `json:"exclude,omitempty"` // subfolders not synchronized (slash paths below the pair)
	Paused  bool     `json:"paused,omitempty"`
}

// RemoteEntry is a file or folder of the NAS (MTime in Unix milliseconds).
type RemoteEntry struct {
	Path    string `json:"p"`
	Dir     bool   `json:"d,omitempty"`
	Size    int64  `json:"s,omitempty"`
	MTime   int64  `json:"m,omitempty"`
	Version string `json:"v,omitempty"`
	Hash    string `json:"h,omitempty"`
	ID      string `json:"i,omitempty"`
}

// LocalEntry is a file or folder of the computer (MTime in nanoseconds).
type LocalEntry struct {
	Path  string
	Dir   bool
	Size  int64
	MTime int64
	ID    uint64 // file ID (inode), 0 when unknown
}

// BaseEntry is what both sides had at the last synchronization.
type BaseEntry struct {
	Path       string `json:"p"`            // as named on the NAS
	LocalPath  string `json:"l,omitempty"`  // as named locally, when it differs (case)
	Dir        bool   `json:"d,omitempty"`  //
	Size       int64  `json:"s,omitempty"`  //
	LocalMTime int64  `json:"lm,omitempty"` // ns
	LocalID    uint64 `json:"li,omitempty"` //
	Version    string `json:"v,omitempty"`  // remote version
	RemoteID   string `json:"ri,omitempty"` //
	Hash       string `json:"h,omitempty"`  //
}

func (b *BaseEntry) local() string {
	if b.LocalPath != "" {
		return b.LocalPath
	}
	return b.Path
}

// Changes is a page of the NAS change feed.
type Changes struct {
	Cursor  string
	Reset   bool
	More    bool
	Entries []RemoteEntry
	Deleted []string // paths deleted
}

// UploadReq is a file to send.
type UploadReq struct {
	Rel      string
	Size     int64
	MTime    int64  // ms
	IfMatch  string // "" = must not exist
	Body     io.ReaderAt
	ResumeID string
	OnID     func(string)
	Progress func(int64)
}

// Remote is the NAS as the engine sees it (the API, or a fake in tests).
type Remote interface {
	Changes(ctx context.Context, folder, cursor string, wait time.Duration) (Changes, error)
	Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error)
	Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error)
	// Upload returns the new entry, or a *Conflict carrying the upload ID
	// (kept by the NAS) when the file changed there.
	Upload(ctx context.Context, folder string, u UploadReq) (RemoteEntry, error)
	Commit(ctx context.Context, folder, uploadID, rel, ifMatch string) (RemoteEntry, error)
	Mkdir(ctx context.Context, folder, rel string) (RemoteEntry, error)
	Move(ctx context.Context, folder, from, to, ifMatch string) (RemoteEntry, error)
	Delete(ctx context.Context, folder, rel, ifMatch string) error
}

// Conflict: the item changed on the NAS since the version the engine knew.
type Conflict struct {
	Msg      string
	Current  *RemoteEntry
	UploadID string // a finished upload kept for Commit
}

func (c *Conflict) Error() string { return "conflict: " + c.Msg }

var (
	// ErrNotFound: the NAS folder of a pair is gone (nothing is deleted locally).
	ErrNotFound = errors.New("not found on the NAS")
	// ErrUnauthorized: this computer was signed out of the NAS (revoked).
	ErrUnauthorized = errors.New("this computer is no longer paired with the NAS")
	// ErrChanged: the file changed on the NAS during a download.
	ErrChanged = errors.New("changed during the transfer")
)

// Event is something the user is told about.
type Event struct {
	Kind string // "conflict", "error", "brake", "skipped", "signedout"
	Pair string
	Path string
	Msg  string
}
