package backup

// A small NBD client (fixed newstyle handshake, simple replies) talking to
// a qemu-nbd started on a private unix socket. Image backups read VM disks
// through it as raw streams, and restores write them back, without a
// kernel nbd device: no module to load, and no partitions or LVM volumes of
// the guests appearing on the NAS (where udev would activate them).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	nbdMagic       = 0x4e42444d41474943 // "NBDMAGIC"
	nbdOptMagic    = 0x49484156454f5054 // "IHAVEOPT"
	nbdRepMagic    = 0x0003e889045565a9
	nbdReqMagic    = 0x25609513
	nbdSimpleMagic = 0x67446698

	nbdFlagFixedNewstyle = 1 << 0
	nbdFlagNoZeroes      = 1 << 1

	nbdOptGo      = 7
	nbdRepAck     = 1
	nbdRepInfo    = 3
	nbdInfoExport = 0

	nbdCmdRead  = 0
	nbdCmdWrite = 1
	nbdCmdDisc  = 2
	nbdCmdFlush = 3

	nbdTransReadOnly = 1 << 1

	// nbdChunk is the size of one read or write request (qemu-nbd accepts 32 MB).
	nbdChunk = 4 << 20
)

// nbdConn is an open NBD export.
type nbdConn struct {
	mu       sync.Mutex
	c        io.ReadWriteCloser
	size     int64
	readOnly bool
	handle   uint64
}

// nbdHandshake negotiates the default export ("") of a fixed newstyle server.
func nbdHandshake(c io.ReadWriteCloser) (*nbdConn, error) {
	var hello struct {
		Magic, Opt uint64
		Flags      uint16
	}
	if err := binary.Read(c, binary.BigEndian, &hello); err != nil {
		return nil, fmt.Errorf("nbd: handshake: %w", err)
	}
	if hello.Magic != nbdMagic || hello.Opt != nbdOptMagic || hello.Flags&nbdFlagFixedNewstyle == 0 {
		return nil, fmt.Errorf("nbd: not a fixed newstyle server")
	}
	cflags := uint32(nbdFlagFixedNewstyle)
	if hello.Flags&nbdFlagNoZeroes != 0 {
		cflags |= nbdFlagNoZeroes
	}
	// NBD_OPT_GO: export name "" and no information requests.
	var opt bytes.Buffer
	_ = binary.Write(&opt, binary.BigEndian, cflags)
	_ = binary.Write(&opt, binary.BigEndian, uint64(nbdOptMagic))
	_ = binary.Write(&opt, binary.BigEndian, uint32(nbdOptGo))
	_ = binary.Write(&opt, binary.BigEndian, uint32(6))
	_ = binary.Write(&opt, binary.BigEndian, uint32(0)) // name length
	_ = binary.Write(&opt, binary.BigEndian, uint16(0)) // info requests
	if _, err := c.Write(opt.Bytes()); err != nil {
		return nil, err
	}
	n := &nbdConn{c: c, size: -1}
	for {
		var rep struct {
			Magic    uint64
			Opt, Typ uint32
			Length   uint32
		}
		if err := binary.Read(c, binary.BigEndian, &rep); err != nil {
			return nil, fmt.Errorf("nbd: option reply: %w", err)
		}
		if rep.Magic != nbdRepMagic || rep.Length > 1<<20 {
			return nil, fmt.Errorf("nbd: bad option reply")
		}
		data := make([]byte, rep.Length)
		if _, err := io.ReadFull(c, data); err != nil {
			return nil, err
		}
		switch {
		case rep.Typ&(1<<31) != 0:
			return nil, fmt.Errorf("nbd: export refused (%d): %s", rep.Typ&^(1<<31), strings.TrimSpace(string(data)))
		case rep.Typ == nbdRepInfo:
			if len(data) >= 12 && binary.BigEndian.Uint16(data) == nbdInfoExport {
				n.size = int64(binary.BigEndian.Uint64(data[2:]))
				n.readOnly = binary.BigEndian.Uint16(data[10:])&nbdTransReadOnly != 0
			}
		case rep.Typ == nbdRepAck:
			if n.size < 0 {
				return nil, fmt.Errorf("nbd: the server did not give the export size")
			}
			return n, nil
		}
	}
}

// request sends one command and reads its simple reply (data into buf for reads).
func (n *nbdConn) request(cmd uint16, off int64, length uint32, data, buf []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handle++
	h := make([]byte, 28, 28+len(data))
	binary.BigEndian.PutUint32(h[0:], nbdReqMagic)
	binary.BigEndian.PutUint16(h[4:], 0)
	binary.BigEndian.PutUint16(h[6:], cmd)
	binary.BigEndian.PutUint64(h[8:], n.handle)
	binary.BigEndian.PutUint64(h[16:], uint64(off))
	binary.BigEndian.PutUint32(h[24:], length)
	if _, err := n.c.Write(append(h, data...)); err != nil {
		return err
	}
	if cmd == nbdCmdDisc {
		return nil
	}
	var rep [16]byte
	if _, err := io.ReadFull(n.c, rep[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(rep[0:]) != nbdSimpleMagic || binary.BigEndian.Uint64(rep[8:]) != n.handle {
		return fmt.Errorf("nbd: unexpected reply")
	}
	if code := binary.BigEndian.Uint32(rep[4:]); code != 0 {
		return fmt.Errorf("nbd: request failed (error %d)", code)
	}
	if buf != nil {
		_, err := io.ReadFull(n.c, buf)
		return err
	}
	return nil
}

func (n *nbdConn) ReadAt(p []byte, off int64) (int, error) {
	done := 0
	for done < len(p) {
		if off >= n.size {
			return done, io.EOF
		}
		l := min(len(p)-done, nbdChunk, int(n.size-off))
		if err := n.request(nbdCmdRead, off, uint32(l), nil, p[done:done+l]); err != nil {
			return done, err
		}
		done += l
		off += int64(l)
	}
	return done, nil
}

func (n *nbdConn) WriteAt(p []byte, off int64) (int, error) {
	if n.readOnly {
		return 0, fmt.Errorf("nbd: read-only export")
	}
	if off+int64(len(p)) > n.size {
		return 0, fmt.Errorf("nbd: write past the end of the disk")
	}
	done := 0
	for done < len(p) {
		l := min(len(p)-done, nbdChunk)
		if err := n.request(nbdCmdWrite, off, uint32(l), p[done:done+l], nil); err != nil {
			return done, err
		}
		done += l
		off += int64(l)
	}
	return done, nil
}

func (n *nbdConn) Flush() error { return n.request(nbdCmdFlush, 0, 0, nil, nil) }

// Close ends the session (the server exits: it serves one client).
func (n *nbdConn) Close() error {
	_ = n.request(nbdCmdDisc, 0, 0, nil, nil)
	return n.c.Close()
}

// nbdReader reads an export from start to end.
type nbdReader struct {
	n   *nbdConn
	off int64
}

func (r *nbdReader) Read(p []byte) (int, error) {
	if r.off >= r.n.size {
		return 0, io.EOF
	}
	if len(p) > nbdChunk {
		p = p[:nbdChunk]
	}
	n, err := r.n.ReadAt(p, r.off)
	r.off += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// NBDRun is the directory of the qemu-nbd sockets.
var NBDRun = "/run/strata/nbd"

// openImage serves a disk image with qemu-nbd on a private socket and
// connects to it. format comes from the VM definition (never probed: a
// guest could write an image header into a raw disk).
func openImage(ctx context.Context, image, format string, write bool) (*nbdConn, func(), error) {
	if format != "raw" && format != "qcow2" {
		return nil, nil, fmt.Errorf("unsupported disk format %q", format)
	}
	if !filepath.IsAbs(image) || strings.ContainsAny(image, ",\n\r") {
		return nil, nil, fmt.Errorf("unsupported disk path %s", image)
	}
	fi, err := os.Stat(image)
	if err != nil {
		return nil, nil, err
	}
	driver := "file"
	if fi.Mode()&os.ModeDevice != 0 {
		driver = "host_device"
	} else if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a disk image", image)
	}
	if err := os.MkdirAll(NBDRun, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(NBDRun, "s-")
	if err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(dir, "sock")
	// Locking off: the disk may be in use by the paused VM, or be the frozen
	// base of a running VM's temporary snapshot.
	opts := fmt.Sprintf("driver=%s,file.driver=%s,file.filename=%s,file.locking=off", format, driver, image)
	args := []string{"--socket=" + sock, "--image-opts", "--cache=none", "--aio=threads"}
	if !write {
		args = append(args, "--read-only")
	}
	args = append(args, opts)
	cmd := exec.CommandContext(ctx, "qemu-nbd", args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = os.RemoveAll(dir)
	}
	var conn net.Conn
	for i := 0; ; i++ {
		select {
		case err := <-exited:
			_ = os.RemoveAll(dir)
			return nil, nil, fmt.Errorf("qemu-nbd: %v %s", err, strings.TrimSpace(errb.String()))
		default:
		}
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
		if i > 300 {
			_ = cmd.Process.Kill()
			stop()
			return nil, nil, fmt.Errorf("qemu-nbd did not start: %s", strings.TrimSpace(errb.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	n, err := nbdHandshake(conn)
	if err != nil {
		conn.Close()
		_ = cmd.Process.Kill()
		stop()
		return nil, nil, err
	}
	return n, func() { _ = n.Close(); stop() }, nil
}

// zeroWriter writes a stream at increasing offsets of w, skipping blocks
// that are all zero (the target reads as zeros there already: a new qcow2
// or a sparse file), so a mostly empty disk costs little.
type zeroWriter struct {
	w     io.WriterAt
	off   int64
	buf   []byte
	n     int
	check func(written int64) error // called every checkEvery bytes
	next  int64
}

const zeroBlock = 1 << 20
const checkEvery = 256 << 20

func newZeroWriter(w io.WriterAt) *zeroWriter {
	return &zeroWriter{w: w, buf: make([]byte, zeroBlock), next: checkEvery}
}

func allZero(b []byte) bool {
	for len(b) >= 8 {
		if binary.LittleEndian.Uint64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func (z *zeroWriter) flush() error {
	if z.n == 0 {
		return nil
	}
	if !allZero(z.buf[:z.n]) {
		if _, err := z.w.WriteAt(z.buf[:z.n], z.off); err != nil {
			return err
		}
	}
	z.off += int64(z.n)
	z.n = 0
	if z.check != nil && z.off >= z.next {
		z.next = z.off + checkEvery
		return z.check(z.off)
	}
	return nil
}

func (z *zeroWriter) Write(p []byte) (int, error) {
	done := 0
	for done < len(p) {
		c := copy(z.buf[z.n:], p[done:])
		z.n += c
		done += c
		if z.n == len(z.buf) {
			if err := z.flush(); err != nil {
				return done, err
			}
		}
	}
	return done, nil
}

// Close writes what is left and returns the total length.
func (z *zeroWriter) Close() (int64, error) {
	err := z.flush()
	return z.off, err
}

var errShortDisk = errors.New("the disk image in the backup is shorter than the disk")
