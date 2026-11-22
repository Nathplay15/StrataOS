package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"encoding/binary"
	"math/rand/v2"
	"strataos/internal/store"
	"strings"
	"testing"
)

func randBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return b
}

func TestMergeStream(t *testing.T) {
	const size = 10<<20 + 123
	prev := randBytes(size)
	cur := bytes.Clone(prev)
	changed := []extent{{0, 4096}, {1 << 20, 64 << 10}, {5 << 20, 4<<20 + 7}, {size - 100, 100}}
	for _, e := range changed {
		copy(cur[e.Off:e.Off+e.Len], randBytes(int(e.Len)))
	}
	cur[3<<20] ^= 0xff // a change the bitmap did not report: must come from prev
	var out bytes.Buffer
	read, err := mergeStream(&out, bytes.NewReader(prev), bytes.NewReader(cur), size, changed)
	if err != nil {
		t.Fatal(err)
	}
	want := 4096 + 64<<10 + 4<<20 + 7 + 100
	if read != int64(want) {
		t.Fatalf("read %d, want %d", read, want)
	}
	exp := bytes.Clone(cur)
	exp[3<<20] = prev[3<<20]
	if !bytes.Equal(out.Bytes(), exp) {
		t.Fatal("merged stream differs")
	}
	// Nothing changed: a copy of prev.
	out.Reset()
	if read, err := mergeStream(&out, bytes.NewReader(prev), bytes.NewReader(cur), size, nil); err != nil || read != 0 || !bytes.Equal(out.Bytes(), prev) {
		t.Fatal("empty bitmap", read, err)
	}
	// Errors: overlapping extents, a short previous backup.
	if _, err := mergeStream(&out, bytes.NewReader(prev), bytes.NewReader(cur), size, []extent{{100, 10}, {105, 10}}); err == nil {
		t.Fatal("overlap accepted")
	}
	if _, err := mergeStream(&out, bytes.NewReader(prev[:size-1]), bytes.NewReader(cur), size, nil); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatal("short previous backup accepted", err)
	}
}

// rbdDiff builds an export-diff stream.
type rbdDiff struct{ bytes.Buffer }

func newRBDDiff(from, to string, size int64) *rbdDiff {
	d := &rbdDiff{}
	d.WriteString(rbdDiffHeader)
	for tag, n := range map[byte]string{'f': from, 't': to} {
		if n != "" {
			d.WriteByte(tag)
			binary.Write(d, binary.LittleEndian, uint32(len(n)))
			d.WriteString(n)
		}
	}
	d.WriteByte('s')
	binary.Write(d, binary.LittleEndian, uint64(size))
	return d
}

func (d *rbdDiff) rec(tag byte, off int64, data []byte, l int64) {
	d.WriteByte(tag)
	binary.Write(d, binary.LittleEndian, uint64(off))
	binary.Write(d, binary.LittleEndian, uint64(l))
	d.Write(data)
}

func TestApplyRBDDiff(t *testing.T) {
	const size = 8 << 20
	prev := randBytes(size)
	cur := bytes.Clone(prev)
	d := newRBDDiff("strata-a", "strata-b", size)
	w1 := randBytes(4 << 20)
	copy(cur[1<<20:], w1)
	d.rec('w', 1<<20, w1, int64(len(w1)))
	clear(cur[6<<20 : 7<<20])
	d.rec('z', 6<<20, nil, 1<<20)
	w2 := randBytes(10)
	copy(cur[size-10:], w2)
	d.rec('w', size-10, w2, 10)
	d.WriteByte('e')
	var out bytes.Buffer
	info, err := applyRBDDiff(&out, bytes.NewReader(prev), size, d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), cur) {
		t.Fatal("patched image differs")
	}
	if info.From != "strata-a" || info.To != "strata-b" || info.Read != 4<<20+10 || info.Zeroed != 1<<20 {
		t.Fatalf("info %+v", info)
	}

	bad := func(name string, d *rbdDiff, sizePrev int64, want string) {
		t.Helper()
		if _, err := applyRBDDiff(&out, bytes.NewReader(prev), sizePrev, d); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	bad("resized", newRBDDiff("", "x", size*2), size, "size changed")
	d = newRBDDiff("", "x", size)
	d.rec('w', 2<<20, make([]byte, 10), 10)
	d.rec('w', 1<<20, make([]byte, 10), 10)
	bad("out of order", d, size, "out of order")
	d = newRBDDiff("", "x", size)
	d.rec('w', size-5, make([]byte, 10), 10)
	bad("past the end", d, size, "past the end")
	d = newRBDDiff("", "x", size)
	d.WriteByte('q')
	bad("unknown record", d, size, "unknown record")
	d = newRBDDiff("", "x", size)
	d.rec('w', 0, make([]byte, 3), 10) // truncated data
	bad("truncated", d, size, "EOF")
	bad("not a diff", &rbdDiff{}, size, "not an rbd diff")
}

func TestPatchedStream(t *testing.T) {
	const size = 3<<20 + 5
	prev := randBytes(size)
	cur := randBytes(size)
	defer func(f func(context.Context, *store.BackupTask, string, string, io.Writer) error) { dumpPart = f }(dumpPart)
	dumpPart = func(_ context.Context, _ *store.BackupTask, snap, file string, w io.Writer) error {
		if snap != "abcd1234" || file != "/vda.raw" {
			return fmt.Errorf("asked %s %s", snap, file)
		}
		_, err := io.Copy(w, bytes.NewReader(prev))
		return err
	}
	ext := []extent{{1 << 20, 1 << 20}}
	r, wait := patchedStream(context.Background(), &store.BackupTask{}, "abcd1234", "vda", func(w io.Writer, p io.Reader) (int64, error) {
		return mergeStream(w, p, bytes.NewReader(cur), size, ext)
	})
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	n, err := wait()
	if err != nil || n != 1<<20 {
		t.Fatal(n, err)
	}
	want := bytes.Clone(prev)
	copy(want[1<<20:2<<20], cur[1<<20:2<<20])
	if !bytes.Equal(got, want) {
		t.Fatal("patched stream differs")
	}
	// The consumer (restic) stops early: everything ends, with an error.
	r, wait = patchedStream(context.Background(), &store.BackupTask{}, "abcd1234", "vda", func(w io.Writer, p io.Reader) (int64, error) {
		return mergeStream(w, p, bytes.NewReader(cur), size, ext)
	})
	io.CopyN(io.Discard, r, 1000)
	r.CloseWithError(fmt.Errorf("restic failed"))
	if _, err := wait(); err == nil {
		t.Fatal("early stop not reported")
	}
	// The previous backup cannot be read.
	dumpPart = func(context.Context, *store.BackupTask, string, string, io.Writer) error {
		return fmt.Errorf("repository locked")
	}
	r, wait = patchedStream(context.Background(), &store.BackupTask{}, "abcd1234", "vda", func(w io.Writer, p io.Reader) (int64, error) {
		return mergeStream(w, p, bytes.NewReader(cur), size, ext)
	})
	io.Copy(io.Discard, r)
	if _, err := wait(); err == nil || !strings.Contains(err.Error(), "repository locked") {
		t.Fatal("dump error lost", err)
	}
}
