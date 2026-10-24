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

// container checks that n children, each at least minBytes long, can fit,
// then walks them.
func (w *boundsWalker) container(n uint64, minBytes uint64, children int, depth int) error {
	if depth >= maxEnvelopeDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxEnvelopeDepth)
	}
	if n > uint64(w.remaining())/minBytes {
		return errTruncated
	}
	for range n * uint64(children) {
		if err := w.value(depth + 1); err != nil {
			return err
		}
	}
	return nil
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
		return w.container(uint64(c&0x0f), 2, 2, depth)
	case c <= 0x9f: // fixarray
		return w.container(uint64(c&0x0f), 1, 1, depth)
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
		return w.container(n, 1, 1, depth)
	case 0xdd: // array32
		n, err := w.uint(4)
		if err != nil {
			return err
		}
		return w.container(n, 1, 1, depth)
	case 0xde: // map16
		n, err := w.uint(2)
		if err != nil {
			return err
		}
		return w.container(n, 2, 2, depth)
	case 0xdf: // map32
		n, err := w.uint(4)
		if err != nil {
			return err
		}
		return w.container(n, 2, 2, depth)
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
