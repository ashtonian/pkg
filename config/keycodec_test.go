package config

import (
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

var countedKeyReads atomic.Int32 //nolint:gochecknoglobals // the decoder calls UnmarshalText on a zero value it allocates, so only package state reaches a counter

// countedKey counts the texts the decoder hands its UnmarshalText.
type countedKey int

func (k countedKey) MarshalText() ([]byte, error) { return []byte(fmt.Sprint(int(k))), nil }

func (k *countedKey) UnmarshalText(b []byte) error {
	countedKeyReads.Add(1)
	_, err := fmt.Sscan(string(b), (*int)(k))
	return err
}

// Text that is already its key's own reads back by being read once: a
// key's UnmarshalText, which may be costly, runs once per read, and twice
// only for text naming the key another way.
func TestKeyCodec_ReadsCanonicalTextOnce(t *testing.T) {
	c := keyCodecOf(reflect.TypeFor[countedKey]())
	for text, reads := range map[string]int32{"42": 1, "+42": 2} {
		countedKeyReads.Store(0)
		key, canonical, err := c.read(text)
		if assert.NoError(t, err, text) {
			assert.Equal(t, countedKey(42), key.Interface(), text)
			assert.Equal(t, "42", canonical, text)
		}
		assert.Equal(t, reads, countedKeyReads.Load(), text)
	}
}

// Goroutines reading through one codec each get the key their own text
// reads as, or its refusal, whatever the others read meanwhile.
func TestKeyCodec_ReadsConcurrently(t *testing.T) {
	type want struct {
		key       any // nil: the text is refused
		canonical string
	}
	addr := netip.MustParseAddr("10.0.0.1")
	for _, tc := range []struct {
		name  string
		typ   reflect.Type
		cases map[string]want
	}{
		{"int", reflect.TypeFor[int](), map[string]want{
			"8001": {8001, "8001"}, "0x1f41": {8001, "8001"}, "010": {8, "8"}, "": {0, "0"},
			"080": {}, "x": {},
		}},
		{"float64", reflect.TypeFor[float64](), map[string]want{
			"1.5": {1.5, "1.5"}, "15e-1": {1.5, "1.5"}, "inf": {math.Inf(1), "+Inf"},
			"NaN": {}, "nan": {}, "x": {},
		}},
		{"netip.Addr", reflect.TypeFor[netip.Addr](), map[string]want{
			"10.0.0.1": {addr, "10.0.0.1"}, "::ffff:10.0.0.1": {netip.AddrFrom16(addr.As16()), "::ffff:10.0.0.1"},
			"bogus": {},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := keyCodecOf(tc.typ)
			texts := make([]string, 0, len(tc.cases))
			for text := range tc.cases {
				texts = append(texts, text)
			}
			var wg sync.WaitGroup
			for g := range 8 {
				wg.Go(func() {
					for i := range 200 {
						text := texts[(g+i)%len(texts)]
						w := tc.cases[text]
						key, canonical, err := c.read(text)
						if w.key == nil {
							assert.Error(t, err, text)
						} else if assert.NoError(t, err, text) {
							assert.Equal(t, w.key, key.Interface(), text)
							assert.Equal(t, w.canonical, canonical, text)
						}
					}
				})
			}
			wg.Wait()
		})
	}
}
