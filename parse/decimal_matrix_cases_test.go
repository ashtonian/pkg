//go:build unit || integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// decimalDB is one database under the decimal matrix: amount is a column of
// amountType, doc one of docType.
type decimalDB struct {
	db         *sql.DB
	dialect    filter.Dialect
	amountType string
	docType    string
	cast       string // wraps the document placeholder, e.g. "%s::jsonb"
}

// loadDecimalRecords writes DecimalRecords, each amount as its decimal text
// with its scale.
func loadDecimalRecords(t *testing.T, d decimalDB) *filter.ResourceSchema {
	t.Helper()
	schema, err := filtertest.DecimalSchema()
	require.NoError(t, err)
	_, err = d.db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, amount " + d.amountType + ", doc " + d.docType + ")")
	require.NoError(t, err)
	doc := d.dialect.Placeholder(3)
	if d.cast != "" {
		doc = fmt.Sprintf(d.cast, doc)
	}
	insert := "INSERT INTO " + schema.Name + " (id, amount, doc) VALUES (" + d.dialect.Placeholder(1) + ", " + d.dialect.Placeholder(2) + ", " + doc + ")"
	for _, r := range filtertest.DecimalRecords() {
		var amount any
		if r.Amount != nil {
			amount = r.Amount.StringFixed(max(0, -r.Amount.Exponent()))
		}
		_, err := d.db.Exec(insert, r.ID, amount, string(r.Doc))
		require.NoError(t, err, r.ID)
	}
	return schema
}

// runDecimalMatrix runs the decimal cases, sorts, keyset walks and facets
// on d, and checks that each row's price reads in SQL as filter.PathValue
// reads it.
func runDecimalMatrix(t *testing.T, d decimalDB) {
	schema := loadDecimalRecords(t, d)
	t.Run("cases", func(t *testing.T) {
		filtertest.RunDecimals(t, schema, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			res, err := filter.NewSQLBuilder(schema, d.dialect).Build(f.AST)
			if err != nil {
				return nil, err
			}
			return queryIDs(t, d.db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args), nil
		})
	})
	t.Run("walks", func(t *testing.T) {
		r, err := parse.NewResource[filtertest.DecimalRecord](schema.Name, parse.PageSize(walkPage, 10))
		require.NoError(t, err)
		walkOrders(t, walker[filtertest.DecimalRecord]{db: d.db, dialect: d.dialect, resource: r, scan: scanDecimalRecord,
			id: func(r filtertest.DecimalRecord) string { return r.ID }}, filtertest.DecimalOrders)
	})
	t.Run("facets", func(t *testing.T) {
		var reqs []filter.FacetRequest
		for _, name := range slices.Sorted(maps.Keys(filtertest.DecimalFacets)) {
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
		assert.Equal(t, filtertest.DecimalFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, d.db, d.dialect, schema, filtertest.DecimalFacetOrders, filtertest.DecimalFacets)
	})
	t.Run("values match PathValue", func(t *testing.T) {
		f := schema.Fields["doc.price"]
		rows, err := d.db.Query("SELECT id, doc, " + filter.FieldExpr(d.dialect, f) + " FROM " + schema.Name + " ORDER BY id")
		require.NoError(t, err)
		for rows.Next() {
			var id string
			var doc, got any
			require.NoError(t, rows.Scan(&id, &doc, &got))
			want, present, err := filter.PathValue(textOf(doc), f)
			require.NoError(t, err, id)
			if !present {
				want = nil
			}
			assert.True(t, sameDecimal(got, want), "row %s: SQL %v (%T), PathValue %v", id, got, got, want)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	})
}

func scanDecimalRecord(rows *sql.Rows) (filtertest.DecimalRecord, error) {
	var rec filtertest.DecimalRecord
	var amount, doc any
	if err := rows.Scan(&rec.ID, &amount, &doc); err != nil {
		return rec, err
	}
	if a, ok := textOf(amount).(string); ok {
		d, err := decimal.NewFromString(a)
		if err != nil {
			return rec, err
		}
		rec.Amount = &d
	}
	if s, ok := textOf(doc).(string); ok {
		rec.Doc = json.RawMessage(s)
	}
	return rec, nil
}
