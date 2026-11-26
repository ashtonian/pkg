package core

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// JSONFieldsOf walks struct type t (or a pointer to one) as encoding/json
// names its members: gowalker's WithJSONFields under the json tag, with a
// name two fields tie for dropped as encoding/json drops it. These are the
// members a stored document of type t holds. No other tag is read, so a
// document type is named the same wherever it is stored and whatever tags
// other libraries put on it; a tag reflect cannot read whole is refused, as
// it is on a row. A Field carries its name, type and route, and of its Tags
// only FieldNameOptions; StructFieldsOf is the walk with parse's row rules.
// Each type is walked once.
func JSONFieldsOf(t reflect.Type) (*StructFields, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %s", t.Kind())
	}
	fields, err := jsonFields.Get(t)
	if err != nil {
		return nil, walkError(t, err)
	}
	return fields, nil
}

// jsonFields keeps every struct type JSONFieldsOf has walked: types are
// finite and never unloaded.
var jsonFields = gowalker.NewTypeCache(
	newJSONEngine(nil),
	func(s *gowalker.Schema) (*StructFields, error) {
		out := &StructFields{typ: s.Type(), external: map[string]*Field{}, view: s.Root()}
		for _, d := range s.Root().Declarations() {
			if d.State != gowalker.Named {
				continue
			}
			_, options, _ := strings.Cut(d.GoField.Tag.Get(JSONTagKey), ",")
			f := &Field{goName: d.GoField.Name, typ: d.GoField.Type, route: d.Index, external: d.Field.Name(), tags: Tags{FieldNameOptions: options}, unexportedEmbed: unexportedEmbed(out.typ, d.Index)}
			out.fields = append(out.fields, f)
			out.external[f.external] = f
		}
		return out, nil
	},
)

// JSONDocumentOf is the shape of the JSON documents a value of type t
// decodes from, compiled once per type. Its structs name their members as
// JSONFieldsOf does, and every struct, map value, element and pointer
// target it reaches has its own shape; a map's Key carries encoding/json's
// rule for its member names (gowalker's Shape.JSONKey). A type that codes
// itself as JSON (CustomJSON), json.RawMessage included, is a leaf: what
// its document holds is its own business. A struct that embeds two such
// types, whose methods collide, inherits neither and is no leaf;
// encoding/json writes it by its fields, the embedded types' promoted. A
// tag reflect cannot read whole, on any struct the shape reaches, is
// refused, and the refusal is kept as the shape is.
func JSONDocumentOf(t reflect.Type) (*gowalker.Shape, error) {
	shape, err := jsonDocuments.Get(t)
	if err != nil {
		return nil, walkError(t, err)
	}
	return shape, nil
}

// jsonDocuments keeps every type JSONDocumentOf has compiled, as jsonFields
// keeps its walks.
var jsonDocuments = gowalker.NewTypeCache( //nolint:gochecknoglobals // the process-wide compiled graph of every document type, as jsonFields is
	newJSONEngine(
		[]gowalker.TagOption{gowalker.NestedFields()},
		gowalker.WithAtomicImplementers(customJSONInterfaces()...),
	),
	func(s *gowalker.Schema) (*gowalker.Shape, error) { return s.Root(), nil },
)

// newJSONEngine names members as encoding/json does under the json tag,
// dropping a name two fields tie for as encoding/json drops it, and keeps
// every type it compiles. tagOptions and options add how deep its views
// go and which types are leaves. Two engines share this naming because
// they answer different questions: JSONFieldsOf, which members a struct
// declares, still names the fields of a type that codes itself (a list
// response masks them), while JSONDocumentOf, how a document is read,
// stops at that type.
func newJSONEngine(tagOptions []gowalker.TagOption, options ...gowalker.Option) *gowalker.Engine {
	naming := []gowalker.Option{gowalker.WithJSONFields(JSONTagKey, tagOptions...), gowalker.DropTies(), gowalker.WithUnboundedCache()}
	return gowalker.MustNew(append(naming, options...)...)
}
