//go:build integration

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestListOffsetDoesNotOffsetStandaloneQueries(t *testing.T) {
	bound := 50.0
	schema := &filter.ResourceSchema{Name: "items", Fields: map[string]*filter.FieldSchema{
		"status": {ExternalName: "status", SQLExpr: "items.status", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}, Facetable: true},
		"price": {ExternalName: "price", SQLExpr: "items.price", DataType: core.FieldTypeFloat, Facetable: true, FacetSchema: &filter.FacetSchema{
			Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "low", Max: &bound}, {Key: "high", Min: &bound}},
		}},
	}}
	search := &filter.SearchSchema{Language: "english", Fields: []filter.SearchField{{Name: "status", SQLExpr: "items.status"}}}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var dialect filter.Dialect = filter.SQLiteDialect{}
			if backend == "postgres" {
				dialect = filter.PostgresDialect{}
			}
			result, err := filter.NewListQuery(schema, nil, dialect).WithSearchSchema(search).Build(
				filter.ListFilter(`{"status":"active"}`), filter.ListSearch("active"),
				filter.ListStartParam(3), filter.ListWithCount(true), filter.ListFacets("status", "price"),
			)
			require.NoError(t, err)
			require.Contains(t, result.Where, dialect.Placeholder(4), "main fragment preserves the requested offset")
			require.Contains(t, result.CountSQL, dialect.Placeholder(1))
			require.NotContains(t, result.CountSQL, dialect.Placeholder(4))

			var db *sql.DB
			if backend == "postgres" {
				db = dbtest.Postgres(t)
			} else {
				db = dbtest.SQLite(t)
			}
			_, err = db.ExecContext(t.Context(), "CREATE TABLE items (status TEXT, price REAL)")
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), "INSERT INTO items VALUES ('active', 10), ('active', 60), ('inactive', 10)")
			require.NoError(t, err)
			var total int
			require.NoError(t, db.QueryRowContext(t.Context(), result.CountSQL, result.CountArgs...).Scan(&total))
			require.Equal(t, 2, total)
			rows, err := db.QueryContext(t.Context(), result.Facets.SQL, result.Facets.Args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			facets, err := filter.ScanFacetUnion(rows)
			require.NoError(t, err)
			counts := make(map[string]map[string]int64)
			for _, facet := range facets {
				counts[facet.Field] = make(map[string]int64)
				for _, bucket := range facet.Buckets {
					counts[facet.Field][bucket.Key] = bucket.Count
				}
			}
			require.Equal(t, map[string]map[string]int64{"status": {"active": 2}, "price": {"low": 1, "high": 1}}, counts)
		})
	}
}
