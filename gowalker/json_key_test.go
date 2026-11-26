package gowalker_test

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/ashtonian/pkg/gowalker"
)

// readTextKey is a string kind read through UnmarshalText and written as
// itself, as encoding/json writes every string kind.
type readTextKey string

func (k *readTextKey) UnmarshalText(text []byte) error {
	*k = readTextKey("k:" + string(text))
	return nil
}

// writeTextKey is an integer kind written through MarshalText and read as
// a number.
type writeTextKey int

func (k writeTextKey) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("m%d", int(k))), nil }

// pointText is a struct key with both text methods.
type pointText struct{ X, Y int }

func (p pointText) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "%d,%d", p.X, p.Y), nil }

func (p *pointText) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "%d,%d", &p.X, &p.Y)
	return err
}

// pointerText's MarshalText has a pointer receiver, which encoding/json does
// not call on a map key: it reads such a key and never writes one.
type pointerText struct{ V string }

func (p *pointerText) MarshalText() ([]byte, error) { return []byte(p.V), nil }

func (p *pointerText) UnmarshalText(text []byte) error {
	p.V = string(text)
	return nil
}

// appendKey is an integer kind written through AppendText, which
// encoding/json prefers to MarshalText, and read as a number.
type appendKey uint8

func (k appendKey) AppendText(b []byte) ([]byte, error) {
	return fmt.Appendf(b, "x%02x", uint8(k)), nil
}

// appendString is a string kind, which encoding/json writes as itself
// though it has AppendText.
type appendString string

func (k appendString) AppendText(b []byte) ([]byte, error) {
	return append(append(b, "s:"...), k...), nil
}

// appendPoint is a struct key written through AppendText on its value and
// read through UnmarshalText on its pointer.
type appendPoint struct{ X int }

func (p appendPoint) AppendText(b []byte) ([]byte, error) { return fmt.Appendf(b, "p%d", p.X), nil }

func (p *appendPoint) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "p%d", &p.X)
	return err
}

// jsonInt and jsonStruct have JSON methods, which encoding/json does not use
// for map keys.
type jsonInt int

func (jsonInt) MarshalJSON() ([]byte, error) { return []byte(`"j"`), nil }

func (k *jsonInt) UnmarshalJSON([]byte) error { *k = 42; return nil }

type jsonStruct struct{ A int }

func (jsonStruct) MarshalJSON() ([]byte, error) { return []byte(`"s"`), nil }

func (k *jsonStruct) UnmarshalJSON([]byte) error { k.A = 7; return nil }

type (
	namedString string
	namedBool   bool
	namedFloat  float32
)

// encodingJSONKey is what encoding/json itself makes of name for map key
// type t: the key it reads name into and the member name it writes for that
// key, or ok false when it reads or writes none.
func encodingJSONKey(t reflect.Type, name string) (reflect.Value, string, bool) {
	if !t.Comparable() {
		return reflect.Value{}, "", false
	}
	raw, err := json.Marshal(map[string]any{name: nil})
	if err != nil {
		return reflect.Value{}, "", false
	}
	read := reflect.New(reflect.MapOf(t, reflect.TypeFor[json.RawMessage]()))
	if json.Unmarshal(raw, read.Interface()) != nil || read.Elem().Len() != 1 {
		return reflect.Value{}, "", false
	}
	key := read.Elem().MapKeys()[0]
	written, err := json.Marshal(read.Elem().Interface())
	if err != nil {
		return reflect.Value{}, "", false
	}
	var names map[string]json.RawMessage
	if json.Unmarshal(written, &names) != nil || len(names) != 1 {
		return reflect.Value{}, "", false
	}
	for canonical := range names {
		return key, canonical, true
	}
	return reflect.Value{}, "", false
}

// jsonKeyTypes are key types of every rule: the string and integer kinds
// read and written by their kind, the types whose text methods or float,
// pointer and interface rules encoding/json applies, and those it refuses.
func jsonKeyTypes() []reflect.Type {
	types := []reflect.Type{
		reflect.TypeFor[string](), reflect.TypeFor[namedString](),
		reflect.TypeFor[int](), reflect.TypeFor[int8](), reflect.TypeFor[int16](), reflect.TypeFor[int32](), reflect.TypeFor[int64](),
		reflect.TypeFor[uint](), reflect.TypeFor[uint8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint32](), reflect.TypeFor[uint64](), reflect.TypeFor[uintptr](),
		reflect.TypeFor[jsonInt](),
		reflect.TypeFor[float32](), reflect.TypeFor[float64](), reflect.TypeFor[namedFloat](),
		reflect.TypeFor[readTextKey](), reflect.TypeFor[writeTextKey](), reflect.TypeFor[pointText](), reflect.TypeFor[netip.Addr](),
		reflect.TypeFor[appendKey](), reflect.TypeFor[appendString](), reflect.TypeFor[appendPoint](),
		reflect.TypeFor[any](), reflect.TypeFor[*int](), reflect.TypeFor[*pointerText](), reflect.TypeFor[**pointText](),
		reflect.TypeFor[bool](), reflect.TypeFor[namedBool](), reflect.TypeFor[complex128](), reflect.TypeFor[struct{ A int }](),
		reflect.TypeFor[[2]int](), reflect.TypeFor[chan int](), reflect.TypeFor[unsafe.Pointer](), reflect.TypeFor[fmt.Stringer](),
		reflect.TypeFor[encoding.TextUnmarshaler](), reflect.TypeFor[*bool](), reflect.TypeFor[pointerText](), reflect.TypeFor[jsonStruct](),
		reflect.TypeFor[[]int](), reflect.TypeFor[map[string]int](), reflect.TypeFor[func()](),
	}
	return types
}

// jsonKeyNames are member names around each rule's edges: signs, leading
// zeros, every integer kind's bounds, numbers that are not integers, JSON
// literals, text that is no number, and invalid UTF-8.
func jsonKeyNames() []string {
	names := []string{
		"", "0", "-0", "+0", "1", "+1", "01", "-01", "-1", " 1", "1 ", "1e2", "0x10", "1_000", "1.5", "1.50", "-0.0",
		"127", "128", "-128", "-129", "255", "256", "32767", "-32769", "65535", "65536",
		"2147483647", "2147483648", "-2147483649", "4294967295", "4294967296",
		"9223372036854775807", "9223372036854775808", "-9223372036854775808", "-9223372036854775809",
		"18446744073709551615", "18446744073709551616", "99999999999999999999999",
		"a", "Ab", "true", "false", "NaN", "Infinity", "::1", "0:0:0:0:0:0:0:1", "192.0.2.1", "1,2", "01,2",
		"x07", "x7", "p3", "p03",
		"null", "Null", "nul", "00", "-", "+", "1.0", "1e0", "0e0", "-0e0",
		"\xff", "a\xff\xfeb", "\xed\xa0\x80", "é", "<&>",
	}
	return names
}

func jsonKeyShape(t *testing.T, typ reflect.Type) *gowalker.Shape {
	t.Helper()
	schema, err := gowalker.CompileType(typ)
	if err != nil {
		t.Fatalf("%v: %v", typ, err)
	}
	return schema.Root()
}

// JSONKey reads every name into the key encoding/json reads it into, with
// the name encoding/json writes for that key, and refuses exactly the names
// encoding/json reads no key from or writes none for; IsCanonicalJSONKey
// agrees.
func TestShapeJSONKey_AgreesWithEncodingJSON(t *testing.T) {
	for _, typ := range jsonKeyTypes() {
		shape := jsonKeyShape(t, typ)
		for _, name := range jsonKeyNames() {
			wantKey, wantText, wantOK := encodingJSONKey(typ, name)
			key, text, ok := shape.JSONKey(name)
			if ok != wantOK || text != wantText {
				t.Errorf("%v.JSONKey(%q) = %q, %t; encoding/json: %q, %t", typ, name, text, ok, wantText, wantOK)
				continue
			}
			if ok && !reflect.DeepEqual(key.Interface(), wantKey.Interface()) {
				t.Errorf("%v.JSONKey(%q) reads %#v; encoding/json reads %#v", typ, name, key.Interface(), wantKey.Interface())
			}
			if ok && key.Type() != typ {
				t.Errorf("%v.JSONKey(%q) is a %v", typ, name, key.Type())
			}
			// IsCanonicalJSONKey judges a name as a decoder reads it,
			// invalid UTF-8 replaced, whether or not it is passed so.
			read := decodedName(name)
			want := wantOK && wantText == read
			for _, given := range []string{read, name} {
				canonical := shape.IsCanonicalJSONKey([]byte(given))
				if canonical != want {
					t.Errorf("%v.IsCanonicalJSONKey(%q) = %t, want %t", typ, given, canonical, want)
				}
			}
		}
	}
}

// decodedName is name as encoding/json decodes a member name: each byte of
// invalid UTF-8 replaced by U+FFFD.
func decodedName(name string) string {
	var read string
	raw, err := json.Marshal(name)
	if err == nil {
		err = json.Unmarshal(raw, &read)
	}
	if err != nil {
		panic(err)
	}
	return read
}

// CheckJSONKey refuses a type exactly when no name reads into a key of it
// that encoding/json writes back, naming the type and no name.
func TestShapeCheckJSONKey(t *testing.T) {
	for _, typ := range jsonKeyTypes() {
		shape := jsonKeyShape(t, typ)
		readsOne := false
		for _, name := range jsonKeyNames() {
			_, _, ok := encodingJSONKey(typ, name)
			readsOne = readsOne || ok
		}
		err := shape.CheckJSONKey()
		if readsOne != (err == nil) {
			t.Errorf("%v.CheckJSONKey() = %v; encoding/json reads a name into it: %t", typ, err, readsOne)
			continue
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, gowalker.ErrKey) {
			t.Errorf("%v.CheckJSONKey() = %v, want ErrKey", typ, err)
		}
		want := "gowalker: json key " + gowalker.TypeText(typ) + ": unsupported map key: "
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%v.CheckJSONKey() = %q, want it to start %q", typ, err, want)
		}
	}
}

// The key shape of a compiled map carries the rule, so a library walking a
// value's graph reads each map's keys without compiling the key type.
func TestShapeJSONKey_OfAMapKey(t *testing.T) {
	type doc struct {
		Addrs map[netip.Addr]int `json:"addrs"`
		Sizes map[uint8]string   `json:"sizes"`
	}
	schema := schemaFor[doc](t, engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields())))
	addrs, _ := schema.Root().Lookup("addrs")
	sizes, _ := schema.Root().Lookup("sizes")
	key, text, ok := addrs.Shape().Key().JSONKey("0:0:0:0:0:0:0:1")
	if !ok || text != "::1" || key.Interface() != netip.MustParseAddr("::1") {
		t.Errorf("netip.Addr key: %v %q %t", key, text, ok)
	}
	for name, want := range map[string]bool{"7": true, "07": false, "+7": false, "256": false, "-1": false} {
		got := sizes.Shape().Key().IsCanonicalJSONKey([]byte(name))
		if got != want {
			t.Errorf("uint8 key %q canonical: %t, want %t", name, got, want)
		}
	}
	err := sizes.Shape().CheckJSONKey()
	if err == nil {
		t.Error("a map's own shape is no key: want ErrKey")
	}
}

// Checking a string or integer key allocates nothing, however long the
// name.
func TestShapeIsCanonicalJSONKey_AllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's instrumentation allocates")
	}
	long := []byte(strings.Repeat("k", 4096))
	for _, probe := range []struct {
		typ  reflect.Type
		name []byte
	}{
		{reflect.TypeFor[string](), long},
		{reflect.TypeFor[namedString](), []byte("a\xffb")},
		{reflect.TypeFor[int64](), []byte("-9223372036854775808")},
		{reflect.TypeFor[int8](), []byte("0128")},
		{reflect.TypeFor[uint16](), []byte("65535")},
		{reflect.TypeFor[uint](), long},
		{reflect.TypeFor[bool](), []byte("true")},
	} {
		shape := jsonKeyShape(t, probe.typ)
		allocs := testing.AllocsPerRun(20, func() { shape.IsCanonicalJSONKey(probe.name) })
		if allocs != 0 {
			t.Errorf("%v.IsCanonicalJSONKey(%.20q) allocates %v", probe.typ, probe.name, allocs)
		}
	}
}
