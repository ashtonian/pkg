package core

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

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
	gowalker.MustNew(gowalker.WithJSONFields(JSONTagKey), gowalker.DropTies(), gowalker.WithUnboundedCache()),
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

// foldName appends name with each rune replaced by the least rune of its
// Unicode simple case folding orbit, so two names fold alike exactly when
// strings.EqualFold reports them equal: the match encoding/json falls back
// to when no field has a member's exact name.
func foldName(dst, name []byte) []byte {
	for len(name) > 0 {
		r, size := utf8.DecodeRune(name)
		name = name[size:]
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		dst = utf8.AppendRune(dst, least)
	}
	return dst
}
