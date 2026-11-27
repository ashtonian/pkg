//go:build unit

package parsesqlite_test

import (
	"database/sql"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// sqliteMinimum is the supported SQLite release as the README states it.
var sqliteMinimum = regexp.MustCompile(`need \*\*SQLite (\d+\.\d+\.\d+)\*\* or later`)

// The driver this module requires runs a SQLite at least as new as the
// README's minimum, and every construct the README says the minimum is for:
// a driver bump that went below it, or a minimum raised past it, fails here.
func TestDriver_MeetsTheSQLiteMinimum(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	require.NoError(t, err)
	stated := sqliteMinimum.FindSubmatch(readme)
	require.NotNil(t, stated, "README.md states the SQLite minimum as %s", sqliteMinimum)
	minimum := versionParts(t, string(stated[1]))

	queries := []struct {
		name, query, want string
	}{
		{"row-value comparison", `SELECT (1, 'b') > (1, 'a')`, "1"},
		{"row-value IN", `SELECT (1, 'b') IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each('[[1,"b"]]'))`, "1"},
		{"window function", `SELECT row_number() OVER (ORDER BY k) FROM probe`, "1"},
		{"NULLS FIRST", `SELECT k IS NULL FROM (SELECT k FROM probe UNION ALL SELECT NULL) ORDER BY k NULLS FIRST LIMIT 1`, "1"},
		{"the -> operator", `SELECT '{"a":[1, 2]}' -> '$.a'`, "[1,2]"},
	}
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	var version string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT sqlite_version()`).Scan(&version))
	assert.GreaterOrEqual(t, slices.Compare(versionParts(t, version), minimum), 0,
		"modernc.org/sqlite runs SQLite %s, below the stated minimum %s", version, stated[1])

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
}

// versionParts is a dotted SQLite version as its numbers.
func versionParts(t *testing.T, version string) []int {
	t.Helper()
	fields := strings.Split(version, ".")
	parts := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		require.NoError(t, err, "version %q", version)
		parts[i] = n
	}
	return parts
}
