//go:build unit || integration

package parse_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// runGeoMatrix loads filtertest.GeoRecords into a table whose points are
// columns of docType, each placeholder wrapped by cast (e.g. "%s::jsonb"),
// and runs the geo cases on it.
func runGeoMatrix(t *testing.T, db *sql.DB, dialect filter.Dialect, docType, cast string) {
	t.Helper()
	schema, err := filtertest.GeoSchema()
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, loc " + docType + ", pos " + docType + ")")
	require.NoError(t, err)
	doc := func(n int) string {
		if cast == "" {
			return dialect.Placeholder(n)
		}
		return fmt.Sprintf(cast, dialect.Placeholder(n))
	}
	insert := "INSERT INTO " + schema.Name + " (id, loc, pos) VALUES (" + dialect.Placeholder(1) + ", " + doc(2) + ", " + doc(3) + ")"
	for _, r := range filtertest.GeoRecords() {
		_, err := db.Exec(insert, r.ID, nullableText(r.Loc), nullableText(r.Pos))
		require.NoError(t, err, r.ID)
	}
	filtertest.RunGeo(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		res, err := filter.NewSQLBuilder(schema, dialect).Build(f.AST)
		if err != nil {
			return nil, err
		}
		return queryIDs(t, db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args), nil
	})
}

// nullableText is a document's JSON text, or NULL for none.
func nullableText(doc []byte) any {
	if doc == nil {
		return nil
	}
	return string(doc)
}
