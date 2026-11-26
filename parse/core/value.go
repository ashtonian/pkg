package core

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/ashtonian/pkg/gowalker"
)

// ErrInvalidText is the error request text is refused with when it holds
// U+0000 or is not valid UTF-8.
var ErrInvalidText = errors.New("invalid text")

// CheckText refuses s when it holds U+0000 or is not valid UTF-8, wrapping
// ErrInvalidText. PostgreSQL refuses both in text and jsonb parameters, and
// SQLite stores them as sent and ends a LIKE or GLOB pattern at U+0000, so
// a request value holding either would fail at execution on one backend and
// match differently on another. Request values are checked where they are
// read; values the server stored are not.
func CheckText(s string) error {
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("%w: it holds U+0000, which no backend compares as written", ErrInvalidText)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: it is not valid UTF-8", ErrInvalidText)
	}
	return nil
}

// CheckValueText is CheckText for every string of a normalized request
// value: the value itself, a list's or a key's elements, and a document's
// member names and members, at any depth. The error names where the string
// stands. Values of other types hold no request text a backend binds as
// written.
func CheckValueText(v any) error {
	switch x := v.(type) {
	case string:
		return CheckText(x)
	case []any:
		return checkElementsText(x)
	case Key:
		return checkElementsText(x)
	case map[string]any:
		for k, e := range x {
			err := CheckText(k)
			if err != nil {
				return fmt.Errorf("member name %q: %w", k, err)
			}
			err = CheckValueText(e)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	return nil
}

func checkElementsText(elements []any) error {
	for i, e := range elements {
		err := CheckValueText(e)
		if err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

// NormalizeScalar validates a decoded request value against a field's data
// type and returns the canonical Go value filter ASTs and patch operations
// carry:
//
//   - FieldTypeInt       → int64 (signed kinds) or uint64 (unsigned), range-checked against kind
//   - FieldTypeFloat     → finite float64, range-checked against kind
//   - FieldTypeDecimal   → canonical decimal text (DecimalText)
//   - FieldTypeString    → string (encoding.TextMarshaler accepted)
//   - FieldTypeUUID      → canonical lowercase UUID string
//   - FieldTypeBool      → bool
//   - FieldTypeTimestamp → RFC 3339 UTC string with nanosecond precision, of fixed width (TimestampLayout); a UTC year outside 0001 to 9999 is an error (time.Time accepted)
//   - FieldTypeDate      → YYYY-MM-DD (Date accepted, and a time.Time at
//     midnight, as a driver scans a SQL DATE)
//   - FieldTypeTime, FieldTypeInterval, FieldTypeInet, FieldTypeCIDR,
//     FieldTypeMACAddr   → the canonical text of LocalTime, Interval, Inet,
//     CIDR and MACAddr (each type accepted, and netip.Addr, netip.Prefix
//     and net.HardwareAddr where they hold the value)
//
// Other data types pass through unchanged. Non-nil pointers are
// dereferenced first, so typed Go input (*int64, *time.Time) normalizes like
// its value, and a value of a named string, integer, float or bool type
// normalizes as its kind's value does. A nil value is an error; callers decide separately whether a
// field admits null.
func NormalizeScalar(dt FieldType, kind reflect.Kind, v any) (any, error) {
	v = Deref(v)
	if v == nil {
		switch dt {
		case FieldTypeString, FieldTypeUUID, FieldTypeInt, FieldTypeFloat, FieldTypeDecimal, FieldTypeBool, FieldTypeTimestamp, FieldTypeDate,
			FieldTypeTime, FieldTypeInterval, FieldTypeInet, FieldTypeCIDR, FieldTypeMACAddr:
			return nil, fmt.Errorf("expected %s, got null", dt)
		}
		return nil, nil
	}
	switch dt {
	case FieldTypeString:
		s, ok := text(v)
		if !ok {
			return nil, fmt.Errorf("expected string, got %s", gowalker.TypeText(reflect.TypeOf(v)))
		}
		return s, nil
	case FieldTypeUUID:
		s, ok := text(v)
		if !ok {
			return nil, fmt.Errorf("expected UUID string, got %s", gowalker.TypeText(reflect.TypeOf(v)))
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
	case FieldTypeDecimal:
		return DecimalText(v)
	case FieldTypeBool:
		b, ok := boolean(v)
		if !ok {
			return nil, fmt.Errorf("expected boolean, got %s", gowalker.TypeText(reflect.TypeOf(v)))
		}
		return b, nil
	case FieldTypeDate:
		if date, ok := v.(Date); ok {
			parsed, err := ParseDate(string(date))
			return string(parsed), err
		}
		if text, ok := v.(string); ok {
			date, err := ParseDate(text)
			return string(date), err
		}
		if t, ok := v.(time.Time); ok {
			date, err := dateOf(t)
			return string(date), err
		}
		return nil, fmt.Errorf("expected civil date string, got %s", gowalker.TypeText(reflect.TypeOf(v)))
	case FieldTypeTimestamp:
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		if y := t.UTC().Year(); y < minTimestampYear || y > maxTimestampYear {
			return nil, fmt.Errorf("timestamp %s is outside the years 0001 to 9999 in UTC", t.Format(time.RFC3339Nano))
		}
		return FormatTimestamp(t), nil
	case FieldTypeTime:
		return normalizeLocalTime(v)
	case FieldTypeInterval:
		return normalizeInterval(v)
	case FieldTypeInet:
		return normalizeInet(v)
	case FieldTypeCIDR:
		return normalizeCIDR(v)
	case FieldTypeMACAddr:
		return normalizeMACAddr(v)
	}
	return v, nil
}

// NormalizeStored is NormalizeScalar for v, a value a column of type dt
// holds rather than a request value: a float column's NaN or infinity is
// kept, as a float64, where NormalizeScalar refuses one. A request value
// is finite; a stored one need not be, since a table written outside parse
// may hold NaN and both infinities (PostgreSQL's float8; SQLite stores the
// infinities and holds NaN as NULL). Rows Evaluate reads and the values a
// cursor carries normalize through it.
func NormalizeStored(dt FieldType, kind reflect.Kind, v any) (any, error) {
	rv := reflect.ValueOf(Deref(v))
	isFloat := rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64
	if dt == FieldTypeFloat && isFloat {
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return f, nil
		}
	}
	return NormalizeScalar(dt, kind, v)
}

// TimestampLayout is the canonical timestamp text: RFC 3339 in UTC with
// exactly nine fractional digits. The width is fixed, so where timestamps
// are stored and compared as text (SQLite), text order is time order:
// time.RFC3339Nano trims trailing zeros, which sorts `…05.5Z` before
// `…05Z`. A timestamp's UTC year is 0001 to 9999, so its year has four
// digits too.
const TimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// The years a timestamp's UTC instant may fall in.
const (
	minTimestampYear = 1
	maxTimestampYear = 9999
)

// FormatTimestamp renders t in the canonical timestamp form
// (TimestampLayout). Its width is fixed for a UTC year of 0001 to 9999,
// the instants NormalizeScalar accepts.
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
		if !rfc3339Text(t) {
			return time.Time{}, errInvalidTimestamp
		}
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, errInvalidTimestamp
		}
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("expected timestamp string, got %s", gowalker.TypeText(reflect.TypeOf(v)))
}

var errInvalidTimestamp = errors.New("invalid timestamp, expected RFC 3339")

// rfc3339Text reports s in RFC 3339's date-time form: YYYY-MM-DDTHH:MM:SS,
// a fraction of one to nine digits after a dot, then Z or an offset ±HH:MM
// within ±23:59. time.Parse, which checks each field's range after this,
// also takes a comma fraction, a one-digit hour, a tenth fractional digit it
// drops and an offset of +24:00 or with a minute of 60.
func rfc3339Text(s string) bool {
	const shape = "dddd-dd-ddTdd:dd:dd"
	if len(s) <= len(shape) {
		return false
	}
	for i := range len(shape) {
		if shape[i] == 'd' && !isDigit(s[i]) || shape[i] != 'd' && s[i] != shape[i] {
			return false
		}
	}
	rest := s[len(shape):]
	if rest[0] == '.' {
		n := 1
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		if n == 1 || n > 10 {
			return false
		}
		rest = rest[n:]
	}
	if rest == "Z" {
		return true
	}
	return len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':' &&
		isDigit(rest[1]) && isDigit(rest[2]) && isDigit(rest[4]) && isDigit(rest[5]) &&
		rest[1:3] <= "23" && rest[4:6] <= "59"
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

// boolean returns v as a bool when it is one, named bool types included, as
// text and nativeInt read named strings and integers.
func boolean(v any) (bool, bool) {
	b, ok := v.(bool)
	if ok {
		return b, true
	}
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Kind() == reflect.Bool {
		return rv.Bool(), true
	}
	return false, false
}
