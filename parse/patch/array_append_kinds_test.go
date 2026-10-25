//go:build unit

package patch_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// Appending to a SQLite JSON array keeps the element's JSON type for every
// element kind, so the stored array still decodes into its Go type.
func TestArrayAppendKeepsElementTypesSQLite(t *testing.T) {
	ts := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c")
	for _, tc := range []struct {
		name    string
		goType  reflect.Type
		seed    any
		element string
		want    any
	}{
		{"bool", reflect.TypeFor[[]bool](), []bool{false}, `true`, []bool{false, true}},
		{"int", reflect.TypeFor[[]int64](), []int64{1}, `9007199254740993`, []int64{1, 9007199254740993}},
		{"float", reflect.TypeFor[[]float64](), []float64{0.5}, `1.25`, []float64{0.5, 1.25}},
		{"string", reflect.TypeFor[[]string](), []string{"a"}, `"b"`, []string{"a", "b"}},
		{"timestamp", reflect.TypeFor[[]time.Time](), []time.Time{ts}, `"2026-10-01T00:00:00Z"`, []time.Time{ts, ts.Add(12 * time.Hour)}},
		{"uuid", reflect.TypeFor[[]uuid.UUID](), []uuid.UUID{id}, `"` + uuid.Nil.String() + `"`, []uuid.UUID{id, uuid.Nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtest.SQLite(t)
			_, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, vals TEXT)`)
			require.NoError(t, err)
			seed, err := json.Marshal(tc.seed)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO items (id, vals) VALUES (1, ?)`, string(seed))
			require.NoError(t, err)

			parser := patch.NewParser(&patch.PatchSchema{
				Resource: "items", MaxOperations: 5,
				AllowedPaths: map[string]*patch.PathSchema{"/vals": {
					AllowedOps: []patch.PatchOp{patch.PatchOpArrayAppend},
					DataType:   core.FieldTypeArray, GoType: tc.goType, SQLColumn: "vals",
				}},
			}, nil)
			parsed, err := parser.Parse([]byte(fmt.Sprintf(`[{"op":"array_append","path":"/vals","value":%s}]`, tc.element)))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.NoError(t, err)
			_, err = db.Exec(fmt.Sprintf("UPDATE items SET %s WHERE id = ?%d", res.SetClause, res.ParamCount+1), append(res.Args, 1)...)
			require.NoError(t, err)

			var stored string
			require.NoError(t, db.QueryRow(`SELECT vals FROM items WHERE id = 1`).Scan(&stored))
			got := reflect.New(tc.goType)
			require.NoError(t, json.Unmarshal([]byte(stored), got.Interface()), "stored %s", stored)
			require.Equal(t, tc.want, got.Elem().Interface(), "stored %s", stored)
		})
	}
}
