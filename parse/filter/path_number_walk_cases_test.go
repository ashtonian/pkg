//go:build unit || integration

package filter_test

import (
	"database/sql"
	"slices"
	"testing"

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
// and 1.0000000000000001, and the two 23-digit integers; a string member
// and an absent one are NULL.
var measureSeeds = []measureRow{
	{1, `{"n": 0.1}`},
	{2, `{"n": 0.10000000000000001}`},
	{3, `{"n": 0.1}`},
	{4, `{"n": 0.09999999999999999999}`},
	{5, `{"n": 1}`},
	{6, `{"n": 1.0000000000000001}`},
	{7, `{"n": "0.5"}`},
	{8, `{}`},
	{9, `{"n": 12345678901234567890123}`},
	{10, `{"n": 12345678901234567890124}`},
}

// postgresMeasureOrder is the seeds ascending by n as PostgreSQL orders
// the member, exactly (::numeric), ties on the id.
var postgresMeasureOrder = []int64{4, 1, 3, 2, 5, 6, 9, 10, 7, 8}

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
	_, err := m.db.ExecContext(t.Context(), "CREATE TABLE measures (id INTEGER PRIMARY KEY, doc "+m.docType+" NOT NULL)")
	require.NoError(t, err)
	insert := "INSERT INTO measures (id, doc) VALUES (" + m.dialect.Placeholder(1) + ", " + m.dialect.Placeholder(2) + ")"
	for _, seed := range measureSeeds {
		_, err = m.db.ExecContext(t.Context(), insert, seed.ID, seed.Doc)
		require.NoError(t, err)
	}
	lq := mustListQuery(t, measureSchema(), measureSorts(m.storage), m.dialect)
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

func measureSchema() *filter.ResourceSchema {
	return &filter.ResourceSchema{
		Name: "measures", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "measures", Column: "id", DataType: core.FieldTypeInt},
		},
		Fields: map[string]*filter.FieldSchema{},
	}
}

func measureSorts(storage filter.JSONStorage) *filter.SortSchema {
	return &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"n": {DataType: core.FieldTypeFloat, Path: &filter.JSONPath{Table: "measures", Column: "doc", Segments: []string{"n"}, Storage: storage}},
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
