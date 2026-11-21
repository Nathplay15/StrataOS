package backup

// Incremental image backups. The datastore stays a restic repository and
// every backup of a disk stays one complete raw stream (restores, single
// files and checks read it as before): an incremental run rebuilds that
// stream from the previous backup of the disk (restic dump, read from the
// repository) with the blocks changed since then read from the VM, and
// restic deduplicates the unchanged chunks. Only the changed blocks are read
// from the VM's storage: QEMU dirty bitmaps (libvirt checkpoints) for qcow2
// disks of running VMs, rbd export-diff between two snapshots for Ceph RBD.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// mergeStream writes a disk of size bytes to w: the changed extents (sorted,
// not overlapping) read from cur, everything else copied from prev, the
// stream of the previous backup of the same disk. It returns the bytes read
// from cur.
func mergeStream(w io.Writer, prev io.Reader, cur io.ReaderAt, size int64, changed []extent) (int64, error) {
	var pos, read int64
	buf := make([]byte, nbdChunk)
	for _, e := range changed {
		if e.Off < pos || e.Len <= 0 || e.Off+e.Len > size {
			return read, fmt.Errorf("invalid changed extent %d+%d", e.Off, e.Len)
		}
		if n, err := io.CopyN(w, prev, e.Off-pos); err != nil {
			return read, prevErr(n, e.Off-pos, err)
		}
		for off := e.Off; off < e.Off+e.Len; {
			l := min(int64(len(buf)), e.Off+e.Len-off)
			if _, err := cur.ReadAt(buf[:l], off); err != nil && !(errors.Is(err, io.EOF) && off+l == size) {
				return read, err
			}
			if _, err := w.Write(buf[:l]); err != nil {
				return read, err
			}
			read += l
			off += l
		}
		if n, err := io.CopyN(io.Discard, prev, e.Len); err != nil {
			return read, prevErr(n, e.Len, err)
		}
		pos = e.Off + e.Len
	}
	if n, err := io.CopyN(w, prev, size-pos); err != nil {
		return read, prevErr(n, size-pos, err)
	}
	return read, nil
}

func prevErr(n, want int64, err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("the previous backup of the disk is shorter than the disk (%d of %d bytes)", n, want)
	}
	return err
}

// The rbd export-diff format (v1): a header line, then records tagged by
// one byte, integers little endian: f/t (from/to snapshot: u32 length +
// name), s (image size: u64), w (u64 offset, u64 length, data), z (zeroed:
// u64 offset, u64 length), e (end).
const rbdDiffHeader = "rbd diff v1\n"

// rbdDiffInfo is what a diff tells about itself.
type rbdDiffInfo struct {
	From, To string
	Size     int64
	Read     int64 // bytes of data records
	Zeroed   int64
}

// applyRBDDiff writes to w the image described by prev (the previous backup
// of the disk, sizePrev bytes) patched with an rbd export-diff stream. The
// image must keep its size (a resized image is backed up in full).
func applyRBDDiff(w io.Writer, prev io.Reader, sizePrev int64, diff io.Reader) (rbdDiffInfo, error) {
	var info rbdDiffInfo
	r := bufio.NewReaderSize(diff, 1<<20)
	head := make([]byte, len(rbdDiffHeader))
	if _, err := io.ReadFull(r, head); err != nil || string(head) != rbdDiffHeader {
		return info, fmt.Errorf("not an rbd diff stream")
	}
	le := binary.LittleEndian
	u64 := func() (int64, error) {
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		v := le.Uint64(b[:])
		if v > 1<<62 {
			return 0, fmt.Errorf("rbd diff: value out of range")
		}
		return int64(v), nil
	}
	name := func() (string, error) {
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		l := le.Uint32(b[:])
		if l > 4096 {
			return "", fmt.Errorf("rbd diff: name too long")
		}
		s := make([]byte, l)
		_, err := io.ReadFull(r, s)
		return string(s), err
	}
	info.Size = -1
	var pos int64
	zeros := make([]byte, 1<<20)
	for {
		tag, err := r.ReadByte()
		if err != nil {
			return info, fmt.Errorf("rbd diff: %w", err)
		}
		switch tag {
		case 'f':
			if info.From, err = name(); err != nil {
				return info, err
			}
		case 't':
			if info.To, err = name(); err != nil {
				return info, err
			}
		case 's':
			if info.Size, err = u64(); err != nil {
				return info, err
			}
			if info.Size != sizePrev {
				return info, fmt.Errorf("the image size changed (%d → %d bytes)", sizePrev, info.Size)
			}
		case 'w', 'z':
			off, err := u64()
			if err != nil {
				return info, err
			}
			l, err := u64()
			if err != nil {
				return info, err
			}
			if info.Size < 0 || off < pos || off+l > info.Size {
				return info, fmt.Errorf("rbd diff: record out of order or past the end (%d+%d)", off, l)
			}
			if n, err := io.CopyN(w, prev, off-pos); err != nil {
				return info, prevErr(n, off-pos, err)
			}
			if tag == 'w' {
				if _, err := io.CopyN(w, r, l); err != nil {
					return info, fmt.Errorf("rbd diff: %w", err)
				}
				info.Read += l
			} else {
				for left := l; left > 0; {
					k := min(left, int64(len(zeros)))
					if _, err := w.Write(zeros[:k]); err != nil {
						return info, err
					}
					left -= k
				}
				info.Zeroed += l
			}
			if n, err := io.CopyN(io.Discard, prev, l); err != nil {
				return info, prevErr(n, l, err)
			}
			pos = off + l
		case 'e':
			if info.Size < 0 {
				return info, fmt.Errorf("rbd diff: no image size")
			}
			if n, err := io.CopyN(w, prev, info.Size-pos); err != nil {
				return info, prevErr(n, info.Size-pos, err)
			}
			return info, nil
		default:
			return info, fmt.Errorf("rbd diff: unknown record %q", tag)
		}
	}
}
