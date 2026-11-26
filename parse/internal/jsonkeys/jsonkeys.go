// Package jsonkeys checks that a stored JSON document spells its members as
// encoding/json writes them for the Go value it decodes into, so a document
// read from a row or written by a patch has one spelling per member: the
// row scan of a stored document and typed patch application share it. It
// walks the shape core.JSONDocumentOf compiles for the value's type, and
// keeps no type metadata of its own.
package jsonkeys

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// CanonicalMapKey decodes a member name into a key of map key type t, as
// encoding/json decodes it, and returns the key and the member name
// encoding/json writes for that key (gowalker's Shape.JSONKey).
func CanonicalMapKey(t reflect.Type, name string) (reflect.Value, string, error) {
	if t == nil {
		return reflect.Value{}, "", invalidKeyType(t)
	}
	shape, err := core.JSONDocumentOf(t)
	if err != nil {
		return reflect.Value{}, "", err
	}
	if shape.CheckJSONKey() != nil {
		return reflect.Value{}, "", invalidKeyType(t)
	}
	key, canonical, ok := shape.JSONKey(name)
	if !ok {
		return reflect.Value{}, "", fmt.Errorf("JSON member %q is not a %s map key", name, gowalker.TypeText(t))
	}
	return key, canonical, nil
}

func invalidKeyType(t reflect.Type) error {
	return fmt.Errorf("invalid JSON map key type %v", gowalker.TypeText(t))
}

// Validate checks that raw, a document decoded into decoded (a non-nil
// pointer to the decoded value), spells every member the value holds as
// encoding/json writes it: a struct member by its field's
// name exactly, not in another case; a map member by its key's name, so
// "01" is not an int key and "0:0:0:0:0:0:0:1" not a netip.Addr one; and
// no member the value holds twice. A member name is judged as decoding
// reads it, invalid UTF-8 replaced. Members the type does not hold, and
// interface, json.RawMessage and self-decoding subtrees, are opaque. Only
// decoded's type is read, through its compiled shape
// (core.JSONDocumentOf), so validation is one pass over raw.
func Validate(raw []byte, decoded any) error {
	target := reflect.ValueOf(decoded)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("JSON key validation target must be a non-nil pointer, got %s", gowalker.TypeText(reflect.TypeOf(decoded)))
	}
	shape, err := core.JSONDocumentOf(target.Type().Elem())
	if err != nil {
		return fmt.Errorf("JSON key validation: %w", err)
	}
	v, _ := keyValidators.Get().(*keyValidator)
	defer v.release()
	v.reader.Reset(raw)
	v.dec.Reset(&v.reader, decodedJSONOptions)
	err = v.value(shape)
	if err != nil {
		return err
	}
	_, err = v.dec.ReadToken()
	if !errors.Is(err, io.EOF) {
		return errInvalidJSONDocument
	}
	return nil
}

var errInvalidJSONDocument = errors.New("invalid JSON document")

// decodedJSONOptions read a document as encoding/json decodes one: a
// repeated member and invalid UTF-8 are accepted, and judged here.
var decodedJSONOptions = jsonv2.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

// decodedShape follows shape's pointers to the shape a JSON value decodes
// into, stopping at a leaf. A pointer type that leads back to itself, which
// no document decodes into past null, is nil.
func decodedShape(shape *gowalker.Shape) *gowalker.Shape {
	slow := shape
	for shape.Kind() == reflect.Pointer && !shape.Atomic() {
		shape = shape.Elem()
		if shape.Kind() != reflect.Pointer || shape.Atomic() {
			return shape
		}
		shape, slow = shape.Elem(), slow.Elem()
		if shape == slow {
			return nil
		}
	}
	return shape
}

// CheckDecodable refuses a document type encoding/json cannot decode every
// member it writes into: one holding, at any depth, a struct whose member is
// promoted through an unexported embedded pointer (core.CheckWritableField).
// encoding/json writes that member while the pointer is set, and decoding
// into a fresh value, where the pointer is nil, fails on it. Self-decoding
// subtrees are opaque, as Validate reads them.
func CheckDecodable(t reflect.Type) error {
	switch t.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Pointer:
	default:
		// A scalar or an interface holds no member: the common column
		// needs no compiled shape.
		return nil
	}
	shape, err := core.JSONDocumentOf(t)
	if err != nil {
		return err
	}
	return checkDecodable(shape, map[*gowalker.Shape]bool{})
}

func checkDecodable(shape *gowalker.Shape, seen map[*gowalker.Shape]bool) error {
	shape = decodedShape(shape)
	if shape == nil || shape.Atomic() || seen[shape] {
		return nil
	}
	seen[shape] = true
	switch shape.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return checkDecodable(shape.Elem(), seen)
	case reflect.Struct:
		for _, f := range shape.Fields() {
			err := core.CheckWritableField(f)
			if err == nil {
				err = checkDecodable(f.Shape(), seen)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", gowalker.TypeText(shape.Type()), err)
			}
		}
	}
	return nil
}

// keyValidator is one validation's scratch state, pooled so a validation
// allocates nothing per member.
type keyValidator struct {
	reader bytes.Reader
	dec    *jsontext.Decoder
	// name holds the member name just read when it had to be unquoted.
	name []byte
	// seen marks the struct members met in each open struct object, a
	// stack, by their fields' Ordinal.
	seen []bool
	// mapNames and mapSpans hold the member names of each open map object,
	// a stack, to find a repeated one when the object ends.
	mapNames []byte
	mapSpans []nameSpan
}

type nameSpan struct{ start, end int }

var keyValidators = sync.Pool{New: func() any {
	v := new(keyValidator)
	v.dec = jsontext.NewDecoder(&v.reader, decodedJSONOptions)
	return v
}}

// maxPooledScratch bounds the scratch a pooled validator keeps.
const maxPooledScratch = 64 << 10

func (v *keyValidator) release() {
	v.reader.Reset(nil)
	v.dec.Reset(&v.reader, decodedJSONOptions)
	if cap(v.mapNames) > maxPooledScratch || cap(v.seen) > maxPooledScratch {
		return
	}
	v.name = v.name[:0]
	v.seen, v.mapNames, v.mapSpans = v.seen[:0], v.mapNames[:0], v.mapSpans[:0]
	keyValidators.Put(v)
}

// value reads one JSON value bound for shape.
func (v *keyValidator) value(shape *gowalker.Shape) error {
	shape = decodedShape(shape)
	if shape == nil || shape.Atomic() {
		return v.skip()
	}
	kind := v.dec.PeekKind()
	switch shape.Kind() {
	case reflect.Struct, reflect.Map:
		if kind == '{' {
			return v.object(shape)
		}
	case reflect.Slice, reflect.Array:
		if kind == '[' {
			return v.list(shape)
		}
	}
	return v.skip()
}

// object reads an object bound for a struct or map shape.
func (v *keyValidator) object(shape *gowalker.Shape) error {
	_, err := v.dec.ReadToken()
	if err != nil {
		return errInvalidJSONDocument
	}
	isStruct := shape.Kind() == reflect.Struct
	seenStart, spansStart, namesStart := len(v.seen), len(v.mapSpans), len(v.mapNames)
	fields := 0
	if isStruct {
		fields = shape.NumFields()
		v.seen = slices.Grow(v.seen, fields)[:seenStart+fields]
		clear(v.seen[seenStart:])
	}
	for v.dec.PeekKind() != '}' {
		name, err := v.memberName()
		if err != nil {
			return err
		}
		if isStruct {
			// Resliced each time: a nested object may have grown v.seen.
			err = v.structMember(shape, v.seen[seenStart:seenStart+fields], name)
		} else {
			err = v.mapMember(shape, name)
		}
		if err != nil {
			return err
		}
	}
	_, err = v.dec.ReadToken()
	if err != nil {
		return errInvalidJSONDocument
	}
	if !isStruct {
		err = v.repeatedMapMember(shape, v.mapSpans[spansStart:])
		if err != nil {
			return err
		}
	}
	v.seen, v.mapSpans, v.mapNames = v.seen[:seenStart], v.mapSpans[:spansStart], v.mapNames[:namesStart]
	return nil
}

// memberName reads a member name as decoding reads it, valid until the
// next read.
func (v *keyValidator) memberName() ([]byte, error) {
	raw, err := v.dec.ReadValue()
	if err != nil || len(raw) < 2 {
		return nil, errInvalidJSONDocument
	}
	quoted := raw[1 : len(raw)-1]
	if bytes.IndexByte(quoted, '\\') < 0 && utf8.Valid(quoted) {
		return quoted, nil
	}
	// The decoder has read the name, so the only error left is invalid
	// UTF-8, which it accepts and decoding replaces, as here.
	v.name, _ = jsontext.AppendUnquote(v.name[:0], raw)
	return v.name, nil
}

// structMember reads the member name of a struct object; seen marks the
// object's members met so far.
func (v *keyValidator) structMember(shape *gowalker.Shape, seen []bool, name []byte) error {
	field, match := shape.MatchBytes(name)
	switch match {
	case gowalker.NoMatch:
		return v.skip() // not a member of the type
	case gowalker.FoldedMatch:
		// Decoding gives a member no field names exactly to a field its
		// name matches by case.
		return fmt.Errorf("JSON member %q is noncanonical for %s; use %q", v.dec.StackPointer(), gowalker.TypeText(shape.Type()), field.Name())
	}
	if seen[field.Ordinal()] {
		return fmt.Errorf("duplicate JSON member %q in %s", v.dec.StackPointer(), gowalker.TypeText(shape.Type()))
	}
	seen[field.Ordinal()] = true
	return v.value(field.Shape())
}

func (v *keyValidator) mapMember(shape *gowalker.Shape, name []byte) error {
	if !shape.Key().IsCanonicalJSONKey(name) {
		return fmt.Errorf("JSON map member %q does not preserve its decoded key spelling in %s; use canonical JSON member names", v.dec.StackPointer(), gowalker.TypeText(shape.Type()))
	}
	start := len(v.mapNames)
	v.mapNames = append(v.mapNames, name...)
	v.mapSpans = append(v.mapSpans, nameSpan{start, len(v.mapNames)})
	return v.value(shape.Elem())
}

// repeatedMapMember refuses a name a map object gave twice, at its end:
// every name is canonical, so two members name one key exactly when their
// names are equal.
func (v *keyValidator) repeatedMapMember(shape *gowalker.Shape, spans []nameSpan) error {
	names := v.mapNames
	slices.SortFunc(spans, func(a, b nameSpan) int { return bytes.Compare(names[a.start:a.end], names[b.start:b.end]) })
	for i := 1; i < len(spans); i++ {
		name := names[spans[i].start:spans[i].end]
		if bytes.Equal(name, names[spans[i-1].start:spans[i-1].end]) {
			return fmt.Errorf("duplicate JSON map member %q in %s", string(v.dec.StackPointer())+"/"+core.EscapePointerToken(string(name)), gowalker.TypeText(shape.Type()))
		}
	}
	return nil
}

func (v *keyValidator) list(shape *gowalker.Shape) error {
	_, err := v.dec.ReadToken()
	if err != nil {
		return errInvalidJSONDocument
	}
	length := -1 // a slice's
	if shape.Kind() == reflect.Array {
		length = shape.Type().Len()
	}
	for i := 0; v.dec.PeekKind() != ']'; i++ {
		if length >= 0 && i >= length {
			err = v.skip() // decoding drops elements past an array's end
		} else {
			err = v.value(shape.Elem())
		}
		if err != nil {
			return err
		}
	}
	_, err = v.dec.ReadToken()
	if err != nil {
		return errInvalidJSONDocument
	}
	return nil
}

func (v *keyValidator) skip() error {
	err := v.dec.SkipValue()
	if err != nil {
		return errInvalidJSONDocument
	}
	return nil
}
