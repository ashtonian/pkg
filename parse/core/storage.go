package core

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
)

// StorageValue is v, a Go value of a column of type t, as a row stores it:
// the one form every value a row holds is written in. SQLite's dialect
// binds it (filter.SQLiteDialect.MarshalValue), and a provider that holds
// rows itself stores it. It is a driver.Value: nil, int64, float64, bool,
// []byte, string or time.Time.
//
//   - nil and a nil pointer are SQL NULL, as a driver writes them.
//   - A date and a timestamp are their canonical text (StoredScalar): a
//     Valuer read as the value it writes, a timestamp UTC at a fixed width
//     (TimestampLayout), so text order is time order.
//   - A driver.Valuer, unless the column declares JSON storage, is the
//     value it writes, NULL for nil.
//   - A document (json, object), a geo shape (a point a JSONB column
//     holds, filter's geo tag) and an array are their JSON text, a list
//     of timestamps with each element its canonical text. A nil array is
//     NULL, as a driver writes a nil slice to a native array column, where
//     JSON null would be no array a read takes (filter.ColumnValue); a
//     document's nil map or slice is the JSON null encoding/json writes.
//   - Anything else is its driver.Value (driver.DefaultParameterConverter):
//     pointers followed, an integer as an int64 (an unsigned one above
//     math.MaxInt64 is an error), a float as a float64, a named string as
//     its string.
func StorageValue(t TypeInfo, v any) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	switch t.Type {
	case FieldTypeDate, FieldTypeTimestamp:
		text, _, err := StoredScalar(t.Type, reflect.Invalid, v)
		return text, err
	}
	_, valuer := v.(driver.Valuer)
	if valuer && !t.SQLType.IsJSON() {
		return driver.DefaultParameterConverter.ConvertValue(v)
	}
	switch t.Type {
	case FieldTypeJSON, FieldTypeArray, FieldTypeObject, FieldTypeGeo:
		held, err := Deref(v)
		if err != nil || held == nil {
			return nil, err
		}
		if t.Type == FieldTypeArray && nilSlice(held) {
			return nil, nil
		}
		if t.Type == FieldTypeArray && t.Elem == FieldTypeTimestamp {
			v, err = timestampTexts(v)
			if err != nil {
				return nil, err
			}
		}
		text, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return string(text), nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

// nilSlice reports a nil slice.
func nilSlice(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice && rv.IsNil()
}

// timestampTexts is a list of timestamps, or a pointer to one, with each
// element its canonical text: json.Marshal writes a time.Time in its own
// offset, and an element reads as a timestamp only in UTC. A value that is
// no list is unchanged.
func timestampTexts(value any) (any, error) {
	list, err := Deref(value)
	if err != nil {
		return nil, err
	}
	items, ok := AnySlice(list)
	if !ok {
		return value, nil
	}
	out := make([]any, len(items))
	for i, item := range items {
		text, _, err := StoredScalar(FieldTypeTimestamp, reflect.Invalid, item)
		if err != nil {
			return nil, fmt.Errorf("timestamp element %d: %w", i, err)
		}
		out[i] = text
	}
	return out, nil
}
