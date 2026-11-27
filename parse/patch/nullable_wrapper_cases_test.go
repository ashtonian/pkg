//go:build unit || integration

package patch_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// rankedVisit's patchable columns are database/sql's nullable wrappers.
type rankedVisit struct {
	ID     int64         `json:"id"      db:"id"`
	Score  sql.NullInt64 `json:"score"   db:"score"   parse:"patch" patch_ops:"replace,remove,test"`
	SeenAt sql.NullTime  `json:"seen_at" db:"seen_at" parse:"patch" patch_ops:"replace,remove,test"`
}

// wrapperDB is a database a rankedVisit is stored in: the column type of a
// timestamp, and the value a timestamp column binds.
type wrapperDB struct {
	db            *sql.DB
	dialect       patch.SQLDialect
	timestampType string
	stamp         func(sql.NullTime) any
}

var rankedSeen = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// runNullableWrapperPatches holds a wrapper column's patch to one result in
// the database and in Go: a replace stores the value, Valid, and a remove
// or a null stores NULL, the wrapper's zero value.
func runNullableWrapperPatches(t *testing.T, wdb wrapperDB) {
	t.Helper()
	ctx := t.Context()
	_, err := wdb.db.ExecContext(ctx, "CREATE TABLE ranked_visits (id BIGINT PRIMARY KEY, score BIGINT, seen_at "+wdb.timestampType+")")
	require.NoError(t, err)
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[rankedVisit](gen)
	require.NoError(t, err)
	applier, err := patch.NewApplier[rankedVisit](gen, nil)
	require.NoError(t, err)
	seeded := rankedVisit{ID: 1, Score: sql.NullInt64{Int64: 3, Valid: true}, SeenAt: sql.NullTime{Time: rankedSeen, Valid: true}}

	for _, tt := range []struct {
		name  string
		body  string
		merge bool
		want  rankedVisit
	}{
		{"replace a score", `[{"op":"replace","path":"/score","value":7}]`, false,
			rankedVisit{ID: 1, Score: sql.NullInt64{Int64: 7, Valid: true}, SeenAt: seeded.SeenAt}},
		{"remove a score", `[{"op":"remove","path":"/score"}]`, false,
			rankedVisit{ID: 1, SeenAt: seeded.SeenAt}},
		{"replace a time", `[{"op":"replace","path":"/seen_at","value":"2026-05-06T07:08:09Z"}]`, false,
			rankedVisit{ID: 1, Score: seeded.Score, SeenAt: sql.NullTime{Time: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC), Valid: true}}},
		{"remove a time", `[{"op":"remove","path":"/seen_at"}]`, false,
			rankedVisit{ID: 1, Score: seeded.Score}},
		{"merge a score and a null time", `{"score":9,"seen_at":null}`, true,
			rankedVisit{ID: 1, Score: sql.NullInt64{Int64: 9, Valid: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := wdb.db.ExecContext(ctx, "DELETE FROM ranked_visits")
			require.NoError(t, err)
			_, err = wdb.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO ranked_visits (id, score, seen_at) VALUES (1, 3, %s)", wdb.dialect.Placeholder(1)), wdb.stamp(seeded.SeenAt))
			require.NoError(t, err)
			var parsed *patch.ParsedPatch
			if tt.merge {
				parsed, err = patch.ParseMergePatch([]byte(tt.body), schema, nil)
			} else {
				parsed, err = patch.NewParser(schema, nil).Parse([]byte(tt.body))
			}
			require.NoError(t, err)

			built, err := patch.BuildPatchSQL(parsed, wdb.dialect)
			require.NoError(t, err)
			stmt := fmt.Sprintf("UPDATE ranked_visits SET %s WHERE id = 1", built.SetClause)
			_, err = wdb.db.ExecContext(ctx, stmt, built.Args...)
			require.NoError(t, err, stmt)
			var stored rankedVisit
			err = wdb.db.QueryRowContext(ctx, "SELECT id, score, seen_at FROM ranked_visits").Scan(&stored.ID, &stored.Score, &stored.SeenAt)
			require.NoError(t, err)
			assertRankedVisit(t, tt.want, stored, "stored")

			applied, err := applier.Apply(parsed, seeded)
			require.NoError(t, err)
			assertRankedVisit(t, tt.want, applied, "applied")
		})
	}
}

// assertRankedVisit compares times as instants: a driver reads them back in
// its own location.
func assertRankedVisit(t *testing.T, want, got rankedVisit, what string) {
	t.Helper()
	assert.Equal(t, want.ID, got.ID, what)
	assert.Equal(t, want.Score, got.Score, what)
	assert.Equal(t, want.SeenAt.Valid, got.SeenAt.Valid, what)
	assert.True(t, want.SeenAt.Time.Equal(got.SeenAt.Time), "%s: seen_at %s, want %s", what, got.SeenAt.Time, want.SeenAt.Time)
}
