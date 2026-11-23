//go:build unit || integration

package parse_test

import (
	"encoding/json"
	"maps"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// keyedMaps holds maps whose keys encoding/json writes as member names by a
// rule other than the string itself: base-10 integers, and a struct type's
// text methods.
type keyedMaps struct {
	ID     int64              `json:"id"      db:"id"`
	ByInt  map[int]int        `json:"by_int"  db:"by_int"  parse:"patch"`
	ByUint map[uint8]string   `json:"by_uint" db:"by_uint" parse:"patch"`
	ByAddr map[netip.Addr]int `json:"by_addr" db:"by_addr" parse:"patch"`
}

func keyedMapsRow() keyedMaps {
	return keyedMaps{
		ID:     1,
		ByInt:  map[int]int{1: 10, 5: 50},
		ByUint: map[uint8]string{7: "seven"},
		ByAddr: map[netip.Addr]int{netip.MustParseAddr("::1"): 1, netip.MustParseAddr("192.0.2.1"): 2},
	}
}

// keyedMapMerges are merge patches that name keys as encoding/json reads
// them, in spellings it writes and spellings it only reads ("01", the long
// form of ::1).
var keyedMapMerges = []struct{ name, body string }{
	{"integer keys added and replaced", `{"by_int":{"2":20,"-3":-30,"5":55}}`},
	{"a non-canonical integer names its key", `{"by_int":{"01":11}}`},
	{"null deletes an integer key", `{"by_int":{"1":null,"9":90}}`},
	{"unsigned keys up to the type's range", `{"by_uint":{"7":"SEVEN","255":"max","0":"zero"}}`},
	{"text-method keys", `{"by_addr":{"192.0.2.9":9,"192.0.2.1":null}}`},
	{"a text-method key in another spelling names its key", `{"by_addr":{"0:0:0:0:0:0:0:1":100}}`},
	{"every map at once", `{"by_int":{"5":null},"by_uint":{"8":"eight"},"by_addr":{"::1":null}}`},
}

// keyedMapRefusals name a member no key of the map's type decodes from, or
// two members that decode to one key.
var keyedMapRefusals = []struct{ name, body string }{
	{"not an integer", `{"by_int":{"x":1}}`},
	{"an integer in exponent form", `{"by_int":{"1e2":1}}`},
	{"past the unsigned range", `{"by_uint":{"256":"x"}}`},
	{"a negative unsigned key", `{"by_uint":{"-1":"x"}}`},
	{"not an address", `{"by_addr":{"host.example":1}}`},
	{"two spellings of one integer key", `{"by_int":{"01":1,"1":2}}`},
	{"two spellings of one address", `{"by_addr":{"::1":1,"0:0:0:0:0:0:0:1":2}}`},
}

// mergedByEncodingJSON is what a merge patch body makes of row by
// encoding/json's rules: each member's name decodes to a key as Unmarshal
// decodes it into the map, a null member deletes that key (RFC 7396), and
// any other member sets the key to the value Unmarshal decodes.
func mergedByEncodingJSON(t *testing.T, row keyedMaps, body string) keyedMaps {
	t.Helper()
	var members struct {
		ByInt  map[int]json.RawMessage        `json:"by_int"`
		ByUint map[uint8]json.RawMessage      `json:"by_uint"`
		ByAddr map[netip.Addr]json.RawMessage `json:"by_addr"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &members))
	row.ByInt = mergeMembers(t, row.ByInt, members.ByInt)
	row.ByUint = mergeMembers(t, row.ByUint, members.ByUint)
	row.ByAddr = mergeMembers(t, row.ByAddr, members.ByAddr)
	return row
}

func mergeMembers[K comparable, V any](t *testing.T, existing map[K]V, members map[K]json.RawMessage) map[K]V {
	t.Helper()
	out := maps.Clone(existing)
	for key, raw := range members {
		if string(raw) == "null" {
			delete(out, key)
			continue
		}
		var v V
		require.NoError(t, json.Unmarshal(raw, &v))
		out[key] = v
	}
	return out
}
