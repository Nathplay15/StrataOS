package ondemand

import (
	"encoding/json"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The state of files on demand (bbolt, ondemand.db in the account folder):
// for each shared folder its tree and cursor ("tree/<share>"), and the
// state of every file this computer holds ("files", by path).

var (
	bFiles  = []byte("files")
	kCursor = []byte("\x00cursor")
	kHidden = []byte("\x00hidden")
)

type db struct{ b *bolt.DB }

func openDB(file string) (*db, error) {
	b, err := bolt.Open(file, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	err = b.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bFiles)
		return err
	})
	return &db{b}, err
}

func treeBucket(share string) []byte { return []byte("tree/" + share) }

// loadTree reads the saved tree of a shared folder (empty: none).
func (d *db) loadTree(share string) *Tree {
	t := NewTree()
	var nodes []Node
	var hidden []string
	cursor := ""
	_ = d.b.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(treeBucket(share))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			switch string(k) {
			case string(kCursor):
				cursor = string(v)
			case string(kHidden):
				_ = json.Unmarshal(v, &hidden)
			default:
				var n Node
				if json.Unmarshal(v, &n) == nil {
					nodes = append(nodes, n)
				}
			}
			return nil
		})
	})
	t.Restore(nodes, hidden, cursor)
	return t
}

// saveChanges writes the items that changed and the cursor (all: the whole tree).
func (d *db) saveTree(share string, t *Tree, changed []Change, all bool) error {
	return d.b.Update(func(tx *bolt.Tx) error {
		name := treeBucket(share)
		if all {
			if tx.Bucket(name) != nil {
				if err := tx.DeleteBucket(name); err != nil {
					return err
				}
			}
		}
		b, err := tx.CreateBucketIfNotExists(name)
		if err != nil {
			return err
		}
		if all {
			nodes, _, _ := t.Snapshot()
			for _, n := range nodes {
				v, _ := json.Marshal(n)
				if err := b.Put([]byte(n.Path), v); err != nil {
					return err
				}
			}
		} else {
			for _, c := range changed {
				n, ok := t.Get(c.Path)
				if !ok {
					if err := b.Delete([]byte(c.Path)); err != nil {
						return err
					}
					continue
				}
				v, _ := json.Marshal(n)
				if err := b.Put([]byte(c.Path), v); err != nil {
					return err
				}
			}
		}
		_, hidden, cursor := t.Snapshot()
		h, _ := json.Marshal(hidden)
		if err := b.Put(kHidden, h); err != nil {
			return err
		}
		return b.Put(kCursor, []byte(cursor))
	})
}

// shareNames are the shared folders with a saved tree.
func (d *db) shareNames() []string {
	var out []string
	_ = d.b.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if n, ok := strings.CutPrefix(string(name), "tree/"); ok {
				out = append(out, n)
			}
			return nil
		})
	})
	return out
}

func (d *db) dropTree(share string) error {
	return d.b.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(treeBucket(share)) == nil {
			return nil
		}
		return tx.DeleteBucket(treeBucket(share))
	})
}

func (d *db) loadFiles() map[string]*FileState {
	out := map[string]*FileState{}
	_ = d.b.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bFiles).ForEach(func(k, v []byte) error {
			var f FileState
			if json.Unmarshal(v, &f) == nil {
				out[string(k)] = &f
			}
			return nil
		})
	})
	return out
}

// saveFiles writes the states given (nil: deleted).
func (d *db) saveFiles(m map[string]*FileState) error {
	return d.b.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bFiles)
		for p, f := range m {
			if f == nil {
				if err := b.Delete([]byte(p)); err != nil {
					return err
				}
				continue
			}
			v, _ := json.Marshal(f)
			if err := b.Put([]byte(p), v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *db) close() error { return d.b.Close() }
