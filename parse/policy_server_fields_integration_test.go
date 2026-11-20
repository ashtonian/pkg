//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

var serverFieldFixture = []string{
	`CREATE TABLE documents (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, region_code TEXT NOT NULL, title TEXT NOT NULL, folder_id INTEGER NOT NULL)`,
	`INSERT INTO documents VALUES (1, 7, 'eu', 'alpha', 1), (2, 7, 'us', 'beta', 1), (3, 8, 'eu', 'alpha', 2), (4, 7, 'eu', 'gamma', 2)`,
}

// serverFieldRows are serverFieldFixture's rows as a memory backend holds
// them, by column.
var serverFieldRows = []map[string]any{
	{"id": 1, "owner_id": 7, "region_code": "eu", "title": "alpha", "folder_id": 1},
	{"id": 2, "owner_id": 7, "region_code": "us", "title": "beta", "folder_id": 1},
	{"id": 3, "owner_id": 8, "region_code": "eu", "title": "alpha", "folder_id": 2},
	{"id": 4, "owner_id": 7, "region_code": "eu", "title": "gamma", "folder_id": 2},
}

// ownerEUPolicy holds documents to owner 7's in the eu region, through a
// hidden column and a column without the filter capability.
func ownerEUPolicy() parse.Scope {
	return policyOn(
		&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: 7},
		&filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "eu"},
	)
}

func TestPolicyServerFields_SQLite(t *testing.T) {
	runPolicyServerFields(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}

func TestPolicyServerFields_Postgres(t *testing.T) {
	runPolicyServerFields(t, dbtest.Postgres(t), filter.PostgresDialect{})
}

func runPolicyServerFields(t *testing.T, db *sql.DB, d filter.Dialect) {
	for _, stmt := range serverFieldFixture {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	docs := serverFieldDocuments(t)
	req, err := docs.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamFacets: {"title"}})
	require.NoError(t, err)
	plan, err := docs.BuildList(d, req, ownerEUPolicy())
	require.NoError(t, err)

	var ids []string
	for _, row := range queryRows(t, db, plan.Page.SQL, plan.Page.Args) {
		ids = append(ids, fmt.Sprint(row[0]))
	}
	assert.Equal(t, []string{"1", "4"}, ids)
	assert.Equal(t, "2", fmt.Sprint(queryRows(t, db, plan.Count.SQL, plan.Count.Args)[0][0]))

	rows, err := db.Query(plan.Facets.SQL, plan.Facets.Args...)
	require.NoError(t, err)
	facets, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, f := range facets {
		for _, b := range f.Buckets {
			counts[fmt.Sprint(b.Key)] = b.Count
		}
	}
	assert.Equal(t, map[string]int64{"alpha": 1, "gamma": 1}, counts, "the facet counts the policy's population")
}

// TestPolicyServerFields_Memory evaluates the same policy over the rows as
// a memory backend holds them, with the reference evaluator.
func TestPolicyServerFields_Memory(t *testing.T) {
	docs := serverFieldDocuments(t)
	scope := ownerEUPolicy()
	ast, err := filter.ApplyPolicyConstraints(docs.FilterSchema(), nil, scope.Policy)
	require.NoError(t, err)
	var ids []int
	for _, row := range serverFieldRows {
		truth, err := filter.Evaluate(ast.Root, filter.ColumnRow(func(column string) (any, bool) {
			v, ok := row[column[len("documents."):]]
			return v, ok
		}))
		require.NoError(t, err)
		if truth == filter.True {
			ids = append(ids, row["id"].(int))
		}
	}
	slices.Sort(ids)
	assert.Equal(t, []int{1, 4}, ids)
}
