//go:build unit || integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// runQuotedMatrix loads filtertest.QuotedRecords into a table whose n is a
// column of intType and runs the quoted-integer cases on it.
func runQuotedMatrix(t *testing.T, db *sql.DB, dialect filter.Dialect, intType string) {
	t.Helper()
	schema, err := filtertest.QuotedSchema()
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, n " + intType + ")")
	require.NoError(t, err)
	insert := "INSERT INTO " + schema.Name + " (id, n) VALUES (" + dialect.Placeholder(1) + ", " + dialect.Placeholder(2) + ")"
	for _, r := range filtertest.QuotedRecords() {
		var n any
		if r.N != nil {
			n = *r.N
		}
		_, err := db.Exec(insert, r.ID, n)
		require.NoError(t, err, r.ID)
	}
	filtertest.RunQuoted(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		res, err := filter.NewSQLBuilder(schema, dialect).Build(f.AST)
		if err != nil {
			return nil, err
		}
		return queryIDs(t, db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args), nil
	})
}
