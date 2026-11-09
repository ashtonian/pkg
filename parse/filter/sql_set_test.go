//go:build unit

package filter

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

var (
	statusField = &FieldSchema{ExternalName: "status", SQLExpr: "items.status", DataType: core.FieldTypeString}
	colorField  = &FieldSchema{ExternalName: "attrs.color", DataType: core.FieldTypeString,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"color"}, Storage: StorageJSONB}}
)

func buildWhere(t *testing.T, d Dialect, root *FilterNode) *SQLResult {
	t.Helper()
	res, err := NewSQLBuilder(&ResourceSchema{Name: "items"}, d).Build(&FilterAST{Root: root})
	require.NoError(t, err)
	return res
}

func notNode(c *FilterNode) *FilterNode { return &FilterNode{Op: OpNot, Children: []*FilterNode{c}} }

func members(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("v%06d", i)
	}
	return out
}

func TestSetsBindOneParameter(t *testing.T) {
	for _, d := range []Dialect{PostgresDialect{}, SQLiteDialect{}} {
		for _, field := range []*FieldSchema{statusField, colorField} {
			for _, op := range []FilterOperator{OpIn, OpNin} {
				t.Run(d.Name()+" "+field.ExternalName+" "+string(op), func(t *testing.T) {
					small := buildWhere(t, d, &FilterNode{Field: field, Op: op, Value: members(1)})
					large := buildWhere(t, d, &FilterNode{Field: field, Op: op, Value: members(100_000)})
					assert.Equal(t, 1, small.ParamCount)
					assert.Equal(t, 1, large.ParamCount)
					assert.Len(t, large.Args, 1)
				})
			}
		}
	}
}

// An array's `$contains` list binds as one set: the statement is the same
// text and one parameter whatever the list's size.
func TestContainsBindsOneSet(t *testing.T) {
	tagsPath := &FieldSchema{ExternalName: "attrs.tags", DataType: core.FieldTypeArray, Elem: core.FieldTypeString,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"tags"}, Storage: StorageJSONB}}
	labels := &FieldSchema{ExternalName: "labels", SQLExpr: "items.labels", DataType: core.FieldTypeArray, Elem: core.FieldTypeString}
	contains := func(f *FieldSchema, n int) *FilterNode {
		return &FilterNode{Field: f, Op: OpContains, Value: members(n)}
	}
	tests := []struct {
		name string
		d    Dialect
		node func(n int) *FilterNode
	}{
		{"pg path", PostgresDialect{}, func(n int) *FilterNode { return notNode(contains(tagsPath, n)) }},
		{"pg path by containment", PostgresDialect{}, func(n int) *FilterNode { return contains(tagsPath, n) }},
		{"pg column", PostgresDialect{}, func(n int) *FilterNode { return contains(labels, n) }},
		{"sqlite path", SQLiteDialect{}, func(n int) *FilterNode { return contains(tagsPath, n) }},
		{"sqlite column", SQLiteDialect{}, func(n int) *FilterNode { return contains(labels, n) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			small := buildWhere(t, tt.d, tt.node(2))
			large := buildWhere(t, tt.d, tt.node(100_000))
			assert.Equal(t, small.Where, large.Where)
			assert.Equal(t, 1, large.ParamCount)
			assert.Len(t, large.Args, 1)
		})
	}
}

func TestSetCostIsIndependentOfSize(t *testing.T) {
	cost := func(n int) int {
		return NewParsedFilter(&FilterAST{Root: &FilterNode{Field: statusField, Op: OpIn, Value: members(n)}}).EstimatedCost
	}
	assert.Equal(t, cost(1), cost(100_000))
}

func TestSetRendering(t *testing.T) {
	decimalPath := &FieldSchema{ExternalName: "attrs.price", DataType: core.FieldTypeDecimal,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"price"}, Storage: StorageJSONB}}
	boolPath := &FieldSchema{ExternalName: "attrs.on", DataType: core.FieldTypeBool,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"on"}, Storage: StorageJSONB}}
	textColor := &FieldSchema{ExternalName: "attrs.color", DataType: core.FieldTypeString,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"color"}, Storage: StorageText}}
	noContainment := PostgresDialect{ContainmentMaxValues: -1}
	tests := []struct {
		name  string
		d     Dialect
		node  *FilterNode
		where string
		args  []any
	}{
		{"pg column", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpIn, Value: []any{"a", "b"}},
			"items.status = ANY($1)", []any{[]string{"a", "b"}}},
		{"pg column nin", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpNin, Value: []any{"a"}},
			"items.status <> ALL($1)", []any{[]string{"a"}}},
		{"pg NULL member makes a miss unknown", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpIn, Value: []any{"a", nil}},
			"(items.status = ANY($1) OR NULL)", []any{[]string{"a"}}},
		{"pg NULL member in nin", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpNin, Value: []any{nil, "a"}},
			"(items.status <> ALL($1) AND NULL)", []any{[]string{"a"}}},
		{"only NULL members", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpNin, Value: []any{nil}}, "NULL", []any{}},
		{"empty in", PostgresDialect{}, &FilterNode{Field: statusField, Op: OpIn, Value: []any{}}, "1=0", []any{}},
		{"pg path string set by containment", PostgresDialect{}, &FilterNode{Field: colorField, Op: OpIn, Value: []any{"red", "blue"}},
			"items.attrs @> ANY($1::jsonb[])", []any{[]string{`{"color":"red"}`, `{"color":"blue"}`}}},
		{"pg path set beyond the containment bound", PostgresDialect{ContainmentMaxValues: 1}, &FilterNode{Field: colorField, Op: OpIn, Value: []any{"red", "blue"}},
			"(CASE WHEN jsonb_typeof(items.attrs #> '{color}') = 'string' THEN (items.attrs #>> '{color}') END) IN (SELECT unnest($1::text[]) AS v)", []any{[]string{"red", "blue"}}},
		{"pg path set without containment", noContainment, &FilterNode{Field: colorField, Op: OpNin, Value: []any{"red"}},
			"(CASE WHEN jsonb_typeof(items.attrs #> '{color}') = 'string' THEN (items.attrs #>> '{color}') END) NOT IN (SELECT unnest($1::text[]) AS v)", []any{[]string{"red"}}},
		{"pg path decimal set", PostgresDialect{}, &FilterNode{Field: decimalPath, Op: OpIn, Value: []any{"9.5", "100"}},
			FieldExpr(PostgresDialect{}, decimalPath) + " IN (SELECT unnest($1::text[])::numeric AS v)", []any{[]string{"9.5", "100"}}},
		{"pg path bool set", noContainment, &FilterNode{Field: boolPath, Op: OpIn, Value: []any{true}},
			FieldExpr(PostgresDialect{}, boolPath) + " IN (SELECT unnest($1::boolean[]) AS v)", []any{[]bool{true}}},
		{"pg text storage keeps the typed set", PostgresDialect{}, &FilterNode{Field: textColor, Op: OpIn, Value: []any{"red"}},
			FieldExpr(PostgresDialect{}, textColor) + " IN (SELECT unnest($1::text[]) AS v)", []any{[]string{"red"}}},
		{"sqlite column", SQLiteDialect{}, &FilterNode{Field: statusField, Op: OpIn, Value: []any{"a", "b"}},
			"items.status IN (SELECT value AS v FROM json_each(?1))", []any{`["a","b"]`}},
		{"sqlite floats are REAL", SQLiteDialect{}, &FilterNode{Field: &FieldSchema{ExternalName: "score", SQLExpr: "items.score", DataType: core.FieldTypeFloat}, Op: OpNin, Value: []any{2.5, 8.172340535545245e18, 1.0}},
			"items.score NOT IN (SELECT value AS v FROM json_each(?1))", []any{`[2.5,8.172340535545245e+18,1]`}},
		{"sqlite decimals", SQLiteDialect{}, &FilterNode{Field: &FieldSchema{ExternalName: "amount", SQLExpr: "items.amount", DataType: core.FieldTypeDecimal}, Op: OpIn, Value: []any{"9.5"}},
			"parse_decimal(items.amount) IN (SELECT value AS v FROM json_each(?1))", []any{`["9.5"]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := buildWhere(t, tt.d, tt.node)
			assert.Equal(t, tt.where, res.Where)
			assert.Equal(t, tt.args, res.Args)
		})
	}
}

func TestContainmentOnlyWhereSelectionIsTwoValued(t *testing.T) {
	eq := func(f *FieldSchema, v any) *FilterNode { return &FilterNode{Field: f, Op: OpEq, Value: v} }
	typed := FieldExpr(PostgresDialect{}, colorField) + " = $1"
	nested := &FieldSchema{ExternalName: "attrs.dims.w", DataType: core.FieldTypeFloat,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"dims", "w"}, Storage: StorageJSONB}}
	indexed := &FieldSchema{ExternalName: "attrs.list.0", DataType: core.FieldTypeString,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"list", "0"}, Storage: StorageJSONB}}
	decimalPath := &FieldSchema{ExternalName: "attrs.price", DataType: core.FieldTypeDecimal,
		Path: &JSONPath{Column: "items.attrs", Segments: []string{"price"}, Storage: StorageJSONB}}
	tests := []struct {
		name  string
		node  *FilterNode
		where string
		args  []any
	}{
		{"selected", eq(colorField, "red"), "items.attrs @> $1::jsonb", []any{`{"color":"red"}`}},
		{"nested members nest the fragment", eq(nested, 2.5), "items.attrs @> $1::jsonb", []any{`{"dims":{"w":2.5}}`}},
		{"under and / or", &FilterNode{Op: OpOr, Children: []*FilterNode{eq(colorField, "red"), {Op: OpAnd, Children: []*FilterNode{eq(colorField, "blue"), eq(nested, 1.0)}}}},
			"(items.attrs @> $1::jsonb OR (items.attrs @> $2::jsonb AND items.attrs @> $3::jsonb))", []any{`{"color":"red"}`, `{"color":"blue"}`, `{"dims":{"w":1}}`}},
		{"under not, typed", notNode(eq(colorField, "red")), "NOT (" + typed + ")", []any{"red"}},
		{"under not of or, typed", notNode(&FilterNode{Op: OpOr, Children: []*FilterNode{eq(colorField, "red"), eq(colorField, "blue")}}),
			"NOT ((" + typed + " OR " + FieldExpr(PostgresDialect{}, colorField) + " = $2))", []any{"red", "blue"}},
		{"double negation selects again", notNode(notNode(eq(colorField, "red"))), "NOT (NOT (items.attrs @> $1::jsonb))", []any{`{"color":"red"}`}},
		{"NULL equality stays typed", eq(colorField, nil), FieldExpr(PostgresDialect{}, colorField) + " = $1", []any{nil}},
		{"an index segment stays typed", eq(indexed, "x"), FieldExpr(PostgresDialect{}, indexed) + " = $1", []any{"x"}},
		{"decimals stay typed", eq(decimalPath, "9.5"), FieldExpr(PostgresDialect{}, decimalPath) + " = $1::numeric", []any{"9.5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := buildWhere(t, PostgresDialect{}, tt.node)
			assert.Equal(t, tt.where, res.Where)
			assert.Equal(t, tt.args, res.Args)
		})
	}
}

func TestSetMembersOutsideTheDialect(t *testing.T) {
	big := uint64(math.MaxInt64) + 1
	count := &FieldSchema{ExternalName: "count", SQLExpr: "items.count", DataType: core.FieldTypeInt}

	res := buildWhere(t, PostgresDialect{}, &FilterNode{Field: count, Op: OpIn, Value: []any{uint64(1), big}})
	assert.Equal(t, []any{[]uint64{1, big}}, res.Args, "unsigned members above int64 bind for a numeric column")

	_, err := NewSQLBuilder(&ResourceSchema{Name: "items"}, PostgresDialect{}).Build(&FilterAST{Root: &FilterNode{Field: count, Op: OpIn, Value: []any{int64(-1), big}}})
	assert.Error(t, err, "no PostgreSQL integer array holds both")

	_, err = NewSQLBuilder(&ResourceSchema{Name: "items"}, SQLiteDialect{}).Build(&FilterAST{Root: &FilterNode{Field: count, Op: OpIn, Value: []any{big}}})
	assert.Error(t, err, "SQLite integers end at int64")
}
