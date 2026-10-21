// Package cast controls media receivers of the local network: Chromecast
// devices (Cast v2 protocol) and DLNA/UPnP renderers (AVTransport).
package cast

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Message is the CastMessage protobuf of the Cast v2 protocol (cast_channel.proto),
// limited to text payloads: the namespaces used here all carry JSON.
//
//	1 protocol_version (enum, CASTV2_1_0 = 0)
//	2 source_id, 3 destination_id, 4 namespace (string)
//	5 payload_type (enum, STRING = 0, BINARY = 1)
//	6 payload_utf8 (string), 7 payload_binary (bytes)
type Message struct {
	Source      string
	Destination string
	Namespace   string
	Payload     string
	Binary      []byte // set when payload_type is BINARY
}

// maxFrame bounds a message read from a receiver (real ones stay far below).
const maxFrame = 1 << 20

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendString(b []byte, field int, s string) []byte {
	b = appendVarint(b, uint64(field<<3|2))
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// Marshal encodes the message as protobuf (without the length prefix).
func (m *Message) Marshal() []byte {
	b := make([]byte, 0, 32+len(m.Source)+len(m.Destination)+len(m.Namespace)+len(m.Payload)+len(m.Binary))
	b = append(b, 1<<3|0, 0) // protocol_version = CASTV2_1_0 (required, so always written)
	b = appendString(b, 2, m.Source)
	b = appendString(b, 3, m.Destination)
	b = appendString(b, 4, m.Namespace)
	if m.Binary != nil {
		b = append(b, 5<<3|0, 1)
		b = appendString(b, 7, string(m.Binary))
	} else {
		b = append(b, 5<<3|0, 0)
		b = appendString(b, 6, m.Payload)
	}
	return b
}

var errTruncated = errors.New("cast: truncated message")

func readVarint(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1, nil
		}
	}
	return 0, 0, errTruncated
}

// Unmarshal decodes a protobuf CastMessage, skipping unknown fields.
func Unmarshal(b []byte) (*Message, error) {
	m := &Message{}
	binaryType := false
	for len(b) > 0 {
		key, n, err := readVarint(b)
		if err != nil {
			return nil, err
		}
		b = b[n:]
		field, wire := int(key>>3), key&7
		switch wire {
		case 0:
			v, n, err := readVarint(b)
			if err != nil {
				return nil, err
			}
			b = b[n:]
			if field == 5 {
				binaryType = v == 1
			}
		case 1:
			if len(b) < 8 {
				return nil, errTruncated
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return nil, errTruncated
			}
			b = b[4:]
		case 2:
			l, n, err := readVarint(b)
			if err != nil {
				return nil, err
			}
			b = b[n:]
			if l > uint64(len(b)) {
				return nil, errTruncated
			}
			v := b[:l]
			b = b[l:]
			switch field {
			case 2:
				m.Source = string(v)
			case 3:
				m.Destination = string(v)
			case 4:
				m.Namespace = string(v)
			case 6:
				m.Payload = string(v)
			case 7:
				m.Binary = append([]byte{}, v...)
			}
		default:
			return nil, fmt.Errorf("cast: unsupported wire type %d", wire)
		}
	}
	if binaryType && m.Binary == nil {
		m.Binary = []byte{}
	}
	return m, nil
}

// WriteMessage sends a message with its 4-byte big-endian length prefix.
func WriteMessage(w io.Writer, m *Message) error {
	body := m.Marshal()
	frame := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	_, err := w.Write(append(frame, body...))
	return err
}

// ReadMessage reads one length-prefixed message.
func ReadMessage(r io.Reader) (*Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return nil, fmt.Errorf("cast: message too large (%d bytes)", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return Unmarshal(body)
}
