//go:build unit

package parsesqlite_test

import (
	"database/sql"
	"os"
	"regexp"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/parsesqlite"
)

// sqliteMinimum is the supported SQLite release as the README states it.
var sqliteMinimum = regexp.MustCompile(`need \*\*SQLite (\d+\.\d+\.\d+)\*\* or later`)

// Each driver parse documents runs a SQLite at least as new as the minimum
// the README states and CheckVersion enforces, and every construct the
// README says the minimum is for: a driver bump that went below it, or a
// minimum raised past one, fails here.
func TestDrivers_MeetTheSQLiteMinimum(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	require.NoError(t, err)
	stated := sqliteMinimum.FindSubmatch(readme)
	require.NotNil(t, stated, "README.md states the SQLite minimum as %s", sqliteMinimum)
	assert.Equal(t, parsesqlite.MinSQLiteVersion, string(stated[1]), "the README states the minimum CheckVersion enforces")

	queries := []struct {
		name, query, want string
	}{
		{"row-value comparison", `SELECT (1, 'b') > (1, 'a')`, "1"},
		{"row-value IN", `SELECT (1, 'b') IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each('[[1,"b"]]'))`, "1"},
		{"window function", `SELECT row_number() OVER (ORDER BY k) FROM probe`, "1"},
		{"NULLS FIRST", `SELECT k IS NULL FROM (SELECT k FROM probe UNION ALL SELECT NULL) ORDER BY k NULLS FIRST LIMIT 1`, "1"},
		{"the -> operator", `SELECT '{"a":[1, 2]}' -> '$.a'`, "[1,2]"},
		{"a label compared as decoded", `SELECT json_extract('{"a\u003cb":1}', '$."a<b"')`, "1"},
		{"an escaped double quote in a label", `SELECT json_set('{"x\".y":1}', '$."x\".y"', 2)`, `{"x\".y":2}`},
	}
	for _, driver := range []string{"sqlite", "sqlite3"} {
		t.Run(driver, func(t *testing.T) {
			db, err := sql.Open(driver, ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)

			var version string
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT sqlite_version()`).Scan(&version))
			require.NoError(t, parsesqlite.CheckVersion(version), driver)

			_, err = db.ExecContext(t.Context(), `CREATE TABLE probe (k INTEGER PRIMARY KEY)`)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), `INSERT INTO probe VALUES (1)`)
			require.NoError(t, err)
			res, err := db.ExecContext(t.Context(), `INSERT INTO probe VALUES (1) ON CONFLICT DO NOTHING`)
			require.NoError(t, err, "ON CONFLICT DO NOTHING")
			inserted, err := res.RowsAffected()
			require.NoError(t, err)
			assert.Zero(t, inserted, "ON CONFLICT DO NOTHING")
			for _, q := range queries {
				var got string
				err := db.QueryRowContext(t.Context(), q.query).Scan(&got)
				require.NoError(t, err, q.name)
				assert.Equal(t, q.want, got, q.name)
			}
		})
	}
}
