//go:build unit || integration

package parse_test

import (
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// arrayDB is one database under the array matrix: doc and lines are
// columns of docType, labels and scores of labelsType and scoresType.
type arrayDB struct {
	db                              *sql.DB
	dialect                         filter.Dialect
	docType, labelsType, scoresType string
	docCast                         string // wraps a document placeholder, e.g. "%s::jsonb"
	// values maps a record to its labels and scores insert arguments.
	values func(r filtertest.ArrayRecord) (labels, scores any)
}

func loadArrayRecords(t *testing.T, d arrayDB) *filter.ResourceSchema {
	t.Helper()
	schema, err := filtertest.ArraySchema()
	require.NoError(t, err)
	_, err = d.db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, doc " + d.docType + ", labels " + d.labelsType +
		", scores " + d.scoresType + ", lines " + d.docType + ")")
	require.NoError(t, err)
	document := func(n int) string {
		if d.docCast == "" {
			return d.dialect.Placeholder(n)
		}
		return fmt.Sprintf(d.docCast, d.dialect.Placeholder(n))
	}
	insert := "INSERT INTO " + schema.Name + " (id, doc, labels, scores, lines) VALUES (" +
		d.dialect.Placeholder(1) + ", " + document(2) + ", " + d.dialect.Placeholder(3) + ", " + d.dialect.Placeholder(4) + ", " + document(5) + ")"
	for _, r := range filtertest.ArrayRecords() {
		labels, scores := d.values(r)
		_, err := d.db.Exec(insert, r.ID, jsonText(r.Doc), labels, scores, jsonText(r.Lines))
		require.NoError(t, err, r.ID)
	}
	return schema
}

// runArrayMatrix runs the array cases and facets on d.
func runArrayMatrix(t *testing.T, d arrayDB) {
	schema := loadArrayRecords(t, d)
	t.Run("cases", func(t *testing.T) {
		filtertest.RunArrays(t, schema, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			res, err := filter.NewSQLBuilder(schema, d.dialect).Build(f.AST)
			if err != nil {
				return nil, err
			}
			return queryIDs(t, d.db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args), nil
		})
	})
	t.Run("facets", func(t *testing.T) {
		var reqs []filter.FacetRequest
		for _, name := range slices.Sorted(maps.Keys(filtertest.ArrayFacets)) {
			reqs = append(reqs, filter.FacetRequest{Field: name})
		}
		q, err := filter.BuildFacetQuery(schema, d.dialect, "1=1", nil, reqs)
		require.NoError(t, err)
		rows, err := d.db.Query(q.SQL, q.Args...)
		require.NoError(t, err, q.SQL)
		results, err := filter.ScanFacetUnion(rows)
		require.NoError(t, err)
		require.NoError(t, rows.Close())
		got := map[string]map[string]int64{}
		for _, r := range results {
			got[r.Field] = map[string]int64{}
			for _, b := range r.Buckets {
				got[r.Field][b.Key] = b.Count
			}
		}
		assert.Equal(t, filtertest.ArrayFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, d.db, d.dialect, schema, filtertest.ArrayFacetOrders, filtertest.ArrayFacets)
	})
}
