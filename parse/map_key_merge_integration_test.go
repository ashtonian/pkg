//go:build integration

package parse_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

func TestMergePatch_MapKeys(t *testing.T) {
	runSQLBackends(t, runMapKeyMerges)
}

// runMapKeyMerges updates a stored row with each merge patch and reads back
// what encoding/json makes of the same members over the row, the in-memory
// application's result too.
func runMapKeyMerges(t *testing.T, b sqlBackend) {
	open := b.open
	d := b.dialect
	docType := b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})
	r := parse.MustResource[keyedMaps]("keyed_maps")
	for _, tc := range keyedMapMerges() {
		t.Run(tc.name, func(t *testing.T) {
			db := open(t)
			_, err := db.ExecContext(t.Context(), `CREATE TABLE keyed_maps (id BIGINT PRIMARY KEY, by_int `+docType+` NOT NULL, by_uint `+docType+` NOT NULL, by_addr `+docType+` NOT NULL)`)
			require.NoError(t, err)
			row := keyedMapsRow()
			docs := make([]any, 0, 3)
			for _, m := range []any{row.ByInt, row.ByUint, row.ByAddr} {
				raw, err := json.Marshal(m)
				require.NoError(t, err)
				docs = append(docs, string(raw))
			}
			insert := `INSERT INTO keyed_maps VALUES (1, ?, ?, ?)`
			if isPostgres(d) {
				insert = `INSERT INTO keyed_maps VALUES (1, $1, $2, $3)`
			}
			_, err = db.ExecContext(t.Context(), insert, docs...)
			require.NoError(t, err)

			parsed, err := r.ParseMergePatch([]byte(tc.body))
			require.NoError(t, err)
			plan, err := r.BuildUpdate(d, parsed, parse.ByID(int64(1)), parse.Scope{})
			require.NoError(t, err)
			res, err := applyUpdate(t, db, r, d, plan, false)
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)

			want := mergedByEncodingJSON(t, keyedMapsRow(), tc.body)
			assert.Equal(t, want, res.Changed[0])
			inMemory, err := r.ApplyValidatedPatch(parsed, keyedMapsRow())
			require.NoError(t, err)
			assert.Equal(t, inMemory, res.Changed[0])
		})
	}
}
