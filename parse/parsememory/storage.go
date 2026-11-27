package parsememory

import (
	"encoding/json"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
)

// storage is how the store holds column values, which a row scan reads
// back (backend.Storage): as SQLite binds them, a timestamp as its
// canonical text and a document or an array as its JSON text, every other
// value as its driver.Value. A row hands a timestamp over as its time.Time
// (rows), so only documents and arrays come back as text.
type storage struct{}

func (storage) Name() string { return Name }

func (storage) StoresAsText(t core.FieldType) bool {
	return core.IsDocument(t) || t == core.FieldTypeArray
}

// storageValue is v, a Go value of a column of type t, as a table holds it:
// the one storage form of a row's values (core.StorageValue), which a
// composed update's values are in too (patch.ComposedPatch.Apply). A shape
// (geo) is held as its JSON text, a nil one as NULL.
func storageValue(v any, t core.TypeInfo) (any, error) {
	if t.Type != core.FieldTypeGeo {
		return core.StorageValue(t, v)
	}
	v, err := core.Deref(v)
	if err != nil || v == nil || nilReference(v) {
		return nil, err
	}
	return geoText(v)
}

func nilReference(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

// geoText is a shape's JSON text: raw JSON and text as written, anything
// else as encoding/json writes it.
func geoText(v any) (any, error) {
	switch shape := v.(type) {
	case json.RawMessage:
		return string(shape), nil
	case string:
		return shape, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
