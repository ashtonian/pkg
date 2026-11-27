//go:build unit

package core_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type namedString string

type point struct{ X, Y int }

func TestTypeOf(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want core.TypeInfo
	}{
		{"string", reflect.TypeFor[string](), core.TypeInfo{Type: core.FieldTypeString}},
		{"named string", reflect.TypeFor[namedString](), core.TypeInfo{Type: core.FieldTypeString}},
		{"int32", reflect.TypeFor[int32](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int32}},
		{"*uint8", reflect.TypeFor[*uint8](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Uint8}},
		{"float32", reflect.TypeFor[float32](), core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}},
		{"bool", reflect.TypeFor[bool](), core.TypeInfo{Type: core.FieldTypeBool}},
		{"*time.Time", reflect.TypeFor[*time.Time](), core.TypeInfo{Type: core.FieldTypeTimestamp}},
		{"uuid", reflect.TypeFor[uuid.UUID](), core.TypeInfo{Type: core.FieldTypeUUID}},
		{"[]string", reflect.TypeFor[[]string](), core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeString}},
		{"[]int64", reflect.TypeFor[[]int64](), core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeInt, NumberKind: reflect.Int64}},
		{"[]uuid", reflect.TypeFor[[]uuid.UUID](), core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeUUID}},
		{"[]any", reflect.TypeFor[[]any](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"[]struct", reflect.TypeFor[[]point](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"map", reflect.TypeFor[map[string]any](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"any", reflect.TypeFor[any](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"json.RawMessage", reflect.TypeFor[json.RawMessage](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"[]byte", reflect.TypeFor[[]byte](), core.TypeInfo{Type: core.FieldTypeAny}},
		{"struct", reflect.TypeFor[point](), core.TypeInfo{Type: core.FieldTypeObject}},
		{"chan", reflect.TypeFor[chan int](), core.TypeInfo{Type: core.FieldTypeAny}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.TypeOf(tt.typ))
		})
	}
}

// validInt64 has a nullable wrapper's fields but no Scanner or Valuer: no
// driver reads it as one value.
type validInt64 struct {
	Int64 int64
	Valid bool
}

// A nullable wrapper's column holds its value, NULL while Valid is false:
// the column is typed as the value's type, wherever the wrapper comes from.
// A document holds what encoding/json writes for the wrapper, its fields, so
// a member or an element of one keeps the struct's type.
func TestTypeOf_NullableWrapper(t *testing.T) {
	tests := []struct {
		name   string
		typ    reflect.Type
		column core.TypeInfo
		member core.TypeInfo
	}{
		{"sql.NullString", reflect.TypeFor[sql.NullString](), core.TypeInfo{Type: core.FieldTypeString}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.NullInt64", reflect.TypeFor[sql.NullInt64](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.NullInt32", reflect.TypeFor[sql.NullInt32](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int32}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.NullFloat64", reflect.TypeFor[sql.NullFloat64](), core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float64}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.NullBool", reflect.TypeFor[sql.NullBool](), core.TypeInfo{Type: core.FieldTypeBool}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.NullTime", reflect.TypeFor[sql.NullTime](), core.TypeInfo{Type: core.FieldTypeTimestamp}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"*sql.NullTime", reflect.TypeFor[*sql.NullTime](), core.TypeInfo{Type: core.FieldTypeTimestamp}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.Null[int16]", reflect.TypeFor[sql.Null[int16]](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int16}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"sql.Null[core.Date]", reflect.TypeFor[sql.Null[core.Date]](), core.TypeInfo{Type: core.FieldTypeDate}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"google uuid.NullUUID", reflect.TypeFor[uuid.NullUUID](), core.TypeInfo{Type: core.FieldTypeUUID}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"decimal.NullDecimal", reflect.TypeFor[decimal.NullDecimal](), core.TypeInfo{Type: core.FieldTypeDecimal}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"pgtype.Int8", reflect.TypeFor[pgtype.Int8](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"pgtype.Text", reflect.TypeFor[pgtype.Text](), core.TypeInfo{Type: core.FieldTypeString}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"pgtype.Bool", reflect.TypeFor[pgtype.Bool](), core.TypeInfo{Type: core.FieldTypeBool}, core.TypeInfo{Type: core.FieldTypeObject}},
		// More than a value and its flag: no one value.
		{"pgtype.Timestamptz", reflect.TypeFor[pgtype.Timestamptz](), core.TypeInfo{Type: core.FieldTypeObject}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"a wrapper's fields without a Scanner", reflect.TypeFor[validInt64](), core.TypeInfo{Type: core.FieldTypeObject}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"a slice of wrappers is a document", reflect.TypeFor[[]sql.NullString](), core.TypeInfo{Type: core.FieldTypeJSON}, core.TypeInfo{Type: core.FieldTypeJSON}},
		{"int64", reflect.TypeFor[int64](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}, core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.column, core.TypeOf(tt.typ), "column")
			assert.Equal(t, tt.member, core.MemberTypeOf(tt.typ), "member")
		})
	}
	// pgtype.UUID is a wrapper whose value is a bare [16]byte, which is
	// binary, not a UUID type: the README states the limit.
	assert.Equal(t, core.TypeInfo{Type: core.FieldTypeAny}, core.TypeOf(reflect.TypeFor[pgtype.UUID]()), "pgtype.UUID")
}

func TestNormalizeJSON(t *testing.T) {
	type doc struct {
		Name string            `json:"name"`
		Tags []string          `json:"tags"`
		Meta map[string]uint64 `json:"meta"`
	}
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"decoder numbers", map[string]any{"a": json.Number("1"), "b": json.Number("2.5"), "c": json.Number("18446744073709551615")},
			map[string]any{"a": int64(1), "b": 2.5, "c": uint64(18446744073709551615)}},
		{"exact exponent integer", json.Number("1e2"), int64(100)},
		{"large exact decimal integer", json.Number("9007199254740993.0"), int64(9007199254740993)},
		{"portable fraction", json.Number("0.1"), 0.1},
		{"nested arrays", []any{json.Number("1"), []any{true, nil, "x"}}, []any{int64(1), []any{true, nil, "x"}}},
		{"typed struct", doc{Name: "n", Tags: []string{"a"}, Meta: map[string]uint64{"k": 7}},
			map[string]any{"name": "n", "tags": []any{"a"}, "meta": map[string]any{"k": int64(7)}}},
		{"typed slice", []string{"x", "y"}, []any{"x", "y"}},
		{"go int", 42, int64(42)},
		{"go float", float32(0.5), 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.NormalizeJSON(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeJSON_Rejects(t *testing.T) {
	for name, in := range map[string]any{
		"integer beyond 64 bits":    json.Number("123456789012345678901234"),
		"rounded decimal":           json.Number("1.0000000000000000001"),
		"rounded large decimal":     json.Number("9007199254740992.5"),
		"exponent integer overflow": json.Number("18446744073709551616.0"),
		"unbounded exponent":        json.Number("1e-1000000000"),
		"infinite literal":          json.Number("1e400"),
		"nested overflow":           map[string]any{"a": []any{json.Number("99999999999999999999")}},
		"not JSON":                  func() {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := core.NormalizeJSON(in)
			assert.Error(t, err)
		})
	}
}
