//go:build integration

package parse_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestTypedDocumentSQLiteParity(t *testing.T) {
	type dimensions struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	type document struct {
		Dims     dimensions            `json:"dims"`
		Optional *dimensions           `json:"optional,omitempty"`
		Label    string                `json:"label,omitempty"`
		ByName   map[string]dimensions `json:"by_name"`
		Labels   map[string]string     `json:"labels,omitempty"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body string
		merge      bool
	}{
		{"replacement canonicalizes missing fields", `[{"op":"replace","path":"/doc","value":{"dims":{"w":9}}}]`, false},
		{"partial nested merge preserves sibling", `{"doc":{"dims":{"w":9}}}`, true},
		{"nullable complete creation", `{"doc":{"optional":{"w":9,"h":0}}}`, true},
		{"omitted member deletion", `{"doc":{"label":null}}`, true},
		{"typed map complete insertion", `{"doc":{"by_name":{"new":{"w":9,"h":0}}}}`, true},
		{"omitted map safe removal and insertion", `{"doc":{"labels":{"old":null,"new":"value"}}}`, true},
		{"omitted map canonical replacement", `[{"op":"replace","path":"/doc","value":{"dims":{"w":9},"labels":{}}}]`, false},
		{"typed map deletion", `{"doc":{"by_name":{"old":null}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtest.SQLite(t)
			ctx := t.Context()
			_, err := db.ExecContext(ctx, `CREATE TABLE docs (doc TEXT NOT NULL)`)
			require.NoError(t, err)
			initial := row{Doc: document{Dims: dimensions{W: 2, H: 7}, Label: "old", Labels: map[string]string{"old": "value"}, ByName: map[string]dimensions{"old": {W: 3, H: 4}}}}
			start, err := json.Marshal(initial.Doc)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO docs (doc) VALUES (?)`, string(start))
			require.NoError(t, err)
			var parsed *patch.ParsedPatch
			if tc.merge {
				parsed, err = p.ParseMergePatch([]byte(tc.body))
			} else {
				parsed, err = p.ParsePatch([]byte(tc.body))
			}
			require.NoError(t, err)
			set, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "UPDATE docs SET "+set.SetClause, set.Args...)
			require.NoError(t, err)
			got, err := p.ApplyValidatedPatch(parsed, initial)
			require.NoError(t, err)
			encoded, err := json.Marshal(got.Doc)
			require.NoError(t, err)
			var stored string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT doc FROM docs").Scan(&stored))
			assert.JSONEq(t, string(encoded), stored, "SQL and Go agree")
		})
	}
}
