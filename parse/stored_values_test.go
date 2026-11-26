//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"
	"time"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// sealedCode keeps its Valuer on the pointer, as sort schema generation
// admits: a row's copy of it is not addressable.
type sealedCode struct{ code string }

func (c *sealedCode) Value() (driver.Value, error) { return "code-" + c.code, nil }

// A value is read as the database holds it (core.StoredValue) by facets,
// cursors and relation binds alike: each of the types parse keeps as they
// are, and Valuers, which stand for what they write. Each reading ends in
// the value's canonical text as its column's type.
func TestStoredValues_FacetsCursorsAndRelationsAgree(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 600, time.FixedZone("", 3600))
	ref := "0198a3c2-7a1e-7c00-8000-00000000000a"
	for name, tc := range map[string]struct {
		typ  core.FieldType
		v    any
		want string
	}{
		"a time":                    {core.FieldTypeTimestamp, at, "2026-01-02T02:04:05.000000600Z"},
		"a NULL-able time":          {core.FieldTypeTimestamp, sql.NullTime{Time: at, Valid: true}, "2026-01-02T02:04:05.000000600Z"},
		"a date":                    {core.FieldTypeDate, core.Date("2026-01-02"), "2026-01-02"},
		"a decimal":                 {core.FieldTypeDecimal, decimal.RequireFromString("1.50"), "1.5"},
		"a pointer to a decimal":    {core.FieldTypeDecimal, new(decimal.RequireFromString("12345678901234567890.10")), "12345678901234567890.1"},
		"a UUID with a Valuer":      {core.FieldTypeUUID, googleuuid.MustParse(ref), ref},
		"a UUID":                    {core.FieldTypeUUID, uuid.MustParse(ref), ref},
		"a UUID as text":            {core.FieldTypeString, googleuuid.MustParse(ref), ref},
		"a time of day":             {core.FieldTypeTime, core.LocalTime("03:04:05.5"), "03:04:05.5"},
		"a MAC address":             {core.FieldTypeMACAddr, core.MACAddr("08:00:2b:01:02:03"), "08:00:2b:01:02:03"},
		"a Valuer's text":           {core.FieldTypeString, sql.NullString{String: "x", Valid: true}, "x"},
		"a Valuer's integer":        {core.FieldTypeInt, sql.NullInt64{Int64: 5, Valid: true}, "5"},
		"a pointer-receiver Valuer": {core.FieldTypeString, sealedCode{code: "a"}, "code-a"},
		"a scanned interface":       {core.FieldTypeString, new(any("x")), "x"},
	} {
		t.Run(name, func(t *testing.T) {
			info := core.TypeInfo{Type: tc.typ}

			facets, err := filter.FacetKeys(tc.v, &filter.FieldSchema{ExternalName: "f", DataType: tc.typ})
			require.NoError(t, err, "facet")
			assert.Equal(t, []string{tc.want}, facets, "facet")

			key := filter.CursorKey{Name: "f", Column: "f", DataType: tc.typ}
			value, err := filter.KeyValue(key, tc.v)
			require.NoError(t, err, "cursor")
			token := &filter.PageToken{Fields: []filter.CursorField{{Name: "f", Value: value}}}
			require.NoError(t, filter.ValidateCursor(token, []filter.CursorKey{key}), "cursor")
			cursorText, err := core.KeyValueText(info, token.Fields[0].Value)
			require.NoError(t, err, "cursor")
			assert.Equal(t, tc.want, cursorText, "cursor")

			rel := &core.RelationSchema{JoinTable: "pairs", SourceColumns: []string{"owner_id"}, TargetColumns: []string{"target"},
				SourceTypes: []core.TypeInfo{{Type: core.FieldTypeString}}, TargetTypes: []core.TypeInfo{info}}
			op := patch.RelationPatchOp{Field: "f", Schema: rel, Verb: patch.RelationVerbRemove, Values: []any{tc.v}}
			stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []core.Key{{"w1"}}, patch.RenderRelationOpOptions{})
			require.NoError(t, err, "relation")
			require.Len(t, stmts, 1)
			bound := reflect.ValueOf(stmts[0].Args[len(stmts[0].Args)-1])
			require.Equal(t, reflect.Slice, bound.Kind(), "the targets bind as one array")
			require.Equal(t, 1, bound.Len())
			relationText, err := core.KeyValueText(info, bound.Index(0).Interface())
			require.NoError(t, err, "relation")
			assert.Equal(t, tc.want, relationText, "relation")
		})
	}
}
