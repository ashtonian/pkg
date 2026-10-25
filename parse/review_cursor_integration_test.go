//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// N12: a column with a linguistic collation still walks in code-point order
// the order the memory backend and filters use — page after page, under
// either code-point collation (pg_c_utf8, the default, and "C" for
// PostgreSQL 14–16), and a string range filter seeks in the same order.
func TestKeysetWalk_LinguisticCollationWalksByCodePoint(t *testing.T) {
	schema := &filter.ResourceSchema{Name: "probe", PrimaryKey: "id", Fields: map[string]*filter.FieldSchema{
		"name": {ExternalName: "name", SQLExpr: "probe.name", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpGt}},
	}}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {SQLExpr: "probe.name", Column: "name", DataType: core.FieldTypeString},
		"id":   {SQLExpr: "probe.id", Column: "id", DataType: core.FieldTypeInt},
	}}
	for name, dialect := range map[string]filter.PostgresDialect{
		"pg_c_utf8": {},
		"C":         {Collation: filter.CollationC},
	} {
		t.Run(name, func(t *testing.T) {
			db := dbtest.Postgres(t)
			_, err := db.Exec(`CREATE TABLE probe (id INT PRIMARY KEY, name TEXT NOT NULL COLLATE "en-US-x-icu")`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO probe VALUES (1, 'b'), (2, 'B'), (3, 'a'), (4, 'A'), (5, 'é'), (6, 'z')`)
			require.NoError(t, err)
			lq := filter.NewListQuery(schema, sorts, dialect)

			var walked []string
			cursor := ""
			for range 6 {
				opts := []filter.ListOption{filter.ListSort("name"), filter.ListLimit(1)}
				if cursor != "" {
					opts = append(opts, filter.ListCursor(cursor))
				}
				q, err := lq.Build(opts...)
				require.NoError(t, err)
				rows := queryNames(t, db, "SELECT id, name FROM probe "+q.Fragment, q.Args)
				page, err := filter.NewPage(rows, 1, q.Cursor, func(r namedRow, col string) (any, error) {
					if col == "id" {
						return r.id, nil
					}
					return r.name, nil
				})
				require.NoError(t, err)
				for _, r := range page.Items {
					walked = append(walked, r.name)
				}
				if !page.PageInfo.HasNextPage {
					break
				}
				cursor = string(page.PageInfo.EndCursor)
			}
			assert.Equal(t, []string{"A", "B", "a", "b", "z", "é"}, walked)
			assert.Negative(t, filter.CompareKeyValues(filter.CursorKey{DataType: core.FieldTypeString}, "B", "a"), "memory orders the same way")

			q, err := lq.Build(filter.ListFilter(`{"name":{"$gt":"b"}}`), filter.ListSort("name"))
			require.NoError(t, err)
			var above []string
			for _, r := range queryNames(t, db, "SELECT id, name FROM probe "+q.Fragment, q.Args) {
				above = append(above, r.name)
			}
			assert.Equal(t, []string{"z", "é"}, above)
		})
	}
}

type namedRow struct {
	id   int64
	name string
}

func queryNames(t *testing.T, db *sql.DB, q string, args []any) []namedRow {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err)
	defer rows.Close()
	var out []namedRow
	for rows.Next() {
		var r namedRow
		require.NoError(t, rows.Scan(&r.id, &r.name))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}
