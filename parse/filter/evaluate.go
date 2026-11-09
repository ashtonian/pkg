package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

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
//   - arrays as []any of their elements, each in its element type's domain.
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
// column, named by the expression the schema holds — a field's SQLExpr, or
// for a path its document's Path.Column, table-qualified as generated
// schemas qualify them (records.name) — and false when the row has no such
// column. A missing column is an error, so a misspelt column cannot read as
// NULL. Columns read through ColumnValue, paths through PathValue.
type ColumnRow func(column string) (value any, ok bool)

// Value reads f from the row's columns.
func (r ColumnRow) Value(f *FieldSchema) (any, bool, error) {
	column := f.SQLExpr
	if f.Path != nil {
		column = f.Path.Column
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
			return nil, false, fmt.Errorf("field %s: an array column holds a slice or a JSON array, got %T", f.ExternalName, v)
		}
		out := make([]any, len(items))
		for i, item := range items {
			e, present, err := ColumnValue(&FieldSchema{ExternalName: f.ExternalName, DataType: f.Elem, NumberKind: f.NumberKind}, item)
			if err != nil {
				return nil, false, err
			}
			if !present {
				return nil, false, fmt.Errorf("field %s: element %d is NULL", f.ExternalName, i)
			}
			out[i] = e
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
//   - Numbers and decimals compare by exact value; strings, UUIDs,
//     timestamps and dates by code point; booleans as false < true.
//   - Documents compare as jsonb does (JSONEqual, JSONContains, JSONHasKey);
//     an array $contains every listed element; $like and $ilike match as
//     MatchLike; $some / $none test a document's elements, or a scalar
//     array's (an array column's, or an array path's elements of its type).
//   - A membership or raw-SQL predicate is Unknown: it never admits a row its
//     SQL would refuse. Resolve memberships and relation predicates first
//     (ResolveMemberships); an unresolved relation predicate is an error.
//   - A geo operator is an error (E2115).
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
	case OpRawSQL, OpMember:
		return Unknown, nil
	case OpNear, OpWithin, OpIntersects:
		return False, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeGeoNotSupported.String(),
			Message: fmt.Sprintf("geo operator '%s' is not supported by the reference evaluator", n.Op),
		}
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
	elems, _ := v.([]any)
	found := false
	if present {
		for _, e := range elems {
			if len(n.Children) == 0 {
				found = true
				break
			}
			t, err := evaluate(n.Children[0], elementRow{e})
			if err != nil {
				return False, err
			}
			if t == True {
				found = true
				break
			}
		}
	}
	return truthOf(found == (n.Op == OpSome)), nil
}

// elementRow is one element of an array. A scalar array's element is the
// value of its element field (FieldSchema.Element); a JSON array document's
// fields are element paths, and only an object element has members.
type elementRow struct{ elem any }

func (r elementRow) Value(f *FieldSchema) (any, bool, error) {
	if isElement(f) {
		return r.elem, r.elem != nil, nil
	}
	if f.Path == nil || f.Path.Column != elementColumn {
		return nil, false, fmt.Errorf("field %s is not an element path", f.ExternalName)
	}
	if _, ok := r.elem.(map[string]any); !ok {
		return nil, false, nil
	}
	v, present, err := decodedPathValue(r.elem, f.Path.Segments, f.DataType, f.Elem)
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
			return False, fmt.Errorf("field %s: $%s compares strings, got %T and %T", f.ExternalName, op, v, lit)
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
		return False, fmt.Errorf("field %s: $%s requires an array value, got %T", f.ExternalName, op, lit)
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
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeTimestamp, core.FieldTypeDate:
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
	return 0, fmt.Errorf("field %s: cannot compare %s value %T with %T", f.ExternalName, t, v, lit)
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
		return truthOf(JSONEqual(doc, lit)), nil
	case OpNe:
		return truthOf(!JSONEqual(doc, lit)), nil
	case OpContains:
		return truthOf(JSONContains(doc, lit)), nil
	case OpHasKey:
		key, ok := lit.(string)
		if !ok {
			return False, fmt.Errorf("field %s: $has_key takes a string, got %T", f.ExternalName, lit)
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
		return False, fmt.Errorf("field %s: an array value is []any, got %T", f.ExternalName, v)
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
