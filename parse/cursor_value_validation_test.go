//go:build unit

package parse_test

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCursorValuesValidateAgainstActiveKeys(t *testing.T) {
	tests := []struct {
		name     string
		dt       core.FieldType
		kind     reflect.Kind
		nullable bool
		value    any
		want     any
		invalid  bool
	}{
		{name: "integer boolean", dt: core.FieldTypeInt, value: true, invalid: true},
		{name: "integer string", dt: core.FieldTypeInt, value: "42", invalid: true},
		{name: "integer null", dt: core.FieldTypeInt, invalid: true},
		{name: "fractional integer", dt: core.FieldTypeInt, value: 1.5, invalid: true},
		{name: "inexact integer", dt: core.FieldTypeInt, value: float64(1 << 54), invalid: true},
		{name: "int8 overflow", dt: core.FieldTypeInt, kind: reflect.Int8, value: int64(128), invalid: true},
		{name: "unsigned negative", dt: core.FieldTypeInt, kind: reflect.Uint8, value: int64(-1), invalid: true},
		{name: "unsigned overflow", dt: core.FieldTypeInt, kind: reflect.Uint8, value: uint64(256), invalid: true},
		{name: "float32 overflow", dt: core.FieldTypeFloat, kind: reflect.Float32, value: math.MaxFloat64, invalid: true},
		{name: "invalid UUID", dt: core.FieldTypeUUID, value: "invalid", invalid: true},
		{name: "invalid timestamp", dt: core.FieldTypeTimestamp, value: "yesterday", invalid: true},
		{name: "boolean string", dt: core.FieldTypeBool, value: "true", invalid: true},
		{name: "numeric string", dt: core.FieldTypeString, value: int64(42), invalid: true},
		{name: "max integer", dt: core.FieldTypeInt, value: int64(math.MaxInt64), want: int64(math.MaxInt64)},
		{name: "max unsigned", dt: core.FieldTypeInt, kind: reflect.Uint64, value: uint64(math.MaxUint64), want: uint64(math.MaxUint64)},
		{name: "small unsigned", dt: core.FieldTypeInt, kind: reflect.Uint8, value: uint8(255), want: uint64(255)},
		{name: "integral float", dt: core.FieldTypeInt, value: 42.0, want: int64(42)},
		{name: "float", dt: core.FieldTypeFloat, value: 1.5, want: 1.5},
		{name: "nullable integer", dt: core.FieldTypeInt, nullable: true},
		{name: "uuid", dt: core.FieldTypeUUID, value: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", want: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		{name: "timestamp", dt: core.FieldTypeTimestamp, value: "2026-01-01T01:00:00+01:00", want: "2026-01-01T00:00:00.000000000Z"},
		{name: "boolean", dt: core.FieldTypeBool, value: true, want: true},
		{name: "string", dt: core.FieldTypeString, value: "key", want: "key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &filter.PageToken{Fields: []filter.CursorField{{Name: "key", Direction: filter.SortAsc, Value: tt.value}}}
			encoded, err := filter.EncodeCursor(token)
			require.NoError(t, err)
			decoded, err := filter.DecodeCursor(encoded)
			require.NoError(t, err)
			err = filter.ValidateCursor(decoded, []filter.CursorKey{{Name: "key", Direction: filter.SortAsc, DataType: tt.dt, NumberKind: tt.kind, Nullable: tt.nullable}})
			if tt.invalid {
				var pe *core.ParseError
				require.True(t, errors.As(err, &pe))
				require.Equal(t, filter.ECodeInvalidCursor.String(), pe.Code)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, decoded.Fields[0].Value)
		})
	}
}

func TestCursorValidationFailureDoesNotNormalizeEarlierValues(t *testing.T) {
	token := &filter.PageToken{Fields: []filter.CursorField{
		{Name: "uuid", Direction: filter.SortAsc, Value: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"},
		{Name: "id", Direction: filter.SortAsc, Value: false},
	}}
	err := filter.ValidateCursor(token, []filter.CursorKey{
		{Name: "uuid", Direction: filter.SortAsc, DataType: core.FieldTypeUUID},
		{Name: "id", Direction: filter.SortAsc, DataType: core.FieldTypeInt},
	})
	require.Error(t, err)
	require.Equal(t, "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", token.Fields[0].Value)
	require.Error(t, filter.ValidateCursor(nil, nil))
}

func TestGeneratedSortSchemaValidatesCursorRangeAndNullableValues(t *testing.T) {
	type row struct {
		ID      uint8     `json:"id" db:"id" parse:"filter,sort"`
		Created time.Time `json:"created" db:"created" parse:"sort"`
		UUID    uuid.UUID `json:"uuid" db:"uuid" parse:"sort"`
		Rank    *int16    `json:"rank" db:"rank" parse:"sort"`
	}
	p, err := parse.NewResource[row]("rows")
	require.NoError(t, err)
	require.Equal(t, reflect.Uint8, p.SortSchema().Fields["id"].NumberKind)
	require.Equal(t, reflect.Int16, p.SortSchema().Fields["rank"].NumberKind)
	for _, dialect := range []filter.Dialect{filter.SQLiteDialect{}, filter.PostgresDialect{}} {
		for _, value := range []any{true, "bad", nil, int64(256)} {
			encoded, encodeErr := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: value}}})
			require.NoError(t, encodeErr)
			_, encodeErr = filter.NewListQuery(p.FilterSchema(), p.SortSchema(), dialect).Build(filter.ListCursor(encoded))
			require.Error(t, encodeErr)
			// Filter-only PKs retain their numeric range too.
			_, encodeErr = filter.NewListQuery(p.FilterSchema(), nil, dialect).Build(filter.ListCursor(encoded))
			require.Error(t, encodeErr)
		}
	}
	sort, err := filter.ParseSort(p.SortSchema(), "created,uuid,rank")
	require.NoError(t, err)
	keys := filter.Keyset(sort, filter.CursorKey{Name: "id", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Uint8})
	plan := filter.CursorPlan{Keys: keys}
	values := map[string]any{"id": uint8(255), "created": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "uuid": uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), "rank": (*int16)(nil)}
	token, err := plan.Token(false, func(column string) (any, error) { return values[column], nil })
	require.NoError(t, err)
	encoded, err := filter.EncodeCursor(token)
	require.NoError(t, err)
	token, err = filter.DecodeCursor(encoded)
	require.NoError(t, err)
	require.NoError(t, filter.ValidateCursor(token, keys))
	require.Equal(t, uint64(255), token.Fields[3].Value)
}

func TestSystemFieldCursorTieBreakerValidatesType(t *testing.T) {
	for _, dt := range []core.FieldType{core.FieldTypeInt, core.FieldTypeUUID} {
		schema := &filter.ResourceSchema{Name: "rows", PrimaryKey: "id", SystemFields: map[string]*filter.FieldSchema{
			"id": {SQLExpr: "rows.id", DataType: dt, NumberKind: reflect.Uint8},
		}}
		for _, dialect := range []filter.Dialect{filter.SQLiteDialect{}, filter.PostgresDialect{}} {
			values := []any{true, nil, "not-a-key", int64(256)}
			for _, value := range values {
				encoded, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: value}}})
				require.NoError(t, err)
				_, err = filter.NewListQuery(schema, nil, dialect).Build(filter.ListCursor(encoded))
				var pe *core.ParseError
				require.True(t, errors.As(err, &pe), "type %s, value %v, error %v", dt, value, err)
				require.Equal(t, filter.ECodeInvalidCursor.String(), pe.Code)
			}
			var valid any = int64(255)
			if dt == core.FieldTypeUUID {
				valid = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			}
			encoded, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: valid}}})
			require.NoError(t, err)
			result, err := filter.NewListQuery(schema, nil, dialect).Build(filter.ListCursor(encoded))
			require.NoError(t, err)
			require.Contains(t, result.OrderBy, "rows.id")
		}
	}
}
