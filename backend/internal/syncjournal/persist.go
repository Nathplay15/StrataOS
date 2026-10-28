package syncjournal

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
)

type snapshot struct {
	Version int
	Root    string
	Epoch   string
	Seq     int64
	MinSeq  int64
	Dev     uint64
	Entries []Entry
}

// seqJump is added to the sequence number of a loaded index: changes
// numbered after the last save were possibly seen by clients, and are found
// again by the next scan under numbers those clients have not seen yet.
const seqJump = 1 << 32

// Save writes the index (gzipped gob) when it changed since the last save.
func (j *Journal) Save(file string) error {
	j.mu.Lock()
	if !j.dirty {
		j.mu.Unlock()
		return nil
	}
	snap := snapshot{Version: 1, Root: j.Root, Epoch: j.epoch, Seq: j.seq, MinSeq: j.minSeq, Dev: j.dev, Entries: make([]Entry, 0, len(j.entries))}
	for _, e := range j.entries {
		snap.Entries = append(snap.Entries, *e)
	}
	j.dirty = false
	j.mu.Unlock()
	err := writeSnapshot(file, &snap)
	if err != nil {
		j.mu.Lock()
		j.dirty = true
		j.mu.Unlock()
	}
	return err
}

func writeSnapshot(file string, snap *snapshot) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	zw, _ := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	err = gob.NewEncoder(zw).Encode(snap)
	err = errors.Join(err, zw.Close(), bw.Flush(), f.Sync(), f.Close())
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

// Load reads an index saved for root; a missing, damaged or foreign file
// gives a new empty index (clients then reset).
func Load(file, root string) *Journal {
	j := New(root)
	f, err := os.Open(file)
	if err != nil {
		return j
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return j
	}
	var snap snapshot
	if gob.NewDecoder(zr).Decode(&snap) != nil || snap.Version != 1 || snap.Root != root || snap.Epoch == "" {
		return j
	}
	j.epoch, j.seq, j.minSeq, j.dev = snap.Epoch, snap.Seq+seqJump, snap.MinSeq, snap.Dev
	for i := range snap.Entries {
		e := &snap.Entries[i]
		j.entries[e.Path] = e
		if e.Deleted == 0 {
			pd, name := parent(e.Path), filepath.Base(e.Path)
			if j.kids[pd] == nil {
				j.kids[pd] = map[string]bool{}
			}
			j.kids[pd][name] = true
			if e.Dir && j.kids[e.Path] == nil {
				j.kids[e.Path] = map[string]bool{}
			}
		}
		if !e.Dir && e.Hash != "" {
			j.hashes[hkey{e.Ino, e.Size, e.MTime}] = e.Hash
		}
	}
	j.compactLocked()
	return j
}
