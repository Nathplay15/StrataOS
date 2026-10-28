package engine

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The state database (bbolt, one file): for each pair, the base (key →
// BaseEntry), the remote tree (NAS path → RemoteEntry) with the cursor of
// the change feed, and the transfers in progress (to resume them).

var (
	bBase    = []byte("base")
	bRemote  = []byte("remote")
	bMeta    = []byte("meta")
	bUploads = []byte("uploads")
	kCursor  = []byte("cursor")
)

type store struct{ db *bolt.DB }

func openStore(file string) (*store, error) {
	db, err := bolt.Open(file, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

func (s *store) close() error { return s.db.Close() }

func pairBucket(tx *bolt.Tx, id string, create bool) *bolt.Bucket {
	name := []byte("pair/" + id)
	if !create {
		return tx.Bucket(name)
	}
	b, err := tx.CreateBucketIfNotExists(name)
	if err != nil {
		return nil
	}
	for _, sub := range [][]byte{bBase, bRemote, bMeta, bUploads} {
		if _, err := b.CreateBucketIfNotExists(sub); err != nil {
			return nil
		}
	}
	return b
}

// pairData is what the database holds for a pair.
type pairData struct {
	base    map[string]*BaseEntry
	remote  map[string]*RemoteEntry // by NAS path
	cursor  string
	uploads map[string]uploadState
}

// uploadState is an upload in progress, resumed when the file is unchanged.
type uploadState struct {
	ID      string `json:"id"`
	Size    int64  `json:"s"`
	MTime   int64  `json:"m"`
	IfMatch string `json:"v"`
}

func (s *store) load(id string) (pairData, error) {
	d := pairData{base: map[string]*BaseEntry{}, remote: map[string]*RemoteEntry{}, uploads: map[string]uploadState{}}
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := pairBucket(tx, id, true)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		_ = b.Bucket(bBase).ForEach(func(k, v []byte) error {
			var e BaseEntry
			if json.Unmarshal(v, &e) == nil {
				d.base[string(k)] = &e
			}
			return nil
		})
		_ = b.Bucket(bRemote).ForEach(func(k, v []byte) error {
			var e RemoteEntry
			if json.Unmarshal(v, &e) == nil {
				d.remote[string(k)] = &e
			}
			return nil
		})
		_ = b.Bucket(bUploads).ForEach(func(k, v []byte) error {
			var u uploadState
			if json.Unmarshal(v, &u) == nil {
				d.uploads[string(k)] = u
			}
			return nil
		})
		d.cursor = string(b.Bucket(bMeta).Get(kCursor))
		return nil
	})
	return d, err
}

// change is a pending write: a nil value deletes the key.
type change struct {
	bucket []byte
	key    string
	val    any
}

// write applies changes (and the cursor when not nil) in one transaction.
func (s *store) write(id string, changes []change, cursor *string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := pairBucket(tx, id, true)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		for _, c := range changes {
			sb := b.Bucket(c.bucket)
			if c.val == nil {
				if err := sb.Delete([]byte(c.key)); err != nil {
					return err
				}
				continue
			}
			data, err := json.Marshal(c.val)
			if err != nil {
				return err
			}
			if err := sb.Put([]byte(c.key), data); err != nil {
				return err
			}
		}
		if cursor != nil {
			return b.Bucket(bMeta).Put(kCursor, []byte(*cursor))
		}
		return nil
	})
}

// replaceRemote swaps the whole remote tree (after a reset of the feed).
func (s *store) replaceRemote(id string, remote map[string]*RemoteEntry, cursor string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := pairBucket(tx, id, true)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		if err := b.DeleteBucket(bRemote); err != nil {
			return err
		}
		rb, err := b.CreateBucket(bRemote)
		if err != nil {
			return err
		}
		for p, e := range remote {
			data, _ := json.Marshal(e)
			if err := rb.Put([]byte(p), data); err != nil {
				return err
			}
		}
		return b.Bucket(bMeta).Put(kCursor, []byte(cursor))
	})
}

// drop forgets a pair.
func (s *store) drop(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("pair/"+id)) == nil {
			return nil
		}
		return tx.DeleteBucket([]byte("pair/" + id))
	})
}
