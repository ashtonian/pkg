package core

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// CompareCanonical orders a and b, two values of a column of type t, as
// the SQL dialects order the column, returning -1, 0 or +1. Each is read
// as the database holds it (StoredValue). NULL — nil, a nil pointer, a
// Valuer whose value is nil — orders after every value and equals NULL, as
// a keyset orders NULL greatest. Otherwise:
//
//   - int, float, decimal: by value, exactly, however each is held (an
//     integer of any kind, a float, a json.Number, a decimal's text or
//     type), the infinities before and after every finite value;
//   - string: by code point;
//   - uuid, timestamp, date, time, interval, inet, cidr, macaddr: by
//     canonical text (NormalizeScalar), which orders a UUID as its bytes, a
//     timestamp by its instant (TimestampLayout is fixed width), a date by
//     its day and a time of day by its time; an interval or network value,
//     which no sort orders, by its text, as a key of one locks
//     (filter.KeyOrder);
//   - bool: false before true;
//   - object, or a key that declares no type (""): as the scalar both
//     values reduce to, numbers by value, text by code point, false before
//     true.
//
// A value t cannot hold, NaN, values of an object or untyped key that
// reduce to scalars of different kinds, and a type with no order (json,
// array, geo, any) are errors: no comparison falls back to another
// reading. Evaluate, keysets, keys and patch policies order and equate
// values through it, so a filter, a page boundary, a lock order and a
// policy's allowed values agree.
func CompareCanonical(t TypeInfo, a, b any) (int, error) {
	x, xnull, err := StoredValue(a)
	if err != nil {
		return 0, err
	}
	y, ynull, err := StoredValue(b)
	if err != nil {
		return 0, err
	}
	switch {
	case xnull && ynull:
		return 0, nil
	case xnull:
		return 1, nil
	case ynull:
		return -1, nil
	}
	switch t.Type {
	case FieldTypeInt, FieldTypeFloat:
		return compareNumbers(t.Type, x, y)
	case FieldTypeDecimal:
		c, ok := CompareDecimals(x, y)
		if !ok {
			return 0, incomparable(t.Type, x, y)
		}
		return c, nil
	case FieldTypeString, FieldTypeUUID, FieldTypeTimestamp, FieldTypeDate, FieldTypeTime,
		FieldTypeInterval, FieldTypeInet, FieldTypeCIDR, FieldTypeMACAddr:
		xs, err := canonicalString(t, x)
		if err != nil {
			return 0, err
		}
		ys, err := canonicalString(t, y)
		if err != nil {
			return 0, err
		}
		return strings.Compare(xs, ys), nil
	case FieldTypeBool:
		xb, xok := boolean(x)
		yb, yok := boolean(y)
		if !xok || !yok {
			return 0, incomparable(t.Type, x, y)
		}
		return compareBools(xb, yb), nil
	case FieldTypeObject, "":
		return compareScalars(t.Type, x, y)
	}
	return 0, fmt.Errorf("a %s value has no order", t.Type)
}

// compareNumbers orders two numbers of a column of type t by value.
func compareNumbers(t FieldType, x, y any) (int, error) {
	c, ok := CompareNumbers(x, y)
	if !ok {
		return 0, incomparable(t, x, y)
	}
	return c, nil
}

// canonicalString is v normalized as t, a type whose canonical value is
// text.
func canonicalString(t TypeInfo, v any) (string, error) {
	n, err := NormalizeScalar(t.Type, t.NumberKind, v)
	if err != nil {
		return "", err
	}
	s, ok := n.(string)
	if !ok {
		return "", fmt.Errorf("%s value %s has no canonical text", t.Type, gowalker.TypeText(reflect.TypeOf(n)))
	}
	return s, nil
}

// compareScalars orders two values of a column whose type names no scalar
// by the scalars they are: both numbers, both text or both booleans.
func compareScalars(t FieldType, x, y any) (int, error) {
	if IsNumber(x) && IsNumber(y) {
		return compareNumbers(t, x, y)
	}
	xs, xok := text(x)
	ys, yok := text(y)
	if xok && yok {
		return strings.Compare(xs, ys), nil
	}
	xb, xok := boolean(x)
	yb, yok := boolean(y)
	if xok && yok {
		return compareBools(xb, yb), nil
	}
	return 0, incomparable(t, x, y)
}

func compareBools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

func incomparable(t FieldType, x, y any) error {
	if t == "" {
		return fmt.Errorf("cannot compare %s with %s", gowalker.TypeText(reflect.TypeOf(x)), gowalker.TypeText(reflect.TypeOf(y)))
	}
	return fmt.Errorf("cannot compare %s values %s and %s", t, gowalker.TypeText(reflect.TypeOf(x)), gowalker.TypeText(reflect.TypeOf(y)))
}

// CanonicalText is the canonical text of v, a value of a column of type t,
// read as one value of a key (NormalizeKey): a text type's canonical value
// (a UUID lower-case and hyphenated, a timestamp at TimestampLayout); a
// number's canonical decimal text, a float32 column's at float32
// precision, so -0 is 0; true or false. It is the text statements read
// the column as (a key's text, a terms facet's key), so it is equal for
// two values exactly when the database holds them equal; keys, facets and
// typed lookups name a value by it. NULL, NaN, the infinities and a type
// of no scalar (IsKeyType) have none.
func CanonicalText(t TypeInfo, v any) (string, error) {
	n, err := normalizeKeyValue(t, v)
	if err != nil {
		return "", err
	}
	return normalizedText(t, n)
}
