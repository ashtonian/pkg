//go:build unit

package filter

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

var countField = &FieldSchema{ExternalName: "count", SQLExpr: "count", DataType: core.FieldTypeInt}

// valueRow holds count's value; nil is NULL.
func valueRow(v any) Row {
	return RowFunc(func(*FieldSchema) (any, bool, error) { return v, v != nil, nil })
}

func TestEvaluateThreeValuedLogic(t *testing.T) {
	// $nin: [] and $in: [] are decided without the value; a raw-SQL
	// predicate is unknown.
	tr := &FilterNode{Field: countField, Op: OpNin, Value: []any{}}
	fa := &FilterNode{Field: countField, Op: OpIn, Value: []any{}}
	un := &FilterNode{Op: OpRawSQL, RawSQL: "x > 1"}
	and := func(c ...*FilterNode) *FilterNode { return &FilterNode{Op: OpAnd, Children: c} }
	or := func(c ...*FilterNode) *FilterNode { return &FilterNode{Op: OpOr, Children: c} }
	not := func(c *FilterNode) *FilterNode { return &FilterNode{Op: OpNot, Children: []*FilterNode{c}} }

	tests := []struct {
		name string
		node *FilterNode
		want Truth
	}{
		{"nil root selects", nil, True},
		{"and true true", and(tr, tr), True},
		{"and true unknown", and(tr, un), Unknown},
		{"and false unknown", and(un, fa), False},
		{"or false unknown", or(fa, un), Unknown},
		{"or true unknown", or(un, tr), True},
		{"or false false", or(fa, fa), False},
		{"not true", not(tr), False},
		{"not false", not(fa), True},
		{"not unknown", not(un), Unknown},
		{"membership unresolved", &FilterNode{Field: countField, Op: OpMember}, Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.node, valueRow(int64(1)))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvaluateNullAndSets(t *testing.T) {
	tests := []struct {
		name  string
		op    FilterOperator
		lit   any
		value any
		want  Truth
	}{
		{"eq NULL", OpEq, int64(1), nil, Unknown},
		{"ne NULL", OpNe, int64(1), nil, Unknown},
		{"nin NULL", OpNin, []any{int64(1)}, nil, Unknown},
		{"in empty NULL", OpIn, []any{}, nil, False},
		{"nin empty NULL", OpNin, []any{}, nil, True},
		{"null on NULL", OpNull, nil, nil, True},
		{"not_null on NULL", OpNotNull, nil, nil, False},
		{"zero is a value", OpNull, nil, int64(0), False},
		{"in with a NULL member, absent", OpIn, []any{int64(1), nil}, int64(2), Unknown},
		{"in with a NULL member, present", OpIn, []any{int64(1), nil}, int64(1), True},
		{"nin with a NULL member, absent", OpNin, []any{int64(1), nil}, int64(2), Unknown},
		{"nin with a NULL member, present", OpNin, []any{int64(1), nil}, int64(1), False},
		{"exact above 2^53", OpEq, int64(9007199254740993), int64(9007199254740992), False},
		{"int against a float member", OpGt, int64(3), 3.5, True},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(&FilterNode{Field: countField, Op: tt.op, Value: tt.lit}, valueRow(tt.value))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvaluateRefusals(t *testing.T) {
	relation := &FieldSchema{ExternalName: "tags", DataType: core.FieldTypeString, Relation: &core.RelationSchema{}}
	doc := &FieldSchema{ExternalName: "doc", SQLExpr: "doc", DataType: core.FieldTypeJSON}
	name := &FieldSchema{ExternalName: "name", SQLExpr: "name", DataType: core.FieldTypeString}
	tests := []struct {
		name string
		node *FilterNode
		row  Row
		code core.Code // the ParseError code, when coded
	}{
		{"an undefined operator fails on a NULL row too", &FilterNode{Field: countField, Op: OpLike, Value: "a%"}, valueRow(nil), ""},
		{"unresolved $exists", &FilterNode{Field: countField, Op: OpExists, Value: true}, valueRow(nil), ""},
		{"geo", &FilterNode{Field: countField, Op: OpNear}, valueRow(nil), ECodeGeoNotSupported},
		{"unresolved relation", &FilterNode{Field: relation, Op: OpEq, Value: "x"}, valueRow(nil), ECodeInvalidRelation},
		{"elements of a document without element paths", &FilterNode{Field: doc, Op: OpSome}, valueRow(nil), ""},
		{"a row value outside the field's domain", &FilterNode{Field: name, Op: OpEq, Value: "a"}, valueRow(int64(1)), ""},
		{"and without children", &FilterNode{Op: OpAnd}, valueRow(nil), ""},
		{"not with two children", &FilterNode{Op: OpNot, Children: []*FilterNode{{Op: OpRawSQL}, {Op: OpRawSQL}}}, valueRow(nil), ""},
		{"a column the row lacks", &FilterNode{Field: name, Op: OpEq, Value: "a"}, ColumnRow(func(string) (any, bool) { return nil, false }), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Evaluate(tt.node, tt.row)
			require.Error(t, err)
			if tt.code != "" {
				pe, ok := errors.AsType[*core.ParseError](err)
				require.True(t, ok, "%v", err)
				assert.Equal(t, tt.code.String(), pe.Code)
			}
		})
	}
}

// numericText is a driver.Valuer holding a number as its text.
type numericText string

func (n numericText) Value() (any, error) { return string(n), nil }

func TestColumnValue(t *testing.T) {
	field := func(dt core.FieldType) *FieldSchema { return &FieldSchema{ExternalName: "f", DataType: dt} }
	arrayOf := func(elem core.FieldType) *FieldSchema {
		return &FieldSchema{ExternalName: "f", DataType: core.FieldTypeArray, Elem: elem}
	}
	at := time.Date(2026, 1, 2, 5, 4, 5, 0, time.FixedZone("+02", 2*3600))
	ref := "0198A3C2-7A1E-7C00-8000-000000000001"
	tests := []struct {
		name    string
		field   *FieldSchema
		value   any
		want    any
		present bool
	}{
		{"nil pointer is NULL", field(core.FieldTypeString), (*string)(nil), nil, false},
		{"invalid Valuer is NULL", field(core.FieldTypeString), sql.NullString{}, nil, false},
		{"valid Valuer", field(core.FieldTypeString), sql.NullString{String: "x", Valid: true}, "x", true},
		{"Valuer holding a number as text", field(core.FieldTypeInt), numericText("42"), int64(42), true},
		{"unsigned above int64", &FieldSchema{ExternalName: "f", DataType: core.FieldTypeInt, NumberKind: reflect.Uint64}, uint64(1 << 63), uint64(1 << 63), true},
		{"decimal drops its scale", field(core.FieldTypeDecimal), decimal.RequireFromString("10.00"), "10", true},
		{"time in UTC canonical text", field(core.FieldTypeTimestamp), at, "2026-01-02T03:04:05.000000000Z", true},
		{"UUID lower-cased", field(core.FieldTypeUUID), ref, "0198a3c2-7a1e-7c00-8000-000000000001", true},
		{"document JSON text", field(core.FieldTypeJSON), json.RawMessage(`{"n":1}`), map[string]any{"n": json.Number("1")}, true},
		{"JSON null document is a value", field(core.FieldTypeJSON), json.RawMessage(`null`), nil, true},
		{"nil document is NULL", field(core.FieldTypeJSON), json.RawMessage(nil), nil, false},
		{"array elements normalize", arrayOf(core.FieldTypeInt), []int32{1, 2}, []any{int64(1), int64(2)}, true},
		{"empty array is a value", arrayOf(core.FieldTypeString), []string{}, []any{}, true},
		{"nil array is NULL", arrayOf(core.FieldTypeString), []string(nil), nil, false},
		{"array held as JSON text", arrayOf(core.FieldTypeString), `["a","b"]`, []any{"a", "b"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present, err := ColumnValue(tt.field, tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.present, present)
			assert.Equal(t, tt.want, got)
		})
	}

	_, _, err := ColumnValue(arrayOf(core.FieldTypeString), []*string{nil})
	assert.Error(t, err, "a NULL element has no canonical value")
}

func TestEvaluateElementsOfDecodedDocuments(t *testing.T) {
	type item struct {
		Qty int64 `json:"qty" parse:"filter"`
	}
	type order struct {
		Items []item `json:"items" db:"items" parse:"filter"`
	}
	schema, err := GenerateFilterSchema[order](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := NewFilterParser(schema, core.DefaultConfig())
	some, err := parser.Parse(`{"items":{"$some":{"qty":{"$gt":2}}}}`)
	require.NoError(t, err)
	none, err := parser.Parse(`{"items":{"$none":{"qty":{"$gt":2}}}}`)
	require.NoError(t, err)

	tests := []struct {
		name       string
		doc        any // nil is NULL
		some, none Truth
	}{
		// encoding/json decodes numbers to float64 without UseNumber.
		{"Go numbers", []any{map[string]any{"qty": float64(1)}, map[string]any{"qty": float64(3)}}, True, False},
		{"json.Number", []any{map[string]any{"qty": json.Number("1")}}, False, True},
		{"scalar elements have no members", []any{float64(3)}, False, True},
		{"an object is not an array", map[string]any{"qty": float64(3)}, False, True},
		{"NULL has no elements", nil, False, True},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := RowFunc(func(*FieldSchema) (any, bool, error) { return tt.doc, tt.doc != nil, nil })
			got, err := Evaluate(some.Root, row)
			require.NoError(t, err)
			assert.Equal(t, tt.some, got, "$some")
			got, err = Evaluate(none.Root, row)
			require.NoError(t, err)
			assert.Equal(t, tt.none, got, "$none")
		})
	}
}
