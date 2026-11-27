//go:build unit || integration

package filter_test

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// measureRow is a row whose document's number member n a float path sorts
// by.
type measureRow struct {
	ID  int64  `db:"id"`
	Doc string `db:"doc"`
}

// measureSeeds hold members that differ beyond float64 precision: 0.1,
// 0.10000000000000001 and 0.09999999999999999999 are one float64, as are 1
// and 1.0000000000000001, and the two 23-digit integers; and members at the
// bound of a number member (core.MaxDecimalDigits digits in plain
// notation): 1e-999 and ±1e400 are within it, 1e-1000 and a 1001-digit
// integer beyond it, so they are NULL, as a string member and an absent one
// are. ns holds elements on either side of float64 precision and of the
// bound.
var measureSeeds = []measureRow{
	{1, `{"n": 0.1, "ns": [0.1, 1e-1000]}`},
	{2, `{"n": 0.10000000000000001, "ns": [0.10000000000000001]}`},
	{3, `{"n": 0.1}`},
	{4, `{"n": 0.09999999999999999999}`},
	{5, `{"n": 1}`},
	{6, `{"n": 1.0000000000000001}`},
	{7, `{"n": "0.5"}`},
	{8, `{}`},
	{9, `{"n": 12345678901234567890123}`},
	{10, `{"n": 12345678901234567890124}`},
	{11, `{"n": 1e-1000, "ns": [1e-1000]}`},
	{12, `{"n": 1` + strings.Repeat("0", core.MaxDecimalDigits) + `}`},
	{13, `{"n": 1e400, "ns": [1e400]}`},
	{14, `{"n": -1e400}`},
	{15, `{"n": 1e-999}`},
}

// postgresMeasureOrder is the seeds ascending by n as PostgreSQL orders
// the member, exactly (::numeric), ties on the id.
var postgresMeasureOrder = []int64{14, 15, 4, 1, 3, 2, 5, 6, 9, 10, 13, 7, 8, 11, 12}

// sqliteMeasureOrder is the seeds ascending by n as SQLite orders the
// INTEGER or REAL json_extract reads: members one float64 apart tie and
// break on the id, 1e-999 is 0.0 and ±1e400 are ±Inf.
var sqliteMeasureOrder = []int64{14, 15, 1, 2, 3, 4, 5, 6, 9, 10, 13, 7, 8, 11, 12}

// measureDB is a database a document column's float path pages on.
type measureDB struct {
	db      *sql.DB
	dialect filter.Dialect
	docType string
	storage filter.JSONStorage
}

// runPathNumberWalks pages the seeds by the float path n in both
// directions, every page size, forwards and backwards, and holds every walk
// to want: the ids in the order the dialect's ORDER BY gives the member
// ascending, ties on the id.
func runPathNumberWalks(t *testing.T, m measureDB, want []int64) {
	t.Helper()
	storeMeasures(t, m)
	lq := mustListQuery(t, measureSchema(m.storage), measureSorts(m.storage), m.dialect)
	w := listWalk[measureRow]{
		lq:    lq,
		query: func(t *testing.T, res *filter.ListResult) []measureRow { return queryMeasures(t, m.db, res) },
		id:    func(r measureRow) int64 { return r.ID },
	}
	w.check(t, "n", want)
	descending := slices.Clone(want)
	slices.Reverse(descending)
	w.check(t, "-n", descending)
}

// storeMeasures stores the seeds in a measures table.
func storeMeasures(t *testing.T, m measureDB) {
	t.Helper()
	_, err := m.db.ExecContext(t.Context(), "CREATE TABLE measures (id INTEGER PRIMARY KEY, doc "+m.docType+" NOT NULL)")
	require.NoError(t, err)
	insert := "INSERT INTO measures (id, doc) VALUES (" + m.dialect.Placeholder(1) + ", " + m.dialect.Placeholder(2) + ")"
	for _, seed := range measureSeeds {
		_, err = m.db.ExecContext(t.Context(), insert, seed.ID, seed.Doc)
		require.NoError(t, err)
	}
}

// measureSchema filters and facets on the float path n and the float array
// path ns.
func measureSchema(storage filter.JSONStorage) *filter.ResourceSchema {
	return &filter.ResourceSchema{
		Name: "measures", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "measures", Column: "id", DataType: core.FieldTypeInt},
		},
		Fields: map[string]*filter.FieldSchema{
			"n": {ExternalName: "n", DataType: core.FieldTypeFloat, Path: measurePath(storage, "n"), Facetable: true,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin, filter.OpNull}},
			"ns": {ExternalName: "ns", DataType: core.FieldTypeArray, Elem: core.FieldTypeFloat, Path: measurePath(storage, "ns"), Facetable: true,
				AllowedOperators: []filter.FilterOperator{filter.OpContains, filter.OpSome, filter.OpNone, filter.OpNull}},
		},
	}
}

func measurePath(storage filter.JSONStorage, member string) *filter.JSONPath {
	return &filter.JSONPath{Table: "measures", Column: "doc", Segments: []string{member}, Storage: storage}
}

func measureSorts(storage filter.JSONStorage) *filter.SortSchema {
	return &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"n": {DataType: core.FieldTypeFloat, Path: measurePath(storage, "n")},
	}}
}

func queryMeasures(t *testing.T, db *sql.DB, res *filter.ListResult) []measureRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT measures.id, measures.doc FROM measures "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []measureRow
	for rows.Next() {
		var r measureRow
		require.NoError(t, rows.Scan(&r.ID, &r.Doc))
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}

// runPathNumberFilters holds filter.Evaluate to the database over the
// seeds: each filter is True on exactly the rows its statement selects.
func runPathNumberFilters(t *testing.T, m measureDB, filters []string) {
	t.Helper()
	storeMeasures(t, m)
	schema := measureSchema(m.storage)
	lq := mustListQuery(t, schema, measureSorts(m.storage), m.dialect)
	stored := queryMeasures(t, m.db, &filter.ListResult{Fragment: "ORDER BY measures.id"})
	for _, q := range filters {
		ast, err := filter.NewFilterParser(schema, nil).Parse(q)
		require.NoError(t, err, q)
		res, err := lq.Build(filter.ListFilterAST(ast), filter.ListLimit(len(measureSeeds)))
		require.NoError(t, err, q)
		var selected []int64
		for _, r := range queryMeasures(t, m.db, res) {
			selected = append(selected, r.ID)
		}
		var evaluated []int64
		for _, r := range stored {
			truth, err := filter.Evaluate(ast.Root, filter.ColumnRow(func(column string) (any, bool) { return r.Doc, column == "measures.doc" }))
			require.NoError(t, err, "%s over %d", q, r.ID)
			if truth == filter.True {
				evaluated = append(evaluated, r.ID)
			}
		}
		require.NotEmpty(t, selected, q)
		assert.Equal(t, selected, evaluated, q)
	}
}

// runPathNumberFacets holds the terms facets of n and ns to the statement's
// count and to Go's count of the rows the database reads back
// (filter.FacetKeys, filter.FacetBuckets): a member beyond the bound is in
// no bucket.
func runPathNumberFacets(t *testing.T, m measureDB) {
	t.Helper()
	storeMeasures(t, m)
	schema := measureSchema(m.storage)
	stored := queryMeasures(t, m.db, &filter.ListResult{Fragment: "ORDER BY measures.id"})
	for _, name := range []string{"n", "ns"} {
		field := schema.Fields[name]
		q, err := filter.BuildFacetQuery(schema, m.dialect, "1=1", nil, []filter.FacetRequest{{Field: name, Limit: len(measureSeeds)}})
		require.NoError(t, err)
		rows, err := m.db.QueryContext(t.Context(), q.SQL, q.Args...)
		require.NoError(t, err)
		results, err := filter.ScanFacetUnion(rows)
		require.NoError(t, rows.Close())
		require.NoError(t, err)
		require.Len(t, results, 1)

		counts := map[string]int64{}
		for _, r := range stored {
			keys, err := filter.FacetKeys(r.Doc, field)
			require.NoError(t, err, "%s row %d", name, r.ID)
			for _, k := range keys {
				counts[k]++
			}
		}
		assert.Equal(t, results[0].Buckets, filter.FacetBuckets(field, counts, len(measureSeeds)), name)
	}
}
