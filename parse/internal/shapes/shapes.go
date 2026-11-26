// Package shapes holds the gowalker engines, caches and checks parse's
// packages share: encoding/json's member naming, the compiled shape of the
// documents a type decodes from, the refusal of a field a write cannot
// reach, and the wording of a walk's errors. core wraps them in its own
// types, so core's exported API names no gowalker type and a gowalker
// change is no break for core's callers.
package shapes

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// JSONTagKey is the struct tag encoding/json names members by.
const JSONTagKey = "json"

// JSONMarshalers and JSONUnmarshalers are the interfaces by which a type
// encodes or decodes itself as JSON, either encoding/json version's, as
// arrays so a check allocates nothing.
func JSONMarshalers() [2]reflect.Type {
	marshalers := [2]reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[jsonv2.MarshalerTo]()}
	return marshalers
}

// JSONUnmarshalers is JSONMarshalers' decoding half.
func JSONUnmarshalers() [2]reflect.Type {
	unmarshalers := [2]reflect.Type{reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[jsonv2.UnmarshalerFrom]()}
	return unmarshalers
}

// NewJSONEngine names members as encoding/json does under the json tag,
// dropping a name two fields tie for as encoding/json drops it, and keeps
// every type it compiles: types are finite and never unloaded. tagOptions
// and options add how deep its views go and which types are leaves. Two
// engines share this naming because they answer different questions: a
// struct's field list, which still names the fields of a type that codes
// itself (a list response masks them), and Document, how a document is
// read, which stops at that type.
func NewJSONEngine(tagOptions []gowalker.TagOption, options ...gowalker.Option) *gowalker.Engine {
	naming := []gowalker.Option{gowalker.WithJSONFields(JSONTagKey, tagOptions...), gowalker.DropTies(), gowalker.WithUnboundedCache()}
	return gowalker.MustNew(append(naming, options...)...)
}

// Document is the shape of the JSON documents a value of type t decodes
// from, compiled once per type. Its structs name their members as
// NewJSONEngine does, and every struct, map value, element and pointer
// target it reaches has its own shape; a map's Key carries encoding/json's
// rule for its member names (gowalker's Shape.JSONKey). A type that codes
// itself as JSON, json.RawMessage included, is a leaf: what its document
// holds is its own business. A struct that embeds two such types, whose
// methods collide, inherits neither and is no leaf; encoding/json writes
// it by its fields, the embedded types' promoted. A tag reflect cannot read
// whole, on any struct the shape reaches, is refused, and the refusal is
// kept as the shape is.
func Document(t reflect.Type) (*gowalker.Shape, error) {
	shape, err := documents.Get(t)
	if err != nil {
		return nil, WalkError(t, err)
	}
	return shape, nil
}

// documents keeps every type Document has compiled.
var documents = gowalker.NewTypeCache( //nolint:gochecknoglobals // the process-wide compiled graph of every document type
	NewJSONEngine(
		[]gowalker.TagOption{gowalker.NestedFields()},
		gowalker.WithAtomicImplementers(documentLeaves()...),
	),
	func(s *gowalker.Schema) (*gowalker.Shape, error) { return s.Root(), nil },
)

// documentLeaves are the interfaces of the types Document stops at.
func documentLeaves() []reflect.Type {
	marshalers, unmarshalers := JSONMarshalers(), JSONUnmarshalers()
	return append(marshalers[:], unmarshalers[:]...)
}

// CheckWritable refuses a field promoted through an unexported embedded
// pointer (gowalker's Field.UnallocatableParent). Reflection can neither
// allocate that pointer nor point it at a copy of its parent, so neither a
// reader filling a fresh value, where the pointer is nil, nor a writer that
// must not write the parent it shares with the caller, as a patch must
// not, can set the field.
func CheckWritable(f *gowalker.Field) error {
	parent, ok := f.UnallocatableParent()
	if !ok {
		return nil
	}
	return fmt.Errorf("field %s: a write reaches it through the unexported embedded %s, which reflection can neither allocate nor copy; export the type or embed it by value", f.GoField().Name, gowalker.TypeText(parent.Type))
}

// WalkError is err from walking type t, a name conflict's fields named.
func WalkError(t reflect.Type, err error) error {
	conflict, ok := errors.AsType[*gowalker.NameConflict](err)
	if ok {
		first, second := promotedName(conflict.First), promotedName(conflict.Second)
		return fmt.Errorf("%s: fields %s and %s are both named %q at one depth, so encoding/json writes neither; rename one", gowalker.TypeText(t), first, second, conflict.Name)
	}
	return fmt.Errorf("%s: %w", gowalker.TypeText(t), err)
}

// promotedName is a field's Go name behind the embedded fields it is
// promoted through, so two promoted ID fields read as Audit.ID and Base.ID.
func promotedName(f *gowalker.Field) string {
	var b strings.Builder
	for _, parent := range f.InlineParents() {
		b.WriteString(parent.Name)
		b.WriteByte('.')
	}
	b.WriteString(f.GoField().Name)
	return b.String()
}
