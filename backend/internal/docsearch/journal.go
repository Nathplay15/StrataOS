package docsearch

import (
	"errors"
	"os"
	"syscall"

	"strataos/internal/syncjournal"
)

// JournalSource reads the changes of a folder from its sync journal: the
// index follows the files changed over SMB, NFS, the web or the apps
// without walking the tree again.
type JournalSource struct {
	J    *syncjournal.Journal
	Root string
}

func (s JournalSource) Changes(cursor string, limit int) ([]Change, string, bool, bool, error) {
	page, err := s.J.Changes("", cursor, limit)
	if err != nil {
		return nil, "", false, false, err
	}
	out := make([]Change, 0, len(page.Changes))
	for _, e := range page.Changes {
		out = append(out, Change{Path: e.Path, Dir: e.Dir, Deleted: e.Deleted != 0, Size: e.Size, MTime: e.MTime / 1e9, Version: e.Version()})
	}
	return out, page.Cursor, page.Reset, page.More, nil
}

func (s JournalSource) Files() int {
	n, _ := s.J.Stats()
	return n
}

// Open opens a regular file through os.Root: a symbolic link cannot lead
// outside the folder, a FIFO cannot block the indexer.
func (s JournalSource) Open(rel string) (*os.File, error) {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("not a regular file")
	}
	return f, nil
}
