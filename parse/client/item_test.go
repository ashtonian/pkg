//go:build unit

package client

import (
	"errors"
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type sku string

type failingText struct{}

func (failingText) MarshalText() ([]byte, error) { return nil, errors.New("no text") }

func TestItemPath(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 500, time.FixedZone("", 2*60*60))
	tests := []struct {
		name string
		key  []any
		want string
		err  string
	}{
		{name: "one string", key: []any{"d1"}, want: "d1"},
		{name: "a segment per value, each escaped alone", key: []any{"w1", "a/b"}, want: "w1/a%2Fb"},
		{name: "reserved characters", key: []any{"a b?c#d%e"}, want: "a%20b%3Fc%23d%25e"},
		{name: "named string", key: []any{sku("x")}, want: "x"},
		{name: "integers in decimal", key: []any{int8(-5), uint64(math.MaxUint64)}, want: "-5/18446744073709551615"},
		{name: "float as its JSON number", key: []any{1.5, float32(0.1), 1e21}, want: "1.5/0.1/1e+21"},
		{name: "bool", key: []any{true}, want: "true"},
		{name: "quoted as its value", key: []any{Quote(int64(9007199254740993))}, want: "9007199254740993"},
		{name: "text marshaler", key: []any{netip.MustParseAddr("192.0.2.1"), at}, want: "192.0.2.1/2026-01-02T03:04:05.0000005+02:00"},
		{name: "no value", key: nil, err: "a key has a value per key column"},
		{name: "nil", key: []any{"a", nil}, err: "key value 1: client: a key value no path segment carries: nil has no key text"},
		{name: "struct", key: []any{struct{}{}}, err: "a struct value has no key text"},
		{name: "pointer", key: []any{new(string)}, err: "a ptr value has no key text"},
		{name: "not finite", key: []any{math.NaN()}, err: "unsupported value: NaN"},
		{name: "quoted infinity", key: []any{Quote(math.Inf(1))}, err: "unsupported value: +Inf"},
		{name: "empty", key: []any{""}, err: `key value 0: client: a key value no path segment carries: ""`},
		{name: "dot", key: []any{"w1", "."}, err: `key value 1: client: a key value no path segment carries: "."`},
		{name: "dot dot", key: []any{".."}, err: `carries: ".."`},
		{name: "text marshaler error", key: []any{failingText{}}, err: "no text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ItemPath(tt.key...)
			if tt.err != "" {
				checkErrorIs(t, err, ErrKeyValue)
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Errorf("got error %v, want one containing %q", err, tt.err)
				}
				return
			}
			must(t, err)
			check(t, tt.want, got)
		})
	}
}
