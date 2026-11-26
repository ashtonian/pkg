//go:build unit || integration

package filter_test

import (
	"database/sql"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// scoreRow is a row of a float column that holds NaN and the infinities,
// written outside parse: request values are finite, stored ones need not
// be.
type scoreRow struct {
	ID    int64    `db:"id"`
	Score *float64 `db:"score"`
}

// scoreSeeds tie on each non-finite value and on NULL.
var scoreSeeds = []scoreRow{
	{1, new(math.NaN())},
	{2, new(math.Inf(1))},
	{3, new(math.Inf(-1))},
	{4, new(1.5)},
	{5, nil},
	{6, new(math.NaN())},
	{7, new(math.Inf(-1))},
	{8, new(math.Inf(1))},
	{9, new(0.0)},
	{10, nil},
}

// scoreDB is a database a float column pages on.
type scoreDB struct {
	db      *sql.DB
	dialect filter.Dialect
	// column is the float column's SQL type.
	column string
}

// scoreField is the float field the score column is read as.
var scoreField = &filter.FieldSchema{
	ExternalName: "score", Table: "scores", Column: "score", DataType: core.FieldTypeFloat, NumberKind: reflect.Float64,
	AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin},
}

// scoreList stores the seeds ids lists in a scores table, its score
// column nullable or NOT NULL, and lists it, sortable by score.
func scoreList(t *testing.T, s scoreDB, nullable bool, ids []int64) *filter.ListQuery {
	t.Helper()
	null := " NOT NULL"
	if nullable {
		null = ""
	}
	_, err := s.db.ExecContext(t.Context(), "CREATE TABLE scores (id INTEGER PRIMARY KEY, score "+s.column+null+")")
	require.NoError(t, err)
	insert := "INSERT INTO scores (id, score) VALUES (" + s.dialect.Placeholder(1) + ", " + s.dialect.Placeholder(2) + ")"
	for _, seed := range scoreSeeds {
		if slices.Contains(ids, seed.ID) {
			_, err = s.db.ExecContext(t.Context(), insert, seed.ID, seed.Score)
			require.NoError(t, err, "storing %d", seed.ID)
		}
	}
	schema := &filter.ResourceSchema{
		Name: "scores", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "scores", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
		},
		Fields: map[string]*filter.FieldSchema{"score": scoreField},
	}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"score": {Table: "scores", Column: "score", DataType: core.FieldTypeFloat, NumberKind: reflect.Float64, Nullable: nullable},
	}}
	return mustListQuery(t, schema, sorts, s.dialect)
}

// runNonFiniteWalks pages a float column holding NaN, +Inf and -Inf by
// score in both directions, every page size, forwards from the first page
// and backwards from the last, and holds every walk to want: the ids in
// the order the dialect's ORDER BY gives the column ascending, ties on the
// id. Only the seeds want lists are stored; nullable declares the column
// nullable.
func runNonFiniteWalks(t *testing.T, s scoreDB, nullable bool, want []int64) {
	t.Helper()
	lq := scoreList(t, s, nullable, want)

	descending := slices.Clone(want)
	slices.Reverse(descending)
	for _, tc := range []struct {
		sort string
		want []int64
	}{{"score", want}, {"-score", descending}} {
		for limit := 1; limit <= len(want); limit++ {
			forward, lastStart := walkScores(t, s.db, lq, tc.sort, limit, "", false)
			assert.Equal(t, tc.want, forward, "sort=%s, %d a page, forwards", tc.sort, limit)

			// The rows before the last page, walked back from its start
			// cursor, then the last page, are the forward walk. A single
			// page has no start cursor: nothing is before it.
			last := len(tc.want) % limit
			if last == 0 {
				last = limit
			}
			var backward []int64
			if lastStart != "" {
				backward, _ = walkScores(t, s.db, lq, tc.sort, limit, lastStart, true)
			}
			assert.Equal(t, tc.want, append(backward, tc.want[len(tc.want)-last:]...), "sort=%s, %d a page, backwards", tc.sort, limit)
		}
	}
}

// runNonFiniteFilters holds filter.Evaluate to the database over a float
// column holding NaN, +Inf and -Inf: over the rows as the database reads
// them back, each filter is True on exactly the rows its statement
// selects.
func runNonFiniteFilters(t *testing.T, s scoreDB) {
	t.Helper()
	ids := make([]int64, len(scoreSeeds))
	for i, seed := range scoreSeeds {
		ids[i] = seed.ID
	}
	lq := scoreList(t, s, true, ids)
	stored := queryScores(t, s.db, &filter.ListResult{Fragment: "ORDER BY scores.id"})
	for _, node := range []*filter.FilterNode{
		{Field: scoreField, Op: filter.OpEq, Value: 1.5},
		{Field: scoreField, Op: filter.OpNe, Value: 1.5},
		{Field: scoreField, Op: filter.OpGt, Value: 1.5},
		{Field: scoreField, Op: filter.OpGte, Value: 0.0},
		{Field: scoreField, Op: filter.OpLt, Value: 1.5},
		{Field: scoreField, Op: filter.OpLte, Value: 0.0},
		{Field: scoreField, Op: filter.OpIn, Value: []any{1.5, 0.0}},
		{Field: scoreField, Op: filter.OpNin, Value: []any{1.5, 0.0}},
	} {
		res, err := lq.Build(filter.ListFilterAST(&filter.FilterAST{Root: node}), filter.ListLimit(len(scoreSeeds)))
		require.NoError(t, err)
		var selected []int64
		for _, r := range queryScores(t, s.db, res) {
			selected = append(selected, r.ID)
		}
		var evaluated []int64
		for _, r := range stored {
			truth, err := filter.Evaluate(node, filter.ColumnRow(func(column string) (any, bool) { return r.Score, column == "scores.score" }))
			require.NoError(t, err, "$%s %v over %d", node.Op, node.Value, r.ID)
			if truth == filter.True {
				evaluated = append(evaluated, r.ID)
			}
		}
		require.NotEmpty(t, selected, "$%s %v", node.Op, node.Value)
		assert.Equal(t, selected, evaluated, "$%s %v", node.Op, node.Value)
	}
}

// walkScores pages from cursor, following end cursors forwards or start
// cursors backwards, and returns the ids in presentation order and,
// forwards, the last page's start cursor.
func walkScores(t *testing.T, db *sql.DB, lq *filter.ListQuery, sort string, limit int, cursor string, backward bool) ([]int64, string) {
	t.Helper()
	var pages [][]int64
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, len(scoreSeeds)+2, "the walk does not end")
		opts := []filter.ListOption{filter.ListSort(sort), filter.ListLimit(limit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		items := queryScores(t, db, res)
		page, err := filter.NewPage(items, res.Limit, res.Cursor, func(r scoreRow, column string) (any, error) {
			return filter.CursorValueByTag(r, "db", column)
		})
		require.NoError(t, err, "sort=%s, %d a page", sort, limit)
		ids := make([]int64, len(page.Items))
		for i, r := range page.Items {
			ids[i] = r.ID
		}
		pages = append(pages, ids)
		lastStart = string(page.PageInfo.StartCursor)
		next := page.PageInfo.EndCursor
		more := page.PageInfo.HasNextPage
		if backward {
			next, more = page.PageInfo.StartCursor, page.PageInfo.HasPrevPage
		}
		if !more {
			break
		}
		cursor = string(next)
	}
	if backward {
		slices.Reverse(pages)
	}
	return slices.Concat(pages...), lastStart
}

func queryScores(t *testing.T, db *sql.DB, res *filter.ListResult) []scoreRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT scores.id, scores.score FROM scores "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []scoreRow
	for rows.Next() {
		var r scoreRow
		require.NoError(t, rows.Scan(&r.ID, &r.Score))
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}
