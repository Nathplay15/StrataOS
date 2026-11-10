package backup

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeNBD serves disk (in memory) to one client, as qemu-nbd does.
func fakeNBD(t *testing.T, c net.Conn, disk []byte, readOnly bool) {
	defer c.Close()
	be := binary.BigEndian
	hello := make([]byte, 18)
	be.PutUint64(hello, nbdMagic)
	be.PutUint64(hello[8:], nbdOptMagic)
	be.PutUint16(hello[16:], nbdFlagFixedNewstyle|nbdFlagNoZeroes)
	c.Write(hello)
	var cflags uint32
	binary.Read(c, be, &cflags)
	var opt struct {
		Magic       uint64
		Opt, Length uint32
	}
	binary.Read(c, be, &opt)
	io.CopyN(io.Discard, c, int64(opt.Length))
	if opt.Opt != nbdOptGo {
		t.Errorf("option %d", opt.Opt)
		return
	}
	reply := func(typ uint32, data []byte) {
		h := make([]byte, 20)
		be.PutUint64(h, nbdRepMagic)
		be.PutUint32(h[8:], nbdOptGo)
		be.PutUint32(h[12:], typ)
		be.PutUint32(h[16:], uint32(len(data)))
		c.Write(append(h, data...))
	}
	info := make([]byte, 12)
	be.PutUint64(info[2:], uint64(len(disk)))
	flags := uint16(1)
	if readOnly {
		flags |= nbdTransReadOnly
	}
	be.PutUint16(info[10:], flags)
	reply(nbdRepInfo, info)
	reply(nbdRepAck, nil)
	for {
		h := make([]byte, 28)
		if _, err := io.ReadFull(c, h); err != nil {
			return
		}
		cmd, handle, off, l := be.Uint16(h[6:]), be.Uint64(h[8:]), be.Uint64(h[16:]), be.Uint32(h[24:])
		rep := make([]byte, 16)
		be.PutUint32(rep, nbdSimpleMagic)
		be.PutUint64(rep[8:], handle)
		switch cmd {
		case nbdCmdRead:
			c.Write(append(rep, disk[off:off+uint64(l)]...))
		case nbdCmdWrite:
			io.ReadFull(c, disk[off:off+uint64(l)])
			c.Write(rep)
		case nbdCmdFlush:
			c.Write(rep)
		case nbdCmdDisc:
			return
		}
	}
}

func TestNBDClient(t *testing.T) {
	disk := make([]byte, 9<<20+123)
	for i := range disk {
		disk[i] = byte(i * 7)
	}
	a, b := net.Pipe()
	go fakeNBD(t, b, disk, false)
	n, err := nbdHandshake(a)
	if err != nil {
		t.Fatal(err)
	}
	if n.size != int64(len(disk)) || n.readOnly {
		t.Fatalf("size %d ro %v", n.size, n.readOnly)
	}
	var out bytes.Buffer
	if _, err := io.Copy(&out, &nbdReader{n: n}); err != nil || !bytes.Equal(out.Bytes(), disk) {
		t.Fatalf("stream differs: %v (%d bytes)", err, out.Len())
	}
	if _, err := n.WriteAt([]byte("hello"), 5<<20); err != nil || string(disk[5<<20:5<<20+5]) != "hello" {
		t.Fatalf("write: %v", err)
	}
	if _, err := n.WriteAt([]byte("x"), int64(len(disk))); err == nil {
		t.Fatal("write past the end accepted")
	}
	if err := n.Flush(); err != nil {
		t.Fatal(err)
	}
	n.Close()

	a, b = net.Pipe()
	go fakeNBD(t, b, disk, true)
	n, err = nbdHandshake(a)
	if err != nil || !n.readOnly {
		t.Fatalf("read-only export: %v", err)
	}
	if _, err := n.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("write to a read-only export accepted")
	}
	n.Close()
}

type memAt struct{ b []byte }

func (m *memAt) WriteAt(p []byte, off int64) (int, error) {
	if int(off)+len(p) > len(m.b) {
		m.b = append(m.b, make([]byte, int(off)+len(p)-len(m.b))...)
	}
	return copy(m.b[off:], p), nil
}

func TestZeroWriter(t *testing.T) {
	src := make([]byte, 5*zeroBlock+77)
	copy(src[zeroBlock+10:], "data")
	src[len(src)-1] = 9
	m := &memAt{}
	zw := newZeroWriter(m)
	writes := 0
	zw.w = writerAtFunc(func(p []byte, off int64) (int, error) { writes++; return m.WriteAt(p, off) })
	for i := 0; i < len(src); i += 333333 {
		zw.Write(src[i:min(len(src), i+333333)])
	}
	n, err := zw.Close()
	if err != nil || n != int64(len(src)) {
		t.Fatal(n, err)
	}
	if writes != 2 {
		t.Fatalf("zero blocks must be skipped: %d writes", writes)
	}
	got := append(m.b, make([]byte, len(src)-len(m.b))...)
	if !bytes.Equal(got, src) {
		t.Fatal("content differs")
	}
}

type writerAtFunc func([]byte, int64) (int, error)

func (f writerAtFunc) WriteAt(p []byte, off int64) (int, error) { return f(p, off) }

// With qemu-nbd installed: a qcow2 written through the client reads back the same.
func TestQemuNBD(t *testing.T) {
	if _, err := exec.LookPath("qemu-nbd"); err != nil {
		t.Skip("qemu-nbd not installed")
	}
	dir := t.TempDir()
	NBDRun = filepath.Join(dir, "run")
	img := filepath.Join(dir, "d.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", img, "64M").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(rand.IntN(256))
	}
	ctx := context.Background()
	n, done, err := openImage(ctx, img, "qcow2", true)
	if err != nil {
		t.Fatal(err)
	}
	zw := newZeroWriter(n)
	zw.Write(make([]byte, 10<<20))
	zw.Write(data)
	zw.Write(make([]byte, 64<<20-13<<20))
	if total, err := zw.Close(); err != nil || total != 64<<20 {
		t.Fatal(total, err)
	}
	n.Flush()
	done()
	n, done, err = openImage(ctx, img, "qcow2", false)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var out bytes.Buffer
	io.Copy(&out, &nbdReader{n: n})
	if out.Len() != 64<<20 || !bytes.Equal(out.Bytes()[10<<20:13<<20], data) || !allZero(out.Bytes()[:10<<20]) {
		t.Fatalf("read back %d bytes", out.Len())
	}
	if _, _, err := openImage(ctx, img+",x", "qcow2", false); err == nil {
		t.Fatal("comma in path accepted")
	}
	if _, _, err := openImage(ctx, img, "vmdk", false); err == nil {
		t.Fatal("format accepted")
	}
	if entries, _ := os.ReadDir(NBDRun); len(entries) > 1 {
		t.Fatalf("sockets left: %d", len(entries))
	}
}
