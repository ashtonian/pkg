package filter

import (
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
)

// numberKeyer is a dialect that renders an int, float or decimal field's
// value as its terms facet key: the canonical decimal text
// (core.DecimalText) FacetKeys gives the same value.
type numberKeyer interface {
	numberKey(f *FieldSchema) string
}

// numberKey reads a path member and a decimal as the numeric FieldExpr
// renders, exactly; trim_scale drops the scale a literal was written with
// (7.0, 1.50). A float column's key is its shortest text at the field's
// precision, which float8 and float4 output give while extra_float_digits
// is positive (PostgreSQL's default), read as numeric for plain notation.
// A non-finite float has none.
func (d PostgresDialect) numberKey(f *FieldSchema) string {
	expr := FieldExpr(d, f)
	switch {
	case f.Path != nil || f.DataType == core.FieldTypeDecimal:
		return "trim_scale(" + expr + ")::text"
	case f.DataType == core.FieldTypeInt:
		return "(" + expr + ")::text"
	}
	text := "(" + expr + ")::float8::text"
	if f.NumberKind == reflect.Float32 {
		text = "(" + expr + ")::float4::text"
	}
	return "(CASE WHEN " + text + " IN ('NaN', 'Infinity', '-Infinity') THEN NULL ELSE " + text + "::numeric::text END)"
}

// numberKey reads a number member's JSON literal as written through
// parse_decimal, which json_extract would round to a double, and a REAL
// column through parse_decimal (a double) or parse_decimal_float32 (a
// float32 field's). A decimal's FieldExpr is already its canonical text.
func (d SQLiteDialect) numberKey(f *FieldSchema) string {
	switch {
	case f.DataType == core.FieldTypeDecimal:
		return FieldExpr(d, f)
	case f.Path != nil:
		kind, _, literal := sqlitePathMember(f.Path)
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + decimals.SQLiteValueFunc + "(" + literal + ") END)"
	case f.DataType == core.FieldTypeInt:
		return "CAST(" + f.SQLExpr + " AS TEXT)"
	case f.NumberKind == reflect.Float32:
		return decimals.SQLiteFloat32Func + "(" + f.SQLExpr + ")"
	}
	return decimals.SQLiteValueFunc + "(" + f.SQLExpr + ")"
}

// FacetKeys returns the terms buckets a value of f counts in, keyed as
// BuildFacetQuery keys them, for a backend that counts in Go: none for NULL
// or an absent member, one for a scalar, and each distinct element of a
// list once. v is f's value, or for a JSON path its column's document, read
// as PathValue reads one.
//
// A key is the value's canonical text: a string as itself, a boolean as
// true or false, a number as its canonical decimal text (core.DecimalText)
// — a path member's exactly as written, a float32 field's at float32
// precision — a date as YYYY-MM-DD, a timestamp as core.FormatTimestamp and
// a UUID lower-cased. A non-finite float has none. BuildFacetQuery keys a
// timestamp column by the database's own text of it.
func FacetKeys(v any, f *FieldSchema) ([]string, error) {
	if f.Path != nil {
		return pathFacetKeys(v, f)
	}
	if f.DataType != core.FieldTypeArray {
		key, ok, err := valueFacetKey(v, f.DataType, f.NumberKind)
		if err != nil || !ok {
			return nil, facetKeyError(f, err)
		}
		return []string{key}, nil
	}
	v, ok := deref(v)
	if !ok {
		return nil, nil
	}
	items, ok := core.AnySlice(v)
	if !ok {
		return nil, fmt.Errorf("facet %s: %T is not a list", f.ExternalName, v)
	}
	var keys distinctKeys
	for _, item := range items {
		key, ok, err := valueFacetKey(item, f.Elem, f.NumberKind)
		if err != nil {
			return nil, facetKeyError(f, err)
		}
		if ok {
			keys.add(key)
		}
	}
	return keys.list, nil
}

// pathFacetKeys is FacetKeys of a JSON path field over doc.
func pathFacetKeys(doc any, f *FieldSchema) ([]string, error) {
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", f.ExternalName, err)
	}
	member, ok := memberAt(root, f.Path.Segments)
	if !ok {
		return nil, nil
	}
	if f.DataType != core.FieldTypeArray {
		key, ok, err := memberFacetKey(member, f.DataType)
		if err != nil || !ok {
			return nil, facetKeyError(f, err)
		}
		return []string{key}, nil
	}
	items, ok := member.([]any)
	if !ok {
		return nil, nil
	}
	var keys distinctKeys
	for _, item := range items {
		key, ok, err := memberFacetKey(item, f.Elem)
		if err != nil {
			return nil, facetKeyError(f, err)
		}
		if ok {
			keys.add(key)
		}
	}
	return keys.list, nil
}

// memberFacetKey is a decoded document member's key as a path of type t
// reads it (pathMember), a number exactly as written.
func memberFacetKey(v any, t core.FieldType) (string, bool, error) {
	if t == core.FieldTypeInt || t == core.FieldTypeFloat {
		n, ok := jsonNumber(v)
		if !ok {
			return "", false, nil
		}
		text, err := core.DecimalText(n)
		return text, err == nil, nil
	}
	m, present, err := pathMember(v, t)
	if err != nil || !present {
		return "", false, err
	}
	if b, ok := m.(bool); ok {
		return strconv.FormatBool(b), true, nil
	}
	s, ok := m.(string)
	if !ok {
		return "", false, fmt.Errorf("unexpected %s member %T", t, m)
	}
	return s, true, nil
}

// valueFacetKey is a column value's key as a field of type t holds it;
// kind is its Go numeric kind (reflect.Float32 keys at float32 precision).
func valueFacetKey(v any, t core.FieldType, kind reflect.Kind) (string, bool, error) {
	v, ok, err := facetValue(v)
	if err != nil || !ok {
		return "", false, err
	}
	rv := reflect.ValueOf(v)
	switch t {
	case core.FieldTypeString:
		if rv.Kind() == reflect.String {
			return rv.String(), true, nil
		}
	case core.FieldTypeBool:
		if rv.Kind() == reflect.Bool {
			return strconv.FormatBool(rv.Bool()), true, nil
		}
	case core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeDecimal:
		return numberFacetKey(v, kind)
	case core.FieldTypeDate:
		switch d := v.(type) {
		case core.Date:
			return string(d), true, nil
		case time.Time:
			return d.Format(time.DateOnly), true, nil
		case string:
			return d, true, nil
		}
	case core.FieldTypeTimestamp:
		if ts, ok := v.(time.Time); ok {
			return core.FormatTimestamp(ts), true, nil
		}
	case core.FieldTypeUUID:
		if s, ok := textOf(v); ok {
			return strings.ToLower(s), true, nil
		}
	}
	return "", false, fmt.Errorf("%T is not a %s value", v, t)
}

// numberFacetKey is a number's canonical decimal text; a float32 field's
// value is read at float32 precision, and a non-finite float has none.
func numberFacetKey(v any, kind reflect.Kind) (string, bool, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64 {
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", false, nil
		}
		if kind == reflect.Float32 {
			v = float32(f)
		}
	}
	text, err := core.DecimalText(v)
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// deref follows pointers to the value they hold; ok is false for nil.
func deref(v any) (any, bool) {
	for {
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return nil, false
		}
		if rv.Kind() != reflect.Pointer && rv.Kind() != reflect.Interface {
			return v, true
		}
		if rv.IsNil() {
			return nil, false
		}
		v = rv.Elem().Interface()
	}
}

// facetValue dereferences a scalar and reads a driver.Valuer (sql.Null*, a
// UUID) as the value the database holds; ok is false for NULL.
func facetValue(v any) (any, bool, error) {
	v, ok := deref(v)
	if !ok {
		return nil, false, nil
	}
	switch v.(type) {
	case time.Time, core.Date, core.Decimal:
		return v, true, nil
	}
	if dv, ok := v.(driver.Valuer); ok {
		x, err := dv.Value()
		if err != nil {
			return nil, false, err
		}
		if x == nil {
			return nil, false, nil
		}
		return x, true, nil
	}
	return v, true, nil
}

// textOf is a string, or a value's text form.
func textOf(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	case fmt.Stringer:
		return x.String(), true
	}
	return "", false
}

func facetKeyError(f *FieldSchema, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("facet %s: %w", f.ExternalName, err)
}

// distinctKeys collects keys in first-seen order, each once.
type distinctKeys struct {
	list []string
	seen map[string]bool
}

func (d *distinctKeys) add(key string) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if !d.seen[key] {
		d.seen[key] = true
		d.list = append(d.list, key)
	}
}
