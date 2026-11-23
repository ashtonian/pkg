//go:build unit

package jsonkeys

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedKey string

type keyInner struct {
	Y int `json:"y"`
}

// KeyEmbedded is exported: encoding/json cannot allocate an embedded
// pointer to an unexported struct.
type KeyEmbedded struct {
	E string `json:"e"`
}

// ownJSON decodes itself, so whatever it holds is its own business.
type ownJSON struct{ M map[string]int }

func (o *ownJSON) UnmarshalJSON([]byte) error { return nil }

type keyDoc struct {
	X      int                `json:"x"`
	Items  []keyInner         `json:"items"`
	Pair   [1]keyInner        `json:"pair"`
	Ints   map[int]int        `json:"ints"`
	Uints  map[uint8]keyInner `json:"uints"`
	Names  map[string]*keyInner
	Addrs  map[netip.Addr]int `json:"addrs"`
	Raw    json.RawMessage    `json:"raw"`
	Own    ownJSON            `json:"own"`
	Any    any                `json:"any"`
	Nested *keyDoc            `json:"nested"`
	*KeyEmbedded
}

// keyCasesUpper and keyCasesLower name two fields by one name in two cases,
// which encoding/json writes and reads as two members.
type keyCasesUpper struct {
	Upper int `json:"K"`
	Lower int `json:"k"`
}

type keyCasesLower struct {
	Lower int `json:"k"`
	Upper int `json:"K"`
}

type keyPromotedInner struct {
	Alpha int `json:"alpha"`
}

// keyPromoted promotes alpha, which folds like its own ALPHA declared after
// it: decoding matches by case breadth first, so ALPHA takes the fold.
type keyPromoted struct {
	keyPromotedInner
	Upper int `json:"ALPHA"`
}

type keyStrict struct {
	Name int `json:"name,case:strict"`
}

// decodeAndValidate decodes doc as a stored document is read, then
// validates its member names against the decoded value.
func decodeAndValidate(t *testing.T, doc string, target reflect.Type) error {
	t.Helper()
	fresh := reflect.New(target)
	require.NoError(t, json.Unmarshal([]byte(doc), fresh.Interface()))
	return Validate([]byte(doc), fresh.Interface())
}

func TestValidate(t *testing.T) {
	docType := reflect.TypeFor[keyDoc]()
	tests := []struct {
		name string
		doc  string
		typ  reflect.Type
		err  string // empty: valid
	}{
		{name: "canonical members", doc: `{"x":1,"items":[{"y":1}],"ints":{"-3":1,"10":2},"Names":{"a":{"y":2}},"e":"v"}`},
		{name: "null", doc: `null`},
		{name: "empty object", doc: `{}`},
		{name: "member spelled in another case", doc: `{"X":1}`, err: `JSON member "/X" is noncanonical for jsonkeys.keyDoc; use "x"`},
		{name: "field named by its Go name in another case", doc: `{"names":{}}`, err: `JSON member "/names" is noncanonical for jsonkeys.keyDoc; use "Names"`},
		{name: "duplicate member", doc: `{"x":1,"x":2}`, err: `duplicate JSON member "/x" in jsonkeys.keyDoc`},
		// An unknown member is not part of the value: decoding drops it, so
		// neither its spelling nor its repetition changes what is stored.
		{name: "duplicate unknown member", doc: `{"zz":1,"zz":2}`},
		{name: "unknown member", doc: `{"zz":{"Y":1}}`},
		{name: "integer key with a leading zero", doc: `{"ints":{"01":1}}`, err: `JSON map member "/ints/01" does not preserve its decoded key spelling in map[int]int; use canonical JSON member names`},
		{name: "integer key with a plus sign", doc: `{"ints":{"+1":1}}`, err: `JSON map member "/ints/+1" does not preserve its decoded key spelling in map[int]int; use canonical JSON member names`},
		{name: "integer key negative zero", doc: `{"ints":{"-0":1}}`, err: `JSON map member "/ints/-0" does not preserve its decoded key spelling in map[int]int; use canonical JSON member names`},
		{name: "canonical integer keys", doc: `{"ints":{"1":1,"0":2,"-9223372036854775808":3},"uints":{"255":{"y":1}}}`},
		{name: "duplicate integer key", doc: `{"ints":{"1":1,"1":2}}`, err: `duplicate JSON map member "/ints/1" in map[int]int`},
		{name: "duplicate string key", doc: `{"Names":{"a":null,"a":{"y":1}}}`, err: `duplicate JSON map member "/Names/a" in map[string]*jsonkeys.keyInner`},
		{name: "address key in a longer form", doc: `{"addrs":{"0:0:0:0:0:0:0:1":1}}`, err: `JSON map member "/addrs/0:0:0:0:0:0:0:1" does not preserve its decoded key spelling in map[netip.Addr]int; use canonical JSON member names`},
		{name: "canonical address key", doc: `{"addrs":{"::ffff:1.2.3.4":1,"::1":2}}`},
		{name: "element member in another case", doc: `{"items":[{"y":1},{"Y":2}]}`, err: `JSON member "/items/1/Y" is noncanonical for jsonkeys.keyInner; use "y"`},
		{name: "array element member in another case", doc: `{"pair":[{"Y":2}]}`, err: `JSON member "/pair/0/Y" is noncanonical for jsonkeys.keyInner; use "y"`},
		{name: "map value member in another case", doc: `{"uints":{"7":{"Y":1}}}`, err: `JSON member "/uints/7/Y" is noncanonical for jsonkeys.keyInner; use "y"`},
		{name: "nested document", doc: `{"nested":{"nested":{"X":1}}}`, err: `JSON member "/nested/nested/X" is noncanonical for jsonkeys.keyDoc; use "x"`},
		{name: "member promoted from a pointer embed", doc: `{"E":"v"}`, err: `JSON member "/E" is noncanonical for jsonkeys.keyDoc; use "e"`},
		{name: "escaped member name", doc: `{"a~b/c":1,"x":2}`},
		{name: "escaped name in a path", doc: `{"Names":{"a/b~c":{"Y":1}}}`, err: `JSON member "/Names/a~1b~0c/Y" is noncanonical for jsonkeys.keyInner; use "y"`},
		{name: "raw message is opaque", doc: `{"raw":{"X":1,"X":2}}`},
		{name: "a type decoding itself is opaque", doc: `{"own":{"M":{"a":1,"a":2}}}`},
		{name: "interface is opaque", doc: `{"any":{"X":1,"X":2}}`},
		{name: "top-level map", doc: `{"1":{"Y":1}}`, typ: reflect.TypeFor[map[int]keyInner](), err: `JSON member "/1/Y" is noncanonical for jsonkeys.keyInner; use "y"`},
		{name: "top-level slice", doc: `[{"y":1},null,{"y":2}]`, typ: reflect.TypeFor[[]*keyInner]()},
		{name: "one name in two cases", doc: `{"K":1,"k":2}`, typ: reflect.TypeFor[keyCasesUpper]()},
		{name: "a fold two cases share, upper first", doc: "{\"\u212a\":1}", typ: reflect.TypeFor[keyCasesUpper](), err: "JSON member \"/\u212a\" is noncanonical for jsonkeys.keyCasesUpper; use \"K\""},
		{name: "a fold two cases share, lower first", doc: "{\"\u212a\":1}", typ: reflect.TypeFor[keyCasesLower](), err: "JSON member \"/\u212a\" is noncanonical for jsonkeys.keyCasesLower; use \"k\""},
		{name: "unknown beside two cases", doc: `{"K":1,"kk":2}`, typ: reflect.TypeFor[keyCasesUpper]()},
		{name: "a promoted fold below a shallower one", doc: `{"Alpha":1}`, typ: reflect.TypeFor[keyPromoted](), err: `JSON member "/Alpha" is noncanonical for jsonkeys.keyPromoted; use "ALPHA"`},
		{name: "promoted member", doc: `{"alpha":1,"ALPHA":2}`, typ: reflect.TypeFor[keyPromoted]()},
		// Decoding matches a case:strict field only by its exact name, so
		// another spelling is unknown and dropped.
		{name: "case:strict field in another case", doc: `{"NAME":1}`, typ: reflect.TypeFor[keyStrict]()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ := tt.typ
			if typ == nil {
				typ = docType
			}
			err := decodeAndValidate(t, tt.doc, typ)
			if tt.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.err)
		})
	}
}

func TestValidate_RefusesBadInput(t *testing.T) {
	assert.EqualError(t, Validate([]byte(`{}`), keyDoc{}), "JSON key validation target must be a non-nil pointer, got jsonkeys.keyDoc")
	assert.EqualError(t, Validate([]byte(`{}`), (*keyDoc)(nil)), "JSON key validation target must be a non-nil pointer, got *jsonkeys.keyDoc")
	assert.EqualError(t, Validate([]byte(`{"x":`), new(keyDoc)), "invalid JSON document")
}

type textKey string

func (k textKey) MarshalText() ([]byte, error) { return []byte(k), nil }

func (k *textKey) UnmarshalText(b []byte) error {
	*k = textKey("k:" + string(b))
	return nil
}

func TestCanonicalMapKey(t *testing.T) {
	tests := []struct {
		name      string
		keyType   reflect.Type
		member    string
		key       any
		canonical string
		err       bool
	}{
		{name: "string", keyType: reflect.TypeFor[string](), member: "Ab", key: "Ab", canonical: "Ab"},
		{name: "named string", keyType: reflect.TypeFor[namedKey](), member: "x", key: namedKey("x"), canonical: "x"},
		{name: "integer", keyType: reflect.TypeFor[int](), member: "12", key: 12, canonical: "12"},
		{name: "integer with a plus sign", keyType: reflect.TypeFor[int16](), member: "+12", key: int16(12), canonical: "12"},
		{name: "integer with a leading zero", keyType: reflect.TypeFor[int64](), member: "-012", key: int64(-12), canonical: "-12"},
		{name: "unsigned", keyType: reflect.TypeFor[uint8](), member: "255", key: uint8(255), canonical: "255"},
		{name: "integer out of range", keyType: reflect.TypeFor[int8](), member: "128", err: true},
		{name: "unsigned negative", keyType: reflect.TypeFor[uint](), member: "-1", err: true},
		{name: "integer as text", keyType: reflect.TypeFor[int](), member: "a", err: true},
		{name: "address", keyType: reflect.TypeFor[netip.Addr](), member: "0:0:0:0:0:0:0:1", key: netip.MustParseAddr("::1"), canonical: "::1"},
		{name: "address that does not parse", keyType: reflect.TypeFor[netip.Addr](), member: "x", err: true},
		// A string kind decodes through UnmarshalText and encodes as itself.
		{name: "text key of string kind", keyType: reflect.TypeFor[textKey](), member: "a", key: textKey("k:a"), canonical: "k:a"},
		{name: "float", keyType: reflect.TypeFor[float64](), member: "1.50", key: 1.5, canonical: "1.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, canonical, err := CanonicalMapKey(tt.keyType, tt.member)
			if tt.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.key, key.Interface())
			assert.Equal(t, tt.canonical, canonical)
		})
	}
	_, _, err := CanonicalMapKey(reflect.TypeFor[[]int](), "a")
	assert.EqualError(t, err, "invalid JSON map key type []int")
	_, _, err = CanonicalMapKey(nil, "a")
	assert.EqualError(t, err, "invalid JSON map key type <nil>")
}

type benchChild struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
}

type benchDoc struct {
	Labels   map[string]string     `json:"labels"`
	Counts   map[int64]int         `json:"counts"`
	Children map[string]benchChild `json:"children"`
	Items    []benchChild          `json:"items"`
}

// mapHeavyDocument is a stored document whose members are mostly map keys:
// keys string and integer keys, each child with its own attribute map.
func mapHeavyDocument(keys int) []byte {
	doc := benchDoc{Labels: map[string]string{}, Counts: map[int64]int{}, Children: map[string]benchChild{}}
	for i := range keys {
		doc.Labels[fmt.Sprintf("label-%d", i)] = "v"
		doc.Counts[int64(i*7919-keys)] = i
		child := benchChild{Name: fmt.Sprint(i), Attrs: map[string]string{"a": "1", "b": "2", "c": "3"}}
		doc.Children[fmt.Sprintf("child-%d", i)] = child
		doc.Items = append(doc.Items, child)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

func BenchmarkValidate(b *testing.B) {
	raw := mapHeavyDocument(50)
	var decoded benchDoc
	require.NoError(b, json.Unmarshal(raw, &decoded))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if err := Validate(raw, &decoded); err != nil {
			b.Fatal(err)
		}
	}
}

// Validation costs no allocation per member for string and integer keys:
// a document ten times the size allocates no more.
func TestValidate_AllocatesNothingPerKey(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's instrumentation allocates")
	}
	allocs := func(keys int) float64 {
		raw := mapHeavyDocument(keys)
		var decoded benchDoc
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.NoError(t, Validate(raw, &decoded))
		return testing.AllocsPerRun(50, func() { _ = Validate(raw, &decoded) })
	}
	small, large := allocs(10), allocs(100)
	assert.Equal(t, small, large, "allocations grow with the document")
	assert.LessOrEqual(t, small, 1.0)
}

// Members no field names cost no allocation either, whether their names are
// short enough to fold or longer than every field's.
func TestValidate_UnknownMembersAllocateNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's instrumentation allocates")
	}
	allocs := func(members int) float64 {
		doc := map[string]int{"x": 1}
		for i := range members {
			doc[fmt.Sprintf("u%d", i)] = i
			doc[fmt.Sprintf("unknown-%0200d", i)] = i
		}
		raw, err := json.Marshal(doc)
		require.NoError(t, err)
		var decoded keyDoc
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.NoError(t, Validate(raw, &decoded))
		return testing.AllocsPerRun(50, func() { _ = Validate(raw, &decoded) })
	}
	small, large := allocs(10), allocs(100)
	assert.Equal(t, small, large, "allocations grow with the unknown members")
	assert.LessOrEqual(t, small, 1.0)
}

// The rewrite accepts and refuses what the implementation it replaced did,
// on a type whose members that implementation could name.
func FuzzValidate(f *testing.F) {
	for _, seed := range []string{
		`{"x":1,"items":[{"y":1}],"ints":{"-3":1,"10":2},"Names":{"a":{"y":2}},"e":"v"}`,
		`{"X":1}`, `{"names":{}}`, `{"x":1,"x":2}`, `{"zz":1,"zz":2}`, `{"ints":{"01":1}}`, `{"ints":{"+1":1,"1":2}}`,
		`{"ints":{"-0":1}}`, `{"Names":{"a":null,"a":{"y":1}}}`, `{"addrs":{"0:0:0:0:0:0:0:1":1,"::1":2}}`,
		`{"items":[{"y":1},{"Y":2}]}`, `{"pair":[{"Y":2},{"Y":3}]}`, `{"uints":{"7":{"Y":1},"07":{"y":1}}}`,
		`{"nested":{"nested":{"X":1}}}`, `{"E":"v"}`, `{"x":1,"X":2}`, `{"raw":{"X":1,"X":2}}`,
		`{"own":{"M":{"a":1,"a":2}}}`, `{"any":{"X":1,"X":2}}`, `null`, `{}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var decoded keyDoc
		if json.Unmarshal(raw, &decoded) != nil {
			return
		}
		got := Validate(raw, &decoded)
		want := referenceValidate(raw, &decoded)
		if (got == nil) != (want == nil) {
			t.Fatalf("validation of %s: got %v, the replaced implementation %v", raw, got, want)
		}
	})
}

// A document type whose tag reflect cannot read whole is refused, naming the
// field. The type is built at run time, as vet refuses such a tag in source.
func TestValidate_RefusesUnreadableTags(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{{Name: "Doc", Type: reflect.TypeFor[int](), Tag: `json:"doc" bson: "doc"`}})
	err := Validate([]byte(`{"doc":1}`), reflect.New(typ).Interface())
	assert.ErrorContains(t, err, "field Doc")
}
