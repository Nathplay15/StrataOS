package nvr

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Fragmented MP4, as ffmpeg writes it for the live stream: an init segment
// (ftyp + moov) then fragments (moof + mdat). A viewer joining a running
// stream needs the init segment and must start on a fragment that begins
// with a key frame of the video track, which only the sample flags tell.

// maxBox bounds a box read from ffmpeg (a 4K key frame stays far below).
const maxBox = 64 << 20

// Fragment is one moof + mdat pair (with any box before it, such as styp).
type Fragment struct {
	Data []byte
	Key  bool // the video track starts with a sync sample
}

// fmp4Reader splits an fMP4 byte stream into its init segment and fragments.
type fmp4Reader struct {
	r     *bufio.Reader
	init  []byte
	video uint32 // track ID of the video track (0: unknown, any track counts)
	trex  map[uint32]uint32
	pend  []byte // boxes waiting for the next mdat
	key   bool
}

func newFMP4Reader(r io.Reader) *fmp4Reader {
	return &fmp4Reader{r: bufio.NewReaderSize(r, 256<<10), trex: map[uint32]uint32{}}
}

// readBox returns a whole box (header included) and its type.
func readBox(r io.Reader) ([]byte, string, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, "", err
	}
	size := uint64(binary.BigEndian.Uint32(h[:4]))
	typ := string(h[4:8])
	hdr := h[:]
	if size == 1 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, "", err
		}
		size = binary.BigEndian.Uint64(ext[:])
		hdr = append(hdr[:8:8], ext[:]...)
	}
	if size < uint64(len(hdr)) || size > maxBox {
		return nil, "", fmt.Errorf("invalid %q box size %d", typ, size)
	}
	b := make([]byte, size)
	copy(b, hdr)
	if _, err := io.ReadFull(r, b[len(hdr):]); err != nil {
		return nil, "", err
	}
	return b, typ, nil
}

// children calls f for each box inside a container's payload.
func children(p []byte, f func(typ string, payload []byte)) {
	for len(p) >= 8 {
		size := int(binary.BigEndian.Uint32(p[:4]))
		typ := string(p[4:8])
		hdr := 8
		if size == 1 {
			if len(p) < 16 {
				return
			}
			size = int(binary.BigEndian.Uint64(p[8:16]))
			hdr = 16
		}
		if size < hdr || size > len(p) {
			return
		}
		f(typ, p[hdr:size])
		p = p[size:]
	}
}

func payload(box []byte) []byte {
	if binary.BigEndian.Uint32(box[:4]) == 1 {
		return box[16:]
	}
	return box[8:]
}

// parseMoov finds the video track and the default sample flags of each track.
func (m *fmp4Reader) parseMoov(p []byte) {
	children(p, func(typ string, b []byte) {
		switch typ {
		case "trak":
			var id uint32
			var video bool
			children(b, func(typ string, b []byte) {
				switch typ {
				case "tkhd":
					if len(b) >= 24 && b[0] == 1 {
						id = binary.BigEndian.Uint32(b[20:24])
					} else if len(b) >= 16 {
						id = binary.BigEndian.Uint32(b[12:16])
					}
				case "mdia":
					children(b, func(typ string, b []byte) {
						if typ == "hdlr" && len(b) >= 12 && string(b[8:12]) == "vide" {
							video = true
						}
					})
				}
			})
			if video && m.video == 0 {
				m.video = id
			}
		case "mvex":
			children(b, func(typ string, b []byte) {
				if typ == "trex" && len(b) >= 24 {
					m.trex[binary.BigEndian.Uint32(b[4:8])] = binary.BigEndian.Uint32(b[20:24])
				}
			})
		}
	})
}

// sampleIsSync: sample_is_non_sync_sample (bit 16) is clear.
func sampleIsSync(flags uint32) bool { return flags&0x00010000 == 0 }

// moofKey tells whether the video track of a fragment starts with a sync sample.
func (m *fmp4Reader) moofKey(p []byte) bool {
	key, seen := false, false
	children(p, func(typ string, b []byte) {
		if typ != "traf" || seen {
			return
		}
		var track, defFlags uint32
		hasDef := false
		var first *uint32
		children(b, func(typ string, b []byte) {
			switch typ {
			case "tfhd":
				if len(b) < 8 {
					return
				}
				fl := binary.BigEndian.Uint32(b[:4]) & 0xffffff
				track = binary.BigEndian.Uint32(b[4:8])
				off := 8
				for _, f := range []struct {
					bit uint32
					n   int
				}{{0x1, 8}, {0x2, 4}, {0x8, 4}, {0x10, 4}} {
					if fl&f.bit != 0 {
						off += f.n
					}
				}
				if fl&0x20 != 0 && len(b) >= off+4 {
					defFlags, hasDef = binary.BigEndian.Uint32(b[off:off+4]), true
				}
			case "trun":
				if len(b) < 8 || first != nil {
					return
				}
				fl := binary.BigEndian.Uint32(b[:4]) & 0xffffff
				if binary.BigEndian.Uint32(b[4:8]) == 0 {
					return
				}
				off := 8
				if fl&0x1 != 0 {
					off += 4
				}
				if fl&0x4 != 0 {
					if len(b) >= off+4 {
						v := binary.BigEndian.Uint32(b[off : off+4])
						first = &v
					}
					return
				}
				if fl&0x400 != 0 {
					if fl&0x100 != 0 {
						off += 4
					}
					if fl&0x200 != 0 {
						off += 4
					}
					if len(b) >= off+4 {
						v := binary.BigEndian.Uint32(b[off : off+4])
						first = &v
					}
				}
			}
		})
		if m.video != 0 && track != m.video {
			return
		}
		seen = true
		switch {
		case first != nil:
			key = sampleIsSync(*first)
		case hasDef:
			key = sampleIsSync(defFlags)
		default:
			key = sampleIsSync(m.trex[track])
		}
	})
	return key
}

var errNoInit = errors.New("the stream has no MP4 header")

// Init reads the init segment (everything up to the first moof).
func (m *fmp4Reader) Init() ([]byte, error) {
	for {
		b, typ, err := readBox(m.r)
		if err != nil {
			return nil, err
		}
		switch typ {
		case "moof":
			if m.init == nil {
				return nil, errNoInit
			}
			m.pend, m.key = b, m.moofKey(payload(b))
			return m.init, nil
		case "moov":
			m.parseMoov(payload(b))
		}
		m.init = append(m.init, b...)
	}
}

// Next reads the next fragment (call Init first).
func (m *fmp4Reader) Next() (Fragment, error) {
	for {
		b, typ, err := readBox(m.r)
		if err != nil {
			return Fragment{}, err
		}
		switch typ {
		case "moof":
			m.key = m.moofKey(payload(b))
			m.pend = append(m.pend, b...)
		case "mdat":
			f := Fragment{Data: append(m.pend, b...), Key: m.key}
			m.pend, m.key = nil, false
			return f, nil
		default: // styp, sidx, prft…
			m.pend = append(m.pend, b...)
		}
	}
}
