package core

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
	"uuid"
)

// NormalizeScalar validates a decoded request value against a field's data
// type and returns the canonical Go value filter ASTs and patch operations
// carry:
//
//   - FieldTypeInt       → int64 (signed kinds) or uint64 (unsigned), range-checked against kind
//   - FieldTypeFloat     → finite float64, range-checked against kind
//   - FieldTypeString    → string (encoding.TextMarshaler accepted)
//   - FieldTypeUUID      → canonical lowercase UUID string
//   - FieldTypeBool      → bool
//   - FieldTypeTimestamp → RFC 3339 UTC string with nanosecond precision (time.Time accepted)
//
// Other data types pass through unchanged. Non-nil pointers are
// dereferenced first, so typed Go input (*int64, *time.Time) normalizes like
// its value. A nil value is an error; callers decide separately whether a
// field admits null.
func NormalizeScalar(dt FieldType, kind reflect.Kind, v any) (any, error) {
	v = deref(v)
	if v == nil {
		switch dt {
		case FieldTypeString, FieldTypeUUID, FieldTypeInt, FieldTypeFloat, FieldTypeBool, FieldTypeTimestamp:
			return nil, fmt.Errorf("expected %s, got null", dt)
		}
		return nil, nil
	}
	switch dt {
	case FieldTypeString:
		s, ok := text(v)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", v)
		}
		return s, nil
	case FieldTypeUUID:
		s, ok := text(v)
		if !ok {
			return nil, fmt.Errorf("expected UUID string, got %T", v)
		}
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("invalid UUID %q", s)
		}
		return id.String(), nil // canonical: lowercase, hyphenated
	case FieldTypeInt:
		if isUnsignedKind(kind) {
			return NormalizeUint(v, kind)
		}
		return NormalizeInt(v, kind)
	case FieldTypeFloat:
		return NormalizeFloat(v, kind)
	case FieldTypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean, got %T", v)
		}
		return b, nil
	case FieldTypeTimestamp:
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		return FormatTimestamp(t), nil
	}
	return v, nil
}

// TimestampLayout is the canonical timestamp text: RFC 3339 in UTC with
// exactly nine fractional digits. The width is fixed, so where timestamps
// are stored and compared as text (SQLite) text order is time order
// ; time.RFC3339Nano trims trailing zeros, which sorts
// `…05.5Z` before `…05Z`.
const TimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTimestamp renders t in the canonical timestamp form (TimestampLayout).
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}

// AnySlice returns v as []any when it is the decoder's []any or any typed
// slice or array ([]string, []int64, []uuid.UUID). Byte slices are not
// treated as lists.
func AnySlice(v any) ([]any, bool) {
	if arr, ok := v.([]any); ok {
		return arr, true
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

func toTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, errors.New("invalid timestamp, expected RFC 3339")
		}
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("expected timestamp string, got %T", v)
}

// text returns v as a string when it is one (including named string types),
// or when it marshals to text (uuid.UUID, netip.Addr, …).
func text(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number: // a JSON number is not a string, whatever its Go kind
		return "", false
	case encoding.TextMarshaler:
		b, err := t.MarshalText()
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Kind() == reflect.String {
		return rv.String(), true
	}
	return "", false
}

func deref(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	return rv.Interface()
}
