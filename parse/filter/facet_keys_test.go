//go:build unit

package filter_test

import (
	"database/sql"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// FacetKeys over the number key fixture counts the buckets the SQL facet
// query does (TestNumberKeys_SQLite / _PostgreSQL).
func TestFacetKeys_NumberKeyFixture(t *testing.T) {
	schema, err := filtertest.NumberKeySchema()
	require.NoError(t, err)
	value := func(r filtertest.NumberKeyRecord, name string) any {
		switch name {
		case "f64":
			return r.F64
		case "f32":
			return r.F32
		case "i":
			return r.I
		case "ratios":
			return r.Ratios
		}
		return r.Attrs
	}
	got := map[string]map[string]int64{}
	for name := range filtertest.NumberKeyFacets {
		f := schema.Fields[name]
		require.NotNil(t, f, name)
		got[name] = map[string]int64{}
		for _, r := range filtertest.NumberKeyRecords() {
			keys, err := filter.FacetKeys(value(r, name), f)
			require.NoError(t, err, "%s row %s", name, r.ID)
			for _, k := range keys {
				got[name][k]++
			}
		}
	}
	assert.Equal(t, filtertest.NumberKeyFacets, got)
}

func TestFacetKeys(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 500000000, time.FixedZone("x", 2*3600))
	id := uuid.MustParse("0198A3C2-7A1E-7C00-8000-000000000001")
	field := func(t core.FieldType) *filter.FieldSchema {
		return &filter.FieldSchema{ExternalName: "v", SQLExpr: "v", DataType: t}
	}
	float32Field := &filter.FieldSchema{ExternalName: "v", SQLExpr: "v", DataType: core.FieldTypeFloat, NumberKind: reflect.Float32}
	tests := []struct {
		name  string
		field *filter.FieldSchema
		value any
		want  []string
	}{
		{"a string as itself", field(core.FieldTypeString), "Red", []string{"Red"}},
		{"a boolean as its JSON literal", field(core.FieldTypeBool), false, []string{"false"}},
		{"an integer", field(core.FieldTypeInt), int32(-7), []string{"-7"}},
		{"a float in plain notation", field(core.FieldTypeFloat), 1e21, []string{"1000000000000000000000"}},
		{"negative zero as 0", field(core.FieldTypeFloat), math.Copysign(0, -1), []string{"0"}},
		{"a float32 field's value at float32 precision", float32Field, float32(0.1), []string{"0.1"}},
		{"a float32 field's double", float32Field, float64(float32(0.1)), []string{"0.1"}},
		{"a NaN has no key", field(core.FieldTypeFloat), math.NaN(), nil},
		{"an infinity has no key", field(core.FieldTypeFloat), math.Inf(-1), nil},
		{"a decimal's canonical text", field(core.FieldTypeDecimal), "10.50", []string{"10.5"}},
		{"a nullable float that is NULL", field(core.FieldTypeFloat), sql.NullFloat64{}, nil},
		{"a nullable float", field(core.FieldTypeFloat), sql.NullFloat64{Float64: 2.50, Valid: true}, []string{"2.5"}},
		{"a nil pointer", field(core.FieldTypeInt), (*int64)(nil), nil},
		{"a pointer to a value", field(core.FieldTypeInt), new(int64(9007199254740993)), []string{"9007199254740993"}},
		{"a timestamp in canonical UTC", field(core.FieldTypeTimestamp), ts, []string{"2026-01-02T01:04:05.500000000Z"}},
		{"a date", field(core.FieldTypeDate), core.Date("2026-01-02"), []string{"2026-01-02"}},
		{"a UUID lower-cased", field(core.FieldTypeUUID), id, []string{"0198a3c2-7a1e-7c00-8000-000000000001"}},
		{"a list's distinct elements", &filter.FieldSchema{ExternalName: "v", SQLExpr: "v", DataType: core.FieldTypeArray, Elem: core.FieldTypeInt}, []int64{3, 1, 3}, []string{"3", "1"}},
		{"a NULL list", &filter.FieldSchema{ExternalName: "v", SQLExpr: "v", DataType: core.FieldTypeArray, Elem: core.FieldTypeString}, []string(nil), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filter.FacetKeys(tt.value, tt.field)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A path member's number keys exactly as written, past a double's digits.
func TestFacetKeys_PathNumberExact(t *testing.T) {
	type rec struct {
		Doc json.RawMessage `json:"doc" db:"doc" parse:"filter" paths:"r:float:facet;at:timestamp:facet;id:uuid:facet"`
	}
	schema, err := filter.GenerateFilterSchema[rec](core.NewSchemaGenerator())
	require.NoError(t, err)
	doc := `{"r":0.10000000000000000001,"at":"2026-01-02T03:04:05.5Z","id":"0198A3C2-7A1E-7C00-8000-000000000001"}`
	for name, want := range map[string][]string{
		"doc.r":  {"0.10000000000000000001"},
		"doc.at": {"2026-01-02T03:04:05.500000000Z"},
		"doc.id": {"0198a3c2-7a1e-7c00-8000-000000000001"},
	} {
		got, err := filter.FacetKeys(doc, schema.Fields[name])
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
}

// A value its field's type cannot hold is an error naming the field.
func TestFacetKeys_RefusesAMismatchedValue(t *testing.T) {
	_, err := filter.FacetKeys(42, &filter.FieldSchema{ExternalName: "status", SQLExpr: "status", DataType: core.FieldTypeString})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "facet status")
}
