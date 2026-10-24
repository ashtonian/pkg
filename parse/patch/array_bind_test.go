//go:build unit

package patch_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type arrayRow struct {
	IDs    []uuid.UUID `json:"ids"    db:"ids"    parse:"patch"`
	Counts []int32     `json:"counts" db:"counts" parse:"patch"`
	Big    []int64     `json:"big"    db:"big"    parse:"patch"`
	Ratios []float64   `json:"ratios" db:"ratios" parse:"patch"`
	Flags  []bool      `json:"flags"  db:"flags"  parse:"patch"`
	Times  []time.Time `json:"times"  db:"times"  parse:"patch"`
	Names  []string    `json:"names"  db:"names"  parse:"patch"`
}

// A replaced array binds as a typed slice on PostgreSQL — UUIDs as their
// text — so pgx encodes it in every query mode, including the exec and
// simple-protocol modes that never learn the column's type.
func TestBuildPatchSQL_ArraysBindTyped(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[arrayRow](core.NewSchemaGenerator())
	require.NoError(t, err)
	parsed, err := patch.ParseMergePatch([]byte(`{
		"ids":["0190f3c2-0000-7000-8000-000000000001"],
		"counts":[1,2],"big":[9007199254740993],"ratios":[1.5],"flags":[true],
		"times":["2026-10-14T01:00:00Z"],"names":["a"]}`), schema, nil)
	require.NoError(t, err)
	set, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	require.NoError(t, err)

	want := map[string]any{
		"ids":    []string{"0190f3c2-0000-7000-8000-000000000001"},
		"counts": []int64{1, 2},
		"big":    []int64{9007199254740993},
		"ratios": []float64{1.5},
		"flags":  []bool{true},
		"times":  []time.Time{time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)},
		"names":  []string{"a"},
	}
	assert.ElementsMatch(t, values(want), set.Args, "every array binds as a typed slice, never []any")
}

func values(m map[string]any) []any {
	out := make([]any, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
