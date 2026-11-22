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
	"regexp"
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

	nbdOptGo              = 7
	nbdOptStructuredReply = 8
	nbdOptSetMetaContext  = 10
	nbdRepAck             = 1
	nbdRepInfo            = 3
	nbdRepMetaContext     = 4
	nbdInfoExport         = 0

	nbdCmdRead        = 0
	nbdCmdWrite       = 1
	nbdCmdDisc        = 2
	nbdCmdFlush       = 3
	nbdCmdBlockStatus = 7

	nbdStructuredMagic  = 0x668e33ef
	nbdReplyFlagDone    = 1
	nbdReplyBlockStatus = 5
	nbdReplyErrorFlag   = 1 << 15

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
	// structured: structured replies were negotiated (block status only);
	// metaID is the id of the meta context.
	structured bool
	metaID     uint32
}

// nbdHandshake negotiates the default export ("") of a fixed newstyle server.
func nbdHandshake(c io.ReadWriteCloser) (*nbdConn, error) { return nbdHandshakeExport(c, "", "") }

// nbdHandshakeExport negotiates an export by name. With a meta context
// (qemu:dirty-bitmap:<name>), it also turns structured replies on so that
// blockStatus can read it; such a connection is used for that only (reads
// would come back as structured chunks too).
func nbdHandshakeExport(c io.ReadWriteCloser, export, meta string) (*nbdConn, error) {
	if len(export) > 4096 || len(meta) > 4096 {
		return nil, fmt.Errorf("nbd: name too long")
	}
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
	if err := binary.Write(c, binary.BigEndian, cflags); err != nil {
		return nil, err
	}
	n := &nbdConn{c: c, size: -1}
	if meta != "" {
		if err := nbdOption(c, nbdOptStructuredReply, nil); err != nil {
			return nil, err
		}
		for {
			typ, _, err := nbdOptReply(c, nbdOptStructuredReply)
			if err != nil {
				return nil, err
			}
			if typ == nbdRepAck {
				break
			}
		}
		var q bytes.Buffer
		_ = binary.Write(&q, binary.BigEndian, uint32(len(export)))
		q.WriteString(export)
		_ = binary.Write(&q, binary.BigEndian, uint32(1))
		_ = binary.Write(&q, binary.BigEndian, uint32(len(meta)))
		q.WriteString(meta)
		if err := nbdOption(c, nbdOptSetMetaContext, q.Bytes()); err != nil {
			return nil, err
		}
		for {
			typ, data, err := nbdOptReply(c, nbdOptSetMetaContext)
			if err != nil {
				return nil, err
			}
			if typ == nbdRepMetaContext && len(data) >= 4 && string(data[4:]) == meta {
				n.metaID, n.structured = binary.BigEndian.Uint32(data), true
			}
			if typ == nbdRepAck {
				break
			}
		}
		if !n.structured {
			return nil, fmt.Errorf("nbd: the server does not export %s", meta)
		}
	}
	// NBD_OPT_GO: the export name and no information requests.
	var opt bytes.Buffer
	_ = binary.Write(&opt, binary.BigEndian, uint32(len(export)))
	opt.WriteString(export)
	_ = binary.Write(&opt, binary.BigEndian, uint16(0))
	if err := nbdOption(c, nbdOptGo, opt.Bytes()); err != nil {
		return nil, err
	}
	for {
		typ, data, err := nbdOptReply(c, nbdOptGo)
		if err != nil {
			return nil, err
		}
		switch typ {
		case nbdRepInfo:
			if len(data) >= 12 && binary.BigEndian.Uint16(data) == nbdInfoExport {
				n.size = int64(binary.BigEndian.Uint64(data[2:]))
				n.readOnly = binary.BigEndian.Uint16(data[10:])&nbdTransReadOnly != 0
			}
		case nbdRepAck:
			if n.size < 0 {
				return nil, fmt.Errorf("nbd: the server did not give the export size")
			}
			return n, nil
		}
	}
}

// nbdOption sends an option of the handshake.
func nbdOption(c io.Writer, opt uint32, data []byte) error {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint64(nbdOptMagic))
	_ = binary.Write(&b, binary.BigEndian, opt)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.Write(data)
	_, err := c.Write(b.Bytes())
	return err
}

// nbdOptReply reads one option reply; an error reply becomes an error.
func nbdOptReply(c io.Reader, opt uint32) (uint32, []byte, error) {
	var rep struct {
		Magic    uint64
		Opt, Typ uint32
		Length   uint32
	}
	if err := binary.Read(c, binary.BigEndian, &rep); err != nil {
		return 0, nil, fmt.Errorf("nbd: option reply: %w", err)
	}
	if rep.Magic != nbdRepMagic || rep.Length > 1<<20 || rep.Opt != opt {
		return 0, nil, fmt.Errorf("nbd: bad option reply")
	}
	data := make([]byte, rep.Length)
	if _, err := io.ReadFull(c, data); err != nil {
		return 0, nil, err
	}
	if rep.Typ&(1<<31) != 0 {
		return 0, nil, fmt.Errorf("nbd: option %d refused (%d): %s", opt, rep.Typ&^(1<<31), strings.TrimSpace(string(data)))
	}
	return rep.Typ, data, nil
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

// extent is a byte range of a disk.
type extent struct{ Off, Len int64 }

// blockStatus asks the meta context of a structured connection about
// [off, off+length): the server describes a prefix of it (covered bytes),
// and the extents returned are those flagged (dirty, for a bitmap).
func (n *nbdConn) blockStatus(off int64, length uint32) (flagged []extent, covered int64, err error) {
	if !n.structured {
		return nil, 0, fmt.Errorf("nbd: no meta context negotiated")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handle++
	h := make([]byte, 28)
	binary.BigEndian.PutUint32(h[0:], nbdReqMagic)
	binary.BigEndian.PutUint16(h[6:], nbdCmdBlockStatus)
	binary.BigEndian.PutUint64(h[8:], n.handle)
	binary.BigEndian.PutUint64(h[16:], uint64(off))
	binary.BigEndian.PutUint32(h[24:], length)
	if _, err := n.c.Write(h); err != nil {
		return nil, 0, err
	}
	pos := off
	for {
		var ch [20]byte
		if _, err := io.ReadFull(n.c, ch[:]); err != nil {
			return nil, 0, err
		}
		be := binary.BigEndian
		if be.Uint32(ch[0:]) != nbdStructuredMagic || be.Uint64(ch[8:]) != n.handle {
			return nil, 0, fmt.Errorf("nbd: unexpected reply")
		}
		flags, typ, l := be.Uint16(ch[4:]), be.Uint16(ch[6:]), be.Uint32(ch[16:])
		if l > 16<<20 {
			return nil, 0, fmt.Errorf("nbd: reply too large")
		}
		data := make([]byte, l)
		if _, err := io.ReadFull(n.c, data); err != nil {
			return nil, 0, err
		}
		switch {
		case typ&nbdReplyErrorFlag != 0:
			code := uint32(0)
			if len(data) >= 4 {
				code = be.Uint32(data)
			}
			err = fmt.Errorf("nbd: block status failed (error %d)", code)
		case typ == nbdReplyBlockStatus:
			if len(data) < 4 || (len(data)-4)%8 != 0 {
				return nil, 0, fmt.Errorf("nbd: bad block status reply")
			}
			if be.Uint32(data) != n.metaID {
				break
			}
			for d := data[4:]; len(d) >= 8; d = d[8:] {
				dl, st := int64(be.Uint32(d)), be.Uint32(d[4:])
				if dl == 0 {
					return nil, 0, fmt.Errorf("nbd: empty block status descriptor")
				}
				if st&1 != 0 {
					if k := len(flagged); k > 0 && flagged[k-1].Off+flagged[k-1].Len == pos {
						flagged[k-1].Len += dl
					} else {
						flagged = append(flagged, extent{pos, dl})
					}
				}
				pos += dl
			}
		}
		if flags&nbdReplyFlagDone != 0 {
			if err == nil && pos == off {
				err = fmt.Errorf("nbd: no block status given")
			}
			return flagged, pos - off, err
		}
	}
}

// dirtyExtents lists the flagged extents of the whole export, in order.
func (n *nbdConn) dirtyExtents() ([]extent, error) {
	var out []extent
	for off := int64(0); off < n.size; {
		l := min(n.size-off, 1<<30)
		ext, covered, err := n.blockStatus(off, uint32(l))
		if err != nil {
			return nil, err
		}
		for _, e := range ext {
			e.Len = min(e.Len, n.size-e.Off)
			if k := len(out); k > 0 && out[k-1].Off+out[k-1].Len == e.Off {
				out[k-1].Len += e.Len
			} else if e.Len > 0 {
				out = append(out, e)
			}
		}
		off += covered
	}
	return out, nil
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
	n, _, done, err := openImageBitmap(ctx, image, format, write, "")
	return n, done, err
}

// openImageBitmap is openImage that also exports a persistent dirty bitmap
// of a qcow2 image: the second connection reads its extents (nil without
// bitmap).
func openImageBitmap(ctx context.Context, image, format string, write bool, bitmap string) (*nbdConn, *nbdConn, func(), error) {
	if bitmap != "" && (format != "qcow2" || !bitmapNameRe.MatchString(bitmap)) {
		return nil, nil, nil, fmt.Errorf("invalid bitmap %q", bitmap)
	}
	if format != "raw" && format != "qcow2" {
		return nil, nil, nil, fmt.Errorf("unsupported disk format %q", format)
	}
	if !filepath.IsAbs(image) || strings.ContainsAny(image, ",\n\r") {
		return nil, nil, nil, fmt.Errorf("unsupported disk path %s", image)
	}
	fi, err := os.Stat(image)
	if err != nil {
		return nil, nil, nil, err
	}
	driver := "file"
	if fi.Mode()&os.ModeDevice != 0 {
		driver = "host_device"
	} else if !fi.Mode().IsRegular() {
		return nil, nil, nil, fmt.Errorf("%s is not a disk image", image)
	}
	if err := os.MkdirAll(NBDRun, 0o700); err != nil {
		return nil, nil, nil, err
	}
	dir, err := os.MkdirTemp(NBDRun, "s-")
	if err != nil {
		return nil, nil, nil, err
	}
	sock := filepath.Join(dir, "sock")
	// Locking off: the disk may be in use by the paused VM, or be the frozen
	// base of a running VM's temporary snapshot.
	opts := fmt.Sprintf("driver=%s,file.driver=%s,file.filename=%s,file.locking=off", format, driver, image)
	args := []string{"--socket=" + sock, "--image-opts", "--cache=none", "--aio=threads"}
	if !write {
		args = append(args, "--read-only")
	}
	if bitmap != "" {
		args = append(args, "--shared=2", "--bitmap="+bitmap)
	}
	args = append(args, opts)
	cmd := exec.CommandContext(ctx, "qemu-nbd", args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, nil, err
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
			return nil, nil, nil, fmt.Errorf("qemu-nbd: %v %s", err, strings.TrimSpace(errb.String()))
		default:
		}
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
		if i > 300 {
			_ = cmd.Process.Kill()
			stop()
			return nil, nil, nil, fmt.Errorf("qemu-nbd did not start: %s", strings.TrimSpace(errb.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	n, err := nbdHandshake(conn)
	if err != nil {
		conn.Close()
		_ = cmd.Process.Kill()
		stop()
		return nil, nil, nil, err
	}
	if bitmap == "" {
		return n, nil, func() { _ = n.Close(); stop() }, nil
	}
	var bm *nbdConn
	if c2, err := net.Dial("unix", sock); err == nil {
		if bm, err = nbdHandshakeExport(c2, "", "qemu:dirty-bitmap:"+bitmap); err != nil {
			c2.Close()
		}
	}
	if bm == nil {
		_ = n.Close()
		_ = cmd.Process.Kill()
		stop()
		return nil, nil, nil, fmt.Errorf("nbd: cannot read the bitmap %s", bitmap)
	}
	return n, bm, func() { _ = bm.Close(); _ = n.Close(); stop() }, nil
}

var bitmapNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// dialNBD connects to an export of an NBD server on a unix socket (the
// pull-mode backup export of libvirt), with a meta context or not.
func dialNBD(sock, export, meta string) (*nbdConn, error) {
	c, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return nil, err
	}
	n, err := nbdHandshakeExport(c, export, meta)
	if err != nil {
		c.Close()
		return nil, err
	}
	return n, nil
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
