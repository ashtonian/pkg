package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// Truth is the value of a filter on one row under SQL's three-valued logic.
// A row is selected only when its filter is True.
type Truth uint8

const (
	False Truth = iota
	True
	Unknown
)

func (t Truth) String() string {
	switch t {
	case False:
		return "false"
	case True:
		return "true"
	case Unknown:
		return "unknown"
	}
	return fmt.Sprintf("Truth(%d)", uint8(t))
}

func truthOf(b bool) Truth {
	if b {
		return True
	}
	return False
}

// Row is one row a filter is evaluated on. Value is the value of field f in
// the canonical domain filter values are normalized to, and present false
// for SQL NULL:
//
//   - strings, UUIDs (lower-case text), timestamps (core.TimestampLayout
//     text) and dates (civil date text) as string;
//   - integers as int64, or uint64 above math.MaxInt64; floats as finite
//     float64; decimals as canonical text (core.DecimalText);
//   - booleans as bool;
//   - documents decoded from JSON (map[string]any, []any, string, bool, nil,
//     json.Number);
//   - arrays as []any of their elements, each in its element type's domain;
//   - a geo field's JSONB point as a document, or its JSON text, whose lat
//     and lon members (GeoFieldConfig's keys) hold numbers, or strings
//     holding numbers as PostgreSQL casts them to float8; a member missing,
//     null or holding anything else makes the point NULL.
//
// A path's value is what PathValue reads from its document. ColumnRow reads
// a row stored as Go values into this domain; ColumnValue converts one value.
type Row interface {
	Value(f *FieldSchema) (v any, present bool, err error)
}

// RowFunc adapts a function to Row.
type RowFunc func(f *FieldSchema) (any, bool, error)

// Value calls r.
func (r RowFunc) Value(f *FieldSchema) (any, bool, error) { return r(f) }

// ColumnRow is a Row stored by column: it returns the Go value held under a
// column, named table.column, unquoted (records.name), from the field's
// Table and Column — the document's, for a path — or by a computed field's
// Expr, and false when the row has no such column. A missing column is an error, so a misspelt column cannot read as
// NULL. Columns read through ColumnValue, paths through PathValue.
type ColumnRow func(column string) (value any, ok bool)

// rowKey is the name a ColumnRow holds a column under: table.column, or
// expr for a computed one.
func rowKey(table, column, expr string) string {
	switch {
	case expr != "":
		return expr
	case table == "":
		return column
	}
	return table + "." + column
}

// Value reads f from the row's columns.
func (r ColumnRow) Value(f *FieldSchema) (any, bool, error) {
	column := rowKey(f.Table, f.Column, f.Expr)
	if p := f.Path; p != nil {
		column = rowKey(p.Table, p.Column, p.Expr)
	}
	if column == "" {
		return nil, false, fmt.Errorf("field %s names no column", f.ExternalName)
	}
	v, ok := r(column)
	if !ok {
		return nil, false, fmt.Errorf("field %s: the row has no column %q", f.ExternalName, column)
	}
	if f.Path != nil {
		return PathValue(v, f)
	}
	return ColumnValue(f, v)
}

// ColumnValue is a stored column value v of field f in the domain Row
// describes, and false for SQL NULL: a nil pointer, interface, map or slice,
// or a driver.Valuer whose value is nil. A document is a Document's decoded
// value, or decoded from its JSON text (string, []byte, json.RawMessage) or
// encoding; an array's elements
// normalize to f.Elem; a scalar is reduced as CursorValue reduces it — so
// drivers' Valuers, decimals, times and text-marshalling types such as UUIDs
// compare as SQL compares them — then normalized to f's type.
func ColumnValue(f *FieldSchema, v any) (any, bool, error) {
	v = derefValue(v)
	if v == nil || isNilReference(v) {
		return nil, false, nil
	}
	switch {
	case core.IsDocument(f.DataType):
		doc, err := decodeDocument(v)
		if err != nil {
			return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return doc, true, nil
	case f.DataType == core.FieldTypeArray:
		items, ok := core.AnySlice(v)
		if !ok {
			items, ok = jsonArray(v)
		}
		if !ok {
			return nil, false, fmt.Errorf("field %s: an array column holds a slice or a JSON array, got %s", f.ExternalName, gowalker.TypeText(reflect.TypeOf(v)))
		}
		// A NULL element is no element, as the SQL element renderings skip
		// it: a PostgreSQL array holds NULLs whatever the Go type is.
		out := make([]any, 0, len(items))
		for _, item := range items {
			e, present, err := ColumnValue(&FieldSchema{ExternalName: f.ExternalName, DataType: f.Elem, NumberKind: f.NumberKind}, item)
			if err != nil {
				return nil, false, err
			}
			if present {
				out = append(out, e)
			}
		}
		return out, true, nil
	case f.DataType == core.FieldTypeGeo || f.DataType == core.FieldTypeAny:
		return v, true, nil
	}
	raw, err := CursorValue(v)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	if raw == nil {
		return nil, false, nil
	}
	// A Valuer may hold a number as its text.
	if text, ok := raw.(string); ok && (f.DataType == core.FieldTypeInt || f.DataType == core.FieldTypeFloat) {
		raw = json.Number(text)
	}
	scalar, err := core.NormalizeScalar(f.DataType, f.NumberKind, raw)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	return scalar, true, nil
}

// jsonArray decodes an array held as JSON text, as SQLite stores one.
func jsonArray(v any) ([]any, bool) {
	switch v.(type) {
	case string, []byte, json.RawMessage:
	default:
		return nil, false
	}
	doc, err := decodeDocument(v)
	if err != nil {
		return nil, false
	}
	arr, ok := doc.([]any)
	return arr, ok
}

func isNilReference(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

// Evaluate is the reference evaluator of a filter: the truth SQL gives root
// on row, under the rules the SQL dialects render and filtertest holds every
// backend to. A nil root is True.
//
//   - $and, $or and $not are three-valued; a comparison with a NULL value is
//     Unknown.
//   - $in: [] is False and $nin: [] True, NULL or not.
//   - A key set (PolicyConstraint.Fields under $in) holds where every
//     column equals its position in one of the keys.
//   - Numbers and decimals compare by exact value; strings, UUIDs,
//     timestamps and dates by code point; booleans as false < true.
//   - Documents compare as jsonb does (core.JSONEqual, JSONContains, JSONHasKey);
//     an array $contains every listed element; $like and $ilike match as
//     MatchLike; $some / $none test a document's elements, or a scalar
//     array's (an array column's, or an array path's elements of its type).
//   - SQL's unknown (OpUnknown) is Unknown; so is a membership or raw-SQL
//     predicate, which therefore never admits a row its SQL would refuse. Resolve memberships and relation predicates first
//     (ResolveMemberships); an unresolved relation predicate is an error.
//   - Geo operators decide a JSONB point as PostGIS does: $near by the
//     WGS 84 geodesic distance (ST_DWithin on geography), $within and
//     $intersects a box or polygon planar in lon/lat, boundary included. A
//     point on native geometry or geography storage is an error (E2115).
func Evaluate(root *FilterNode, row Row) (Truth, error) {
	if root == nil {
		return True, nil
	}
	return evaluate(root, row)
}

func evaluate(n *FilterNode, row Row) (Truth, error) {
	if n == nil {
		return False, errors.New("filter: nil AST node")
	}
	switch n.Op {
	case OpAnd, OpOr:
		return evaluateLogical(n, row)
	case OpNot:
		if len(n.Children) != 1 {
			return False, fmt.Errorf("filter: $not node has %d children, want 1", len(n.Children))
		}
		t, err := evaluate(n.Children[0], row)
		if err != nil || t == Unknown {
			return t, err
		}
		return truthOf(t == False), nil
	case OpUnknown, OpRawSQL, OpMember:
		return Unknown, nil
	case OpKeyIn:
		if n.Keys == nil {
			return False, errors.New("filter: key set node has no keys")
		}
		return evaluate(n.Keys.expand(), row)
	}
	f := n.Field
	if f == nil {
		return False, fmt.Errorf("filter: $%s condition has no field schema", n.Op)
	}
	if f.Relation != nil {
		return False, invalidRelation(f, "is evaluated outside SQL only once resolved (ResolveMemberships)")
	}
	if n.Op == OpSome || n.Op == OpNone {
		return evaluateElements(n, row)
	}
	if isGeoOp(n.Op) && f.DataType == core.FieldTypeGeo {
		return evaluateGeo(n, row)
	}
	v, present, err := row.Value(f)
	if err != nil {
		return False, err
	}
	return compareLeaf(f, n.Op, n.Value, v, present)
}

// evaluateLogical applies Kleene AND / OR: a False (AND) or True (OR) child
// decides; otherwise any Unknown child makes the node Unknown.
func evaluateLogical(n *FilterNode, row Row) (Truth, error) {
	if len(n.Children) == 0 {
		return False, fmt.Errorf("filter: %s node has no children", n.Op)
	}
	decides := False
	if n.Op == OpOr {
		decides = True
	}
	unknown := false
	for _, c := range n.Children {
		t, err := evaluate(c, row)
		if err != nil {
			return False, err
		}
		switch t {
		case decides:
			return t, nil
		case Unknown:
			unknown = true
		}
	}
	if unknown {
		return Unknown, nil
	}
	return truthOf(decides == False), nil
}

// evaluateElements is $some / $none on a JSON array document or a scalar
// array: EXISTS / NOT EXISTS of an element its child holds for. A NULL
// array, or a document that is not an array, has no elements, so the
// predicate is never Unknown.
func evaluateElements(n *FilterNode, row Row) (Truth, error) {
	f := n.Field
	if f.Elements == nil && !isScalarArray(f) {
		return False, fmt.Errorf("filter: $%s on %s: the field has no elements", n.Op, f.ExternalName)
	}
	v, present, err := row.Value(f)
	if err != nil {
		return False, err
	}
	var elems []any
	if present {
		if elems, err = arrayElements(f, v); err != nil {
			return False, err
		}
	}
	found := false
	for _, e := range elems {
		if len(n.Children) == 0 {
			found = true
			break
		}
		t, err := evaluate(n.Children[0], ElementRow{Element: e})
		if err != nil {
			return False, err
		}
		if t == True {
			found = true
			break
		}
	}
	return truthOf(found == (n.Op == OpSome)), nil
}

// arrayElements is the elements of v, f's value in Row's domain: a scalar
// array's []any, or a JSON array document's elements, none for a document
// that is not an array. Any other value is an error, so an array a Row
// holds in another form never reads as one without elements.
func arrayElements(f *FieldSchema, v any) ([]any, error) {
	if items, ok := v.([]any); ok {
		return items, nil
	}
	if f.Elements != nil && isDecodedJSON(v) {
		return nil, nil
	}
	return nil, fmt.Errorf("field %s: elements of %s: a Row holds an array as []any and a document as decoded JSON", f.ExternalName, gowalker.TypeText(reflect.TypeOf(v)))
}

// isDecodedJSON reports a value of a type decoded JSON holds: an object,
// an array, a string, a bool, nil, or a number as jsonNumber reads a
// member (json.Number, or a Go number as encoding/json writes one).
func isDecodedJSON(v any) bool {
	switch v.(type) {
	case nil, map[string]any, []any, string, bool:
		return true
	}
	return core.IsNumber(v)
}

// ElementRow is the Row a `$some` / `$none` condition is evaluated on: one
// element of its array, as Evaluate tests each. Element is an element of a
// JSON array document as DocumentElements decodes it, or of a scalar array
// as Row.Value returns the array, in its element type's domain.
//
// A scalar array's element field (FieldSchema.Element) reads Element
// itself. An element path (a field of FieldSchema.Elements) reads
// Element's member as PathValue reads a document's, and is absent on an
// element that is not an object. Element is already decoded, so a string
// element is a JSON string, never JSON text to decode. A document's element
// is decoded JSON — a map[string]any, []any, string, bool, nil or number
// (json.Number, or a Go number as encoding/json writes one) — or a Document
// (or *Document) holding it; an element path on any other Element is an
// error, as is a field that is neither.
type ElementRow struct{ Element any }

// Value reads f from the element.
func (r ElementRow) Value(f *FieldSchema) (any, bool, error) {
	if isElement(f) {
		return r.Element, r.Element != nil, nil
	}
	if !isElementPath(f) {
		return nil, false, fmt.Errorf("field %s is not an element path", f.ExternalName)
	}
	elem := r.Element
	switch d := elem.(type) {
	case Document:
		elem = d.root
	case *Document:
		elem = nil
		if d != nil {
			elem = d.root
		}
	}
	obj, ok := elem.(map[string]any)
	if !ok {
		if isDecodedJSON(elem) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("field %s: element %s is not decoded JSON", f.ExternalName, gowalker.TypeText(reflect.TypeOf(elem)))
	}
	v, present, err := decodedPathValue(obj, f.Path.Segments, f.DataType, f.Elem)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	return v, present, nil
}

// compareLeaf answers one comparison of the row's value v (present false for
// NULL) with the filter's value lit.
func compareLeaf(f *FieldSchema, op FilterOperator, lit, v any, present bool) (Truth, error) {
	// Decided before the value, so an operator the type does not define
	// fails on every row, NULL or not.
	if op == OpExists {
		return False, fmt.Errorf("field %s: $exists reaches evaluation only as the parser's $null / $not_null", f.ExternalName)
	}
	if !slices.Contains(typeOperators[f.DataType], op) {
		return False, unsupportedOperator(f, op)
	}
	switch op {
	case OpNull:
		return truthOf(!present), nil
	case OpNotNull:
		return truthOf(present), nil
	case OpIn, OpNin:
		// An empty set is decided without the value: IN () is false and
		// NOT IN () true.
		if set, ok := core.AnySlice(lit); ok && len(set) == 0 {
			return truthOf(op == OpNin), nil
		}
	}
	if !present {
		return Unknown, nil
	}
	switch {
	case core.IsDocument(f.DataType):
		return compareDocument(f, op, lit, v)
	case f.DataType == core.FieldTypeArray:
		return arrayContains(f, lit, v)
	}
	switch op {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte:
		c, err := compareScalar(f, f.DataType, v, lit)
		if err != nil {
			return False, err
		}
		return truthOf(ordered(c, op)), nil
	case OpIn, OpNin:
		return membershipTruth(f, op, lit, v)
	case OpContains, OpLike, OpILike:
		s, ok := v.(string)
		pattern, pok := lit.(string)
		if !ok || !pok {
			return False, fmt.Errorf("field %s: $%s compares strings, got %s and %s", f.ExternalName, op, gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(reflect.TypeOf(lit)))
		}
		if op == OpContains {
			return truthOf(strings.Contains(s, pattern)), nil
		}
		return truthOf(MatchLike(s, pattern, op == OpILike)), nil
	}
	return False, unsupportedOperator(f, op)
}

// membershipTruth is SQL's `v IN (…)`: True on an equal member; otherwise
// Unknown when a member is NULL, else False. NOT IN is its negation.
func membershipTruth(f *FieldSchema, op FilterOperator, lit, v any) (Truth, error) {
	set, ok := core.AnySlice(lit)
	if !ok {
		return False, fmt.Errorf("field %s: $%s requires an array value, got %s", f.ExternalName, op, gowalker.TypeText(reflect.TypeOf(lit)))
	}
	in, null := False, false
	for _, m := range set {
		if m == nil {
			null = true
			continue
		}
		c, err := compareScalar(f, f.DataType, v, m)
		if err != nil {
			return False, err
		}
		if c == 0 {
			in = True
			break
		}
	}
	if in == False && null {
		return Unknown, nil
	}
	if op == OpNin {
		return truthOf(in == False), nil
	}
	return in, nil
}

// compareScalar orders a row value against a filter value of type t, both in
// the canonical domain.
func compareScalar(f *FieldSchema, t core.FieldType, v, lit any) (int, error) {
	switch t {
	case core.FieldTypeInt, core.FieldTypeFloat:
		if c, ok := core.CompareNumbers(v, lit); ok {
			return c, nil
		}
	case core.FieldTypeDecimal:
		if c, ok := core.CompareDecimals(v, lit); ok {
			return c, nil
		}
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeTimestamp, core.FieldTypeDate,
		core.FieldTypeTime, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		a, aok := v.(string)
		b, bok := lit.(string)
		if aok && bok {
			return strings.Compare(a, b), nil
		}
	case core.FieldTypeBool:
		a, aok := v.(bool)
		b, bok := lit.(bool)
		if aok && bok {
			switch {
			case a == b:
				return 0, nil
			case b:
				return -1, nil
			}
			return 1, nil
		}
	default:
		return 0, fmt.Errorf("field %s: type %q has no scalar comparison", f.ExternalName, t)
	}
	return 0, fmt.Errorf("field %s: cannot compare %s value %s with %s", f.ExternalName, t, gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(reflect.TypeOf(lit)))
}

// ordered applies a comparison operator to c, the sign of value - literal.
func ordered(c int, op FilterOperator) bool {
	switch op {
	case OpEq:
		return c == 0
	case OpNe:
		return c != 0
	case OpGt:
		return c > 0
	case OpGte:
		return c >= 0
	case OpLt:
		return c < 0
	}
	return c <= 0
}

// compareDocument compares a document as jsonb does.
func compareDocument(f *FieldSchema, op FilterOperator, lit, doc any) (Truth, error) {
	switch op {
	case OpEq:
		return truthOf(core.JSONEqual(doc, lit)), nil
	case OpNe:
		return truthOf(!core.JSONEqual(doc, lit)), nil
	case OpContains:
		return truthOf(JSONContains(doc, lit)), nil
	case OpHasKey:
		key, ok := lit.(string)
		if !ok {
			return False, fmt.Errorf("field %s: $has_key takes a string, got %s", f.ExternalName, gowalker.TypeText(reflect.TypeOf(lit)))
		}
		return truthOf(JSONHasKey(doc, key)), nil
	}
	return False, unsupportedOperator(f, op)
}

// arrayContains is an array column's `@>`: every listed element equals one
// of the array's.
func arrayContains(f *FieldSchema, lit, v any) (Truth, error) {
	want, ok := core.AnySlice(lit)
	if !ok {
		want = []any{lit}
	}
	have, ok := v.([]any)
	if !ok {
		return False, fmt.Errorf("field %s: an array value is []any, got %s", f.ExternalName, gowalker.TypeText(reflect.TypeOf(v)))
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			c, err := compareScalar(f, f.Elem, h, w)
			if err != nil {
				return False, err
			}
			if c == 0 {
				found = true
				break
			}
		}
		if !found {
			return False, nil
		}
	}
	return True, nil
}
