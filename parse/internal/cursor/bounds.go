package cursor

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// maxEnvelopeDepth bounds container nesting in a decoded cursor. Envelopes
// nest a handful of levels; anything deeper is malformed.
const maxEnvelopeDepth = 32

// checkMsgpackBounds walks one MessagePack value and verifies, before any
// decoder sees it, that every declared length fits in the bytes that remain:
// an array of n elements needs at least n bytes, a map of n entries 2n, a
// string, binary or extension payload its full length. msgpack allocates a
// container from its declared length before reading the elements, so a few
// header bytes could otherwise request millions of elements; after this check
// every allocation is bounded by the input size. Exactly one value must fill
// data.
//
// It also refuses a map that names a key twice, at any depth. msgpack decodes
// a repeated member into the value the first occurrence left behind, and when
// that value is a non-nil interface the decoder panics.
func checkMsgpackBounds(data []byte) error {
	w := boundsWalker{data: data}
	if err := w.value(0); err != nil {
		return err
	}
	if w.pos != len(data) {
		return fmt.Errorf("%d trailing bytes after the value", len(data)-w.pos)
	}
	return nil
}

type boundsWalker struct {
	data []byte
	pos  int
}

var errTruncated = errors.New("declared length exceeds the remaining input")

func (w *boundsWalker) remaining() int { return len(w.data) - w.pos }

// skip consumes n bytes, failing if fewer remain.
func (w *boundsWalker) skip(n uint64) error {
	if n > uint64(w.remaining()) {
		return errTruncated
	}
	w.pos += int(n)
	return nil
}

// uint reads a big-endian unsigned length of size bytes.
func (w *boundsWalker) uint(size int) (uint64, error) {
	if w.remaining() < size {
		return 0, errTruncated
	}
	b := w.data[w.pos : w.pos+size]
	w.pos += size
	switch size {
	case 1:
		return uint64(b[0]), nil
	case 2:
		return uint64(binary.BigEndian.Uint16(b)), nil
	default:
		return uint64(binary.BigEndian.Uint32(b)), nil
	}
}

// array checks that n elements, each at least one byte, can fit, then walks
// them.
func (w *boundsWalker) array(n uint64, depth int) error {
	if err := w.fits(n, 1, depth); err != nil {
		return err
	}
	for range n {
		if err := w.value(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

// mapEntries checks that n entries, each at least two bytes, can fit, then
// walks them, refusing a key an earlier entry of the same map named.
func (w *boundsWalker) mapEntries(n uint64, depth int) error {
	if err := w.fits(n, 2, depth); err != nil {
		return err
	}
	seen := make(map[string]struct{}, n)
	for range n {
		start := w.pos
		if err := w.value(depth + 1); err != nil {
			return err
		}
		key := keyIdentity(w.data[start:w.pos])
		if _, dup := seen[key]; dup {
			return fmt.Errorf("a map names the key %q twice", key[1:])
		}
		seen[key] = struct{}{}
		if err := w.value(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

func (w *boundsWalker) fits(n uint64, minBytes uint64, depth int) error {
	if depth >= maxEnvelopeDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxEnvelopeDepth)
	}
	if n > uint64(w.remaining())/minBytes {
		return errTruncated
	}
	return nil
}

// keyIdentity names the member an encoded map key selects. The decoder reads
// a struct member's name from any string or binary encoding, and nil as the
// empty name, so those compare by content: fixstr "v", str8 "v" and bin8 "v"
// are one member. Any other key compares by its encoded bytes.
func keyIdentity(enc []byte) string {
	c := enc[0]
	switch {
	case c == 0xc0:
		return "t"
	case c >= 0xa0 && c <= 0xbf:
		return "t" + string(enc[1:])
	}
	switch c {
	case 0xc4, 0xd9:
		return "t" + string(enc[2:])
	case 0xc5, 0xda:
		return "t" + string(enc[3:])
	case 0xc6, 0xdb:
		return "t" + string(enc[5:])
	}
	return "r" + string(enc)
}

// isMap reports whether c opens a MessagePack map.
func isMap(c byte) bool {
	return (c >= 0x80 && c <= 0x8f) || c == 0xde || c == 0xdf
}

func (w *boundsWalker) value(depth int) error {
	if w.remaining() == 0 {
		return errTruncated
	}
	c := w.data[w.pos]
	w.pos++
	switch {
	case c <= 0x7f, c >= 0xe0: // positive / negative fixint
		return nil
	case c <= 0x8f: // fixmap
		return w.mapEntries(uint64(c&0x0f), depth)
	case c <= 0x9f: // fixarray
		return w.array(uint64(c&0x0f), depth)
	case c <= 0xbf: // fixstr
		return w.skip(uint64(c & 0x1f))
	}
	switch c {
	case 0xc0, 0xc2, 0xc3: // nil, false, true
		return nil
	case 0xc4, 0xd9: // bin8, str8
		return w.sized(1, 0)
	case 0xc5, 0xda: // bin16, str16
		return w.sized(2, 0)
	case 0xc6, 0xdb: // bin32, str32
		return w.sized(4, 0)
	case 0xc7: // ext8: length, type, payload
		return w.sized(1, 1)
	case 0xc8: // ext16
		return w.sized(2, 1)
	case 0xc9: // ext32
		return w.sized(4, 1)
	case 0xca: // float32
		return w.skip(4)
	case 0xcb: // float64
		return w.skip(8)
	case 0xcc, 0xd0: // uint8, int8
		return w.skip(1)
	case 0xcd, 0xd1: // uint16, int16
		return w.skip(2)
	case 0xce, 0xd2: // uint32, int32
		return w.skip(4)
	case 0xcf, 0xd3: // uint64, int64
		return w.skip(8)
	case 0xd4: // fixext1: type + 1
		return w.skip(2)
	case 0xd5: // fixext2
		return w.skip(3)
	case 0xd6: // fixext4
		return w.skip(5)
	case 0xd7: // fixext8
		return w.skip(9)
	case 0xd8: // fixext16
		return w.skip(17)
	case 0xdc: // array16
		n, err := w.uint(2)
		if err != nil {
			return err
		}
		return w.array(n, depth)
	case 0xdd: // array32
		n, err := w.uint(4)
		if err != nil {
			return err
		}
		return w.array(n, depth)
	case 0xde: // map16
		n, err := w.uint(2)
		if err != nil {
			return err
		}
		return w.mapEntries(n, depth)
	case 0xdf: // map32
		n, err := w.uint(4)
		if err != nil {
			return err
		}
		return w.mapEntries(n, depth)
	}
	return fmt.Errorf("invalid MessagePack type byte 0x%02x", c)
}

// sized reads a lenSize-byte payload length, skips extra header bytes (an
// extension's type), then the payload.
func (w *boundsWalker) sized(lenSize int, extra uint64) error {
	n, err := w.uint(lenSize)
	if err != nil {
		return err
	}
	if err := w.skip(extra); err != nil {
		return err
	}
	return w.skip(n)
}
