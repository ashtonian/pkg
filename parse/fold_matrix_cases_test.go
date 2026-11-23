//go:build unit || integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// runFoldMatrix loads filtertest.FoldRecords into a table whose name is a
// column of textType and runs the `$ilike` case cases on it; unicode adds
// those whose answer lower-cases a non-ASCII letter.
func runFoldMatrix(t *testing.T, db *sql.DB, dialect filter.Dialect, textType string, unicode bool) {
	t.Helper()
	schema, err := filtertest.FoldSchema()
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, name " + textType + ")")
	require.NoError(t, err)
	insert := "INSERT INTO " + schema.Name + " (id, name) VALUES (" + dialect.Placeholder(1) + ", " + dialect.Placeholder(2) + ")"
	for _, r := range filtertest.FoldRecords() {
		_, err := db.Exec(insert, r.ID, r.Name)
		require.NoError(t, err, r.ID)
	}
	filtertest.RunFold(t, unicode, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		res, err := filter.NewSQLBuilder(schema, dialect).Build(f.AST())
		if err != nil {
			return nil, err
		}
		return queryIDs(t, db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args), nil
	})
}
