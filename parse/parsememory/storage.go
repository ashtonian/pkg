package parsememory

import (
	"database/sql/driver"
	"encoding/json"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
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
// nil, a nil pointer, map or slice is NULL, and a shape (geo) is held as its
// JSON, as a document is.
func storageValue(v any, t core.TypeInfo) (any, error) {
	v, err := core.Deref(v)
	if err != nil || v == nil || nilReference(v) {
		return nil, err
	}
	if t.Type == core.FieldTypeGeo {
		return geoText(v)
	}
	m, err := filter.SQLiteDialect{}.MarshalValue(v, t)
	if err != nil || m == nil {
		return nil, err
	}
	return driver.DefaultParameterConverter.ConvertValue(m)
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
