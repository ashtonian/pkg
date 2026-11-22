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

// jsonEngine names a document's members as encoding/json does. It keeps every
// type it compiles, as jsonFields does: types are finite and never unloaded.
var jsonEngine = gowalker.MustNew(gowalker.WithJSONFields(JSONTagKey), gowalker.DropTies(), gowalker.WithUnboundedCache())

// jsonFields keeps every struct type JSONFieldsOf has walked.
var jsonFields = gowalker.NewTypeCache(
	jsonEngine,
	func(s *gowalker.Schema) (*StructFields, error) {
		out := &StructFields{Type: s.Type(), external: map[string]*Field{}}
		for _, d := range s.Root().Declarations() {
			if d.State != gowalker.Named {
				continue
			}
			_, options, _ := strings.Cut(d.GoField.Tag.Get(JSONTagKey), ",")
			f := &Field{GoName: d.GoField.Name, Type: d.GoField.Type, Route: d.Index, External: d.Field.Name(), Tags: Tags{FieldNameOptions: options}}
			out.Fields = append(out.Fields, f)
			out.external[f.External] = f
		}
		return out, nil
	},
)
