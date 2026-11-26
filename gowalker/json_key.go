package gowalker

import (
	"encoding"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"
)

// jsonKeyRule is how encoding/json reads a member name into a map key of one
// type and writes the key back, decided when the type's shape is built.
type jsonKeyRule uint8

const (
	// jsonKeyCodec types are read and written through encoding/json itself:
	// text methods (AppendText, MarshalText, UnmarshalText), floats,
	// pointers and the empty interface.
	jsonKeyCodec jsonKeyRule = iota
	// jsonKeyString is a string kind without text methods: the name is the
	// key.
	jsonKeyString
	// jsonKeySigned and jsonKeyUnsigned are integer kinds without text
	// methods, read as base-10 text and written without a sign or leading
	// zero.
	jsonKeySigned
	jsonKeyUnsigned
	// jsonKeyRefused types encoding/json reads no member name into, or
	// writes none for.
	jsonKeyRefused
)

var errJSONKeyRefused = errors.New("encoding/json reads no member name into it, or writes none for it")

func jsonKeyRuleOf(t reflect.Type) jsonKeyRule {
	if !t.Comparable() {
		return jsonKeyRefused
	}
	reads, writes := jsonKeyAccess(t, nil)
	if !reads || !writes {
		return jsonKeyRefused
	}
	if implementsText(t, reflect.TypeFor[encoding.TextAppender]()) || implementsText(t, reflect.TypeFor[encoding.TextMarshaler]()) || implementsText(t, reflect.TypeFor[encoding.TextUnmarshaler]()) {
		return jsonKeyCodec
	}
	switch t.Kind() {
	case reflect.String:
		return jsonKeyString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return jsonKeySigned
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return jsonKeyUnsigned
	}
	return jsonKeyCodec
}

// jsonKeyAccess reports whether encoding/json reads a member name into a map
// key of type t, and whether it writes one for such a key. It ignores JSON
// methods, which encoding/json does not call on a key, and calls
// AppendText or MarshalText only on the key's value: a map key is not
// addressable. A
// pointer reads and writes what it points to, unless it has the text method
// itself; pointers are the types already followed, against a pointer type
// that points to itself.
func jsonKeyAccess(t reflect.Type, pointers []reflect.Type) (reads, writes bool) {
	reads = implementsText(t, reflect.TypeFor[encoding.TextUnmarshaler]())
	writes = t.Implements(reflect.TypeFor[encoding.TextAppender]()) || t.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	switch t.Kind() {
	case reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true, true
	case reflect.Interface:
		// Decoding cannot choose a type for a non-empty interface; the empty
		// one holds the name as a string.
		return t.NumMethod() == 0, true
	case reflect.Pointer:
		if !slices.Contains(pointers, t) {
			elemReads, elemWrites := jsonKeyAccess(t.Elem(), append(pointers, t))
			reads, writes = reads || elemReads, writes || elemWrites
		}
	}
	return reads, writes
}

// implementsText reports t or *t implementing iface, as encoding/json finds
// a text method on an addressable value's pointer.
func implementsText(t, iface reflect.Type) bool {
	return t.Implements(iface) || t.Kind() != reflect.Interface && reflect.PointerTo(t).Implements(iface)
}

// JSONKey reads name, a JSON object member name, into a key of a map whose
// key type is s's type, as encoding/json reads one, and returns the key and
// its canonical text: the member name encoding/json writes for that key. ok
// is false when encoding/json reads no key from name or writes no name for
// the key it reads: for a type it refuses ([Shape.CheckJSONKey]), or a name
// such as "x" or "300" for an int8 key.
//
// A string kind is its own text, each byte of invalid UTF-8 replaced by
// U+FFFD as encoding/json replaces it. An integer kind reads base-10 text,
// leading zeros included and, for a signed kind, a sign, and writes it
// without either. Any other key
// type, a string or integer with AppendText, MarshalText or UnmarshalText
// included, is read and written by encoding/json itself, which allocates on
// each call.
// JSON methods play no part: encoding/json does not call them on a key.
func (s *Shape) JSONKey(name string) (key reflect.Value, canonical string, ok bool) {
	switch s.jsonKey {
	case jsonKeyRefused:
		return reflect.Value{}, "", false
	case jsonKeyString:
		if !utf8.ValidString(name) {
			// Each invalid byte decodes as one RuneError.
			name = string([]rune(name))
		}
		key = reflect.New(s.typ).Elem()
		key.SetString(name)
		return key, name, true
	case jsonKeySigned:
		n, err := strconv.ParseInt(name, 10, 64)
		key = reflect.New(s.typ).Elem()
		if err != nil || key.OverflowInt(n) {
			return reflect.Value{}, "", false
		}
		key.SetInt(n)
		return key, strconv.FormatInt(n, 10), true
	case jsonKeyUnsigned:
		n, err := strconv.ParseUint(name, 10, 64)
		key = reflect.New(s.typ).Elem()
		if err != nil || key.OverflowUint(n) {
			return reflect.Value{}, "", false
		}
		key.SetUint(n)
		return key, strconv.FormatUint(n, 10), true
	}
	return codecJSONKey(s.typ, name)
}

// IsCanonicalJSONKey reports whether name is the canonical text of the key it
// reads into: JSONKey reads a key from it and returns name as that key's
// text, so "7" is canonical for an int key and "07" and "+7" are not, nor a
// name holding invalid UTF-8 for a string key. For a string or integer kind
// without text methods it allocates nothing; it keeps no reference to name.
func (s *Shape) IsCanonicalJSONKey(name []byte) bool {
	switch s.jsonKey {
	case jsonKeyRefused:
		return false
	case jsonKeyString:
		return utf8.Valid(name)
	case jsonKeySigned:
		negative := len(name) > 0 && name[0] == '-'
		digits := name
		if negative {
			digits = name[1:]
		}
		n, ok := canonicalDigits(digits)
		if !ok || negative && n == 0 {
			return false
		}
		limit := uint64(1)<<(s.typ.Bits()-1) - 1
		if negative {
			limit++
		}
		return n <= limit
	case jsonKeyUnsigned:
		n, ok := canonicalDigits(name)
		return ok && n <= math.MaxUint64>>(64-s.typ.Bits())
	}
	_, canonical, ok := codecJSONKey(s.typ, string(name))
	return ok && canonical == string(name)
}

// canonicalDigits reads base-10 digits without a leading zero, or "0", and
// fails past the uint64 range.
func canonicalDigits(digits []byte) (uint64, bool) {
	if len(digits) == 0 || digits[0] == '0' && len(digits) > 1 {
		return 0, false
	}
	var n uint64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		digit := uint64(c - '0')
		if n > (math.MaxUint64-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	return n, true
}

// CheckJSONKey returns an error matching ErrKey when encoding/json refuses
// s's type as a map key: it reads no member name into a key of the type, or
// writes none for one. That is a type that is not comparable, a non-empty
// interface, a bool, complex, struct, array, channel or unsafe.Pointer kind
// without both UnmarshalText and an AppendText or MarshalText on its value,
// and a pointer
// to a refused type that lacks those methods itself. A nil result does not
// promise that every name reads: [Shape.JSONKey] decides each. The error
// names the type, never a name.
func (s *Shape) CheckJSONKey() error {
	if s.jsonKey != jsonKeyRefused {
		return nil
	}
	return failure("json key", ErrKey, s.typ, emptyPath, -1, errJSONKeyRefused)
}

// codecJSONKey asks encoding/json for the key it reads name into and the
// name it writes for that key. The map's values are raw JSON, so no value
// codec runs.
func codecJSONKey(t reflect.Type, name string) (reflect.Value, string, bool) {
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
