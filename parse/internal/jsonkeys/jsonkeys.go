// Package jsonkeys checks that a stored JSON document spells its members as
// encoding/json writes them for the Go value it decodes into, so a document
// read from a row or written by a patch has one spelling per member: the
// row scan of a stored document and typed patch application share it.
package jsonkeys

import (
	"bytes"
	"encoding"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// CanonicalMapKey decodes a member name into a key of map key type t, as
// encoding/json decodes it, and returns the key and the member name
// encoding/json writes for that key.
func CanonicalMapKey(t reflect.Type, name string) (reflect.Value, string, error) {
	if t == nil || !t.Comparable() {
		return reflect.Value{}, "", fmt.Errorf("invalid JSON map key type %v", gowalker.TypeText(t))
	}
	key := reflect.New(t).Elem()
	switch jsonKeyRuleOf(t) {
	case keyAsIs:
		key.SetString(name)
		return key, name, nil
	case keySigned:
		n, err := strconv.ParseInt(name, 10, 64)
		if err != nil || key.OverflowInt(n) {
			return reflect.Value{}, "", fmt.Errorf("JSON member %q is not a %s map key", name, gowalker.TypeText(t))
		}
		key.SetInt(n)
		return key, strconv.FormatInt(n, 10), nil
	case keyUnsigned:
		n, err := strconv.ParseUint(name, 10, 64)
		if err != nil || key.OverflowUint(n) {
			return reflect.Value{}, "", fmt.Errorf("JSON member %q is not a %s map key", name, gowalker.TypeText(t))
		}
		key.SetUint(n)
		return key, strconv.FormatUint(n, 10), nil
	}
	return encodedJSONMapKey(t, name)
}

// jsonKeyRule is how encoding/json turns a member name into a map key of
// one type and back.
type jsonKeyRule uint8

const (
	// keyAsIs: a string kind without text methods; the name is the key.
	keyAsIs jsonKeyRule = iota
	// keySigned, keyUnsigned: an integer kind without text methods, read as
	// strconv reads base-10 text and written as it writes it.
	keySigned
	keyUnsigned
	// keyEncoded: any other key type, whose name only encoding/json can tell.
	keyEncoded
)

// jsonKeyRuleOf is t's key rule, decided once per type.
func jsonKeyRuleOf(t reflect.Type) jsonKeyRule {
	if known, ok := jsonKeyRules.Load(t); ok {
		rule, _ := known.(jsonKeyRule) // the only type stored
		return rule
	}
	rule := keyEncoded
	if !hasTextMethods(t) {
		switch t.Kind() {
		case reflect.String:
			rule = keyAsIs
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			rule = keySigned
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			rule = keyUnsigned
		}
	}
	jsonKeyRules.Store(t, rule)
	return rule
}

var jsonKeyRules sync.Map // reflect.Type -> jsonKeyRule

var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

// hasTextMethods reports a key type encoding/json reads or writes through its
// text methods instead of its kind.
func hasTextMethods(t reflect.Type) bool {
	for _, m := range []reflect.Type{textMarshalerType, textUnmarshalerType} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// encodedJSONMapKey asks encoding/json for name's key and the key's name.
// Dummy values keep the map element's own codec out of the round trip.
func encodedJSONMapKey(t reflect.Type, name string) (reflect.Value, string, error) {
	keyMap := reflect.New(reflect.MapOf(t, rawJSONType))
	raw, err := json.Marshal(map[string]any{name: nil})
	if err != nil {
		return reflect.Value{}, "", err
	}
	if err := json.Unmarshal(raw, keyMap.Interface()); err != nil {
		return reflect.Value{}, "", err
	}
	keys := keyMap.Elem().MapKeys()
	if len(keys) != 1 {
		return reflect.Value{}, "", fmt.Errorf("JSON member %q does not decode to one %s key", name, gowalker.TypeText(t))
	}
	probe := reflect.MakeMapWithSize(keyMap.Elem().Type(), 1)
	probe.SetMapIndex(keys[0], reflect.Zero(rawJSONType))
	raw, err = json.Marshal(probe.Interface())
	if err != nil {
		return reflect.Value{}, "", err
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return reflect.Value{}, "", err
	}
	for canonical := range encoded {
		return keys[0], canonical, nil
	}
	return reflect.Value{}, "", fmt.Errorf("JSON key %s encodes no member", gowalker.TypeText(t))
}

// Validate checks that raw, a document decoded into decoded (a non-nil
// pointer to the decoded value), spells every member the value holds as
// encoding/json writes it: a struct member by its field's
// name exactly, not in another case; a map member by its key's name, so
// "01" is not an int key and "0:0:0:0:0:0:0:1" not a netip.Addr one; and
// no member the value holds twice. Members the type does not hold, and
// interface, json.RawMessage and self-decoding subtrees, are opaque. Only
// decoded's type is read. Each type's members and key rules are found once
// (core.JSONFieldsOf), so validation is one pass over raw.
func Validate(raw []byte, decoded any) error {
	target := reflect.ValueOf(decoded)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("JSON key validation target must be a non-nil pointer, got %s", gowalker.TypeText(reflect.TypeOf(decoded)))
	}
	shape, err := jsonShapeOf(target.Type().Elem())
	if err != nil {
		return fmt.Errorf("JSON key validation: %w", err)
	}
	v, _ := keyValidators.Get().(*keyValidator)
	defer v.release()
	v.reader.Reset(raw)
	v.dec.Reset(&v.reader, decodedJSONOptions)
	if err := v.value(shape); err != nil {
		return err
	}
	if _, err := v.dec.ReadToken(); err != io.EOF {
		return errInvalidJSONDocument
	}
	return nil
}

var errInvalidJSONDocument = errors.New("invalid JSON document")

// decodedJSONOptions read a document as encoding/json decodes one: a
// repeated member and invalid UTF-8 are accepted, and judged here.
var decodedJSONOptions = jsonv2.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

// jsonShape is what a JSON value bound for one Go type holds that its names
// must match.
type jsonShape struct {
	kind shapeKind
	typ  reflect.Type
	// walk and byName name a struct's members; members holds each
	// member's shape, in the walk's order.
	walk    *core.StructFields
	byName  map[string]int
	members []*jsonShape
	// key is a map's key rule; elem a map's value or a list's element.
	key  jsonKeyRule
	elem *jsonShape
	// length is an array's length; a slice has -1.
	length int
}

type shapeKind uint8

const (
	// shapeOpaque values are not read: scalars, interfaces, and types that
	// decode themselves.
	shapeOpaque shapeKind = iota
	shapeStruct
	shapeMap
	shapeList
)

var (
	jsonShapes   sync.Map   // reflect.Type -> *jsonShape
	jsonShapesMu sync.Mutex // serializes building, so a type that contains itself is built once
	opaqueShape  = &jsonShape{kind: shapeOpaque}
)

// jsonShapeOf is t's shape, built with the shapes of every type t reaches.
// A struct type JSONFieldsOf refuses fails it, and stores none of the shapes
// built on the way.
func jsonShapeOf(t reflect.Type) (*jsonShape, error) {
	if known, ok := jsonShapes.Load(t); ok {
		shape, _ := known.(*jsonShape) // the only type stored
		return shape, nil
	}
	jsonShapesMu.Lock()
	defer jsonShapesMu.Unlock()
	building := map[reflect.Type]*jsonShape{}
	shape, err := buildJSONShape(t, building)
	if err != nil {
		return nil, err
	}
	for typ, s := range building {
		jsonShapes.LoadOrStore(typ, s)
	}
	// An opaque root is in no building entry; kept too, the next call for
	// t takes no lock.
	jsonShapes.LoadOrStore(t, shape)
	return shape, nil
}

func buildJSONShape(t reflect.Type, building map[reflect.Type]*jsonShape) (*jsonShape, error) {
	if known, ok := jsonShapes.Load(t); ok {
		shape, _ := known.(*jsonShape)
		return shape, nil
	}
	if shape, ok := building[t]; ok {
		return shape, nil
	}
	if decodesItself(t) || t == rawJSONType {
		return opaqueShape, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		shape, err := buildJSONShape(t.Elem(), building)
		if err != nil {
			return nil, err
		}
		building[t] = shape
		return shape, nil
	case reflect.Struct:
		shape := &jsonShape{kind: shapeStruct, typ: t}
		building[t] = shape
		walk, err := core.JSONFieldsOf(t)
		if err != nil {
			return nil, err
		}
		shape.walk = walk
		shape.byName = make(map[string]int, walk.Len())
		shape.members = make([]*jsonShape, 0, walk.Len())
		for f := range walk.Fields() {
			shape.byName[f.External()] = len(shape.members)
			member, err := buildJSONShape(f.Type(), building)
			if err != nil {
				return nil, err
			}
			shape.members = append(shape.members, member)
		}
		return shape, nil
	case reflect.Map:
		shape := &jsonShape{kind: shapeMap, typ: t, key: jsonKeyRuleOf(t.Key())}
		building[t] = shape
		elem, err := buildJSONShape(t.Elem(), building)
		if err != nil {
			return nil, err
		}
		shape.elem = elem
		return shape, nil
	case reflect.Slice, reflect.Array:
		shape := &jsonShape{kind: shapeList, typ: t, length: -1}
		if t.Kind() == reflect.Array {
			shape.length = t.Len()
		}
		building[t] = shape
		elem, err := buildJSONShape(t.Elem(), building)
		if err != nil {
			return nil, err
		}
		shape.elem = elem
		return shape, nil
	}
	return opaqueShape, nil
}

// CheckDecodable refuses a document type encoding/json cannot decode every
// member it writes into: one holding, at any depth, a struct whose member is
// promoted through an unexported embedded pointer (core.Field.CheckWritable).
// encoding/json writes that member while the pointer is set, and decoding
// into a fresh value, where the pointer is nil, fails on it. Self-decoding
// subtrees are opaque, as Validate reads them.
func CheckDecodable(t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
	default:
		// A scalar or an interface holds no member; jsonShapeOf would take
		// its lock to find the same.
		return nil
	}
	shape, err := jsonShapeOf(t)
	if err != nil {
		return err
	}
	if shape.kind == shapeOpaque {
		return nil
	}
	return shape.checkDecodable(map[*jsonShape]bool{})
}

func (s *jsonShape) checkDecodable(seen map[*jsonShape]bool) error {
	if seen[s] {
		return nil
	}
	seen[s] = true
	switch s.kind {
	case shapeMap, shapeList:
		return s.elem.checkDecodable(seen)
	case shapeStruct:
		i := -1
		for f := range s.walk.Fields() {
			i++
			if f.Hidden() {
				continue // encoding/json decodes no member into it
			}
			err := f.CheckWritable()
			if err == nil {
				err = s.members[i].checkDecodable(seen)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", gowalker.TypeText(s.typ), err)
			}
		}
	}
	return nil
}

var (
	jsonUnmarshalerType   = reflect.TypeFor[json.Unmarshaler]()
	jsonMarshalerToType   = reflect.TypeFor[jsonv2.MarshalerTo]()
	jsonUnmarshalFromType = reflect.TypeFor[jsonv2.UnmarshalerFrom]()
)

// decodesItself reports a type with its own JSON methods, on its value or
// its pointer: what it holds is its own business.
func decodesItself(t reflect.Type) bool {
	for _, m := range []reflect.Type{jsonMarshalerType, jsonUnmarshalerType, jsonMarshalerToType, jsonUnmarshalFromType} {
		if t.Implements(m) || t.Kind() != reflect.Interface && reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// keyValidator is one validation's scratch state, pooled so a validation
// allocates nothing per member.
type keyValidator struct {
	reader bytes.Reader
	dec    *jsontext.Decoder
	// name holds the member name just read when it had to be unquoted.
	name []byte
	// seen marks the struct members met in each open struct object, a
	// stack.
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
func (v *keyValidator) value(shape *jsonShape) error {
	kind := v.dec.PeekKind()
	switch {
	case (shape.kind == shapeStruct || shape.kind == shapeMap) && kind == '{':
		return v.object(shape)
	case shape.kind == shapeList && kind == '[':
		return v.list(shape)
	}
	return v.skip()
}

// object reads an object bound for a struct or map shape.
func (v *keyValidator) object(shape *jsonShape) error {
	if _, err := v.dec.ReadToken(); err != nil {
		return errInvalidJSONDocument
	}
	seenStart, spansStart, namesStart := len(v.seen), len(v.mapSpans), len(v.mapNames)
	if shape.kind == shapeStruct {
		v.seen = slices.Grow(v.seen, len(shape.members))[:seenStart+len(shape.members)]
		clear(v.seen[seenStart:])
	}
	for v.dec.PeekKind() != '}' {
		name, err := v.memberName()
		if err != nil {
			return err
		}
		if shape.kind == shapeStruct {
			err = v.structMember(shape, v.seen[seenStart:seenStart+len(shape.members)], name)
		} else {
			err = v.mapMember(shape, name)
		}
		if err != nil {
			return err
		}
	}
	if _, err := v.dec.ReadToken(); err != nil {
		return errInvalidJSONDocument
	}
	if shape.kind == shapeMap {
		if err := v.repeatedMapMember(shape, v.mapSpans[spansStart:]); err != nil {
			return err
		}
	}
	v.seen, v.mapSpans, v.mapNames = v.seen[:seenStart], v.mapSpans[:spansStart], v.mapNames[:namesStart]
	return nil
}

// memberName reads a member name, valid until the next read.
func (v *keyValidator) memberName() ([]byte, error) {
	raw, err := v.dec.ReadValue()
	if err != nil || len(raw) < 2 {
		return nil, errInvalidJSONDocument
	}
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw[1 : len(raw)-1], nil
	}
	// The decoder has read the name, so the only error left is invalid
	// UTF-8, which it accepts and decoding replaces, as here.
	v.name, _ = jsontext.AppendUnquote(v.name[:0], raw)
	return v.name, nil
}

// structMember reads the member name of a struct object; seen marks the
// object's members met so far.
func (v *keyValidator) structMember(shape *jsonShape, seen []bool, name []byte) error {
	i, ok := shape.byName[string(name)]
	if !ok {
		// Decoding gives a member no field names exactly to a field its name
		// matches by case, if one does.
		if f, folded := shape.walk.FoldedMatch(name); folded {
			return fmt.Errorf("JSON member %q is noncanonical for %s; use %q", v.dec.StackPointer(), gowalker.TypeText(shape.typ), f.External())
		}
		return v.skip() // not a member of the type
	}
	if seen[i] {
		return fmt.Errorf("duplicate JSON member %q in %s", v.dec.StackPointer(), gowalker.TypeText(shape.typ))
	}
	seen[i] = true
	return v.value(shape.members[i])
}

func (v *keyValidator) mapMember(shape *jsonShape, name []byte) error {
	if !canonicalKeyName(shape, name) {
		return fmt.Errorf("JSON map member %q does not preserve its decoded key spelling in %s; use canonical JSON member names", v.dec.StackPointer(), gowalker.TypeText(shape.typ))
	}
	start := len(v.mapNames)
	v.mapNames = append(v.mapNames, name...)
	v.mapSpans = append(v.mapSpans, nameSpan{start, len(v.mapNames)})
	return v.value(shape.elem)
}

// repeatedMapMember refuses a name a map object gave twice, at its end:
// every name is canonical, so two members name one key exactly when their
// names are equal.
func (v *keyValidator) repeatedMapMember(shape *jsonShape, spans []nameSpan) error {
	names := v.mapNames
	slices.SortFunc(spans, func(a, b nameSpan) int { return bytes.Compare(names[a.start:a.end], names[b.start:b.end]) })
	for i := 1; i < len(spans); i++ {
		if name := names[spans[i].start:spans[i].end]; bytes.Equal(name, names[spans[i-1].start:spans[i-1].end]) {
			return fmt.Errorf("duplicate JSON map member %q in %s", string(v.dec.StackPointer())+"/"+core.EscapePointerToken(string(name)), gowalker.TypeText(shape.typ))
		}
	}
	return nil
}

// canonicalKeyName reports name as encoding/json writes the key it decodes
// to.
func canonicalKeyName(shape *jsonShape, name []byte) bool {
	switch shape.key {
	case keyAsIs:
		return true
	case keySigned:
		if len(name) > 1 && name[0] == '-' {
			return name[1] != '0' && canonicalIntegerText(name[1:])
		}
		return canonicalIntegerText(name)
	case keyUnsigned:
		return canonicalIntegerText(name)
	}
	_, canonical, err := CanonicalMapKey(shape.typ.Key(), string(name))
	return err == nil && canonical == string(name)
}

// canonicalIntegerText reports base-10 digits without a leading zero, or
// "0".
func canonicalIntegerText(s []byte) bool {
	if len(s) == 0 || s[0] == '0' && len(s) > 1 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (v *keyValidator) list(shape *jsonShape) error {
	if _, err := v.dec.ReadToken(); err != nil {
		return errInvalidJSONDocument
	}
	for i := 0; v.dec.PeekKind() != ']'; i++ {
		var err error
		if shape.length >= 0 && i >= shape.length {
			err = v.skip() // decoding drops elements past an array's end
		} else {
			err = v.value(shape.elem)
		}
		if err != nil {
			return err
		}
	}
	if _, err := v.dec.ReadToken(); err != nil {
		return errInvalidJSONDocument
	}
	return nil
}

func (v *keyValidator) skip() error {
	if err := v.dec.SkipValue(); err != nil {
		return errInvalidJSONDocument
	}
	return nil
}

var (
	rawJSONType       = reflect.TypeFor[json.RawMessage]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
)
