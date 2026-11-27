package core

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/internal/shapes"
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
		return nil, shapes.WalkError(t, err)
	}
	return fields, nil
}

// jsonFields keeps every struct type JSONFieldsOf has walked: types are
// finite and never unloaded.
var jsonFields = gowalker.NewTypeCache(
	shapes.NewJSONEngine(nil),
	func(s *gowalker.Schema) (*StructFields, error) {
		out := &StructFields{typ: s.Type(), external: map[string]*Field{}, view: s.Root()}
		for _, d := range s.Root().Declarations() {
			if d.State != gowalker.Named {
				continue
			}
			_, options, _ := strings.Cut(d.GoField.Tag.Get(JSONTagKey), ",")
			f := &Field{goName: d.GoField.Name, typ: d.GoField.Type, route: d.Index, external: d.Field.Name(), tags: Tags{FieldNameOptions: options}, unwritable: shapes.CheckWritable(d.Field)}
			out.fields = append(out.fields, f)
			out.external[f.external] = f
		}
		return out, nil
	},
)
