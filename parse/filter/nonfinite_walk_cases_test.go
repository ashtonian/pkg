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

// scoreRow is a row of a number column that holds NaN and the infinities,
// written outside parse: request values are finite, stored ones need not
// be. Score is the value as the driver scans it.
type scoreRow struct {
	ID    int64 `db:"id"`
	Score any   `db:"score"`
}

// nonFiniteSeed is a row to store: its id and score, nil for NULL.
type nonFiniteSeed struct {
	id    int64
	score any
}

// The seeds tie on each non-finite value and on NULL: a float column's as
// float64s, a decimal column's as the text PostgreSQL's numeric writes.
func floatSeeds() []nonFiniteSeed {
	seeds := []nonFiniteSeed{
		{1, math.NaN()}, {2, math.Inf(1)}, {3, math.Inf(-1)}, {4, 1.5}, {5, nil},
		{6, math.NaN()}, {7, math.Inf(-1)}, {8, math.Inf(1)}, {9, 0.0}, {10, nil},
	}
	return seeds
}

func decimalSeeds() []nonFiniteSeed {
	seeds := []nonFiniteSeed{
		{1, "NaN"}, {2, "Infinity"}, {3, "-Infinity"}, {4, "1.5"}, {5, nil},
		{6, "NaN"}, {7, "-Infinity"}, {8, "Infinity"}, {9, "0"}, {10, nil},
	}
	return seeds
}

// scoreDB is a database a number column of a float or decimal field pages
// on.
type scoreDB struct {
	db      *sql.DB
	dialect filter.Dialect
	// column is the number column's SQL type.
	column string
	// field is the field's type: float or decimal.
	field core.FieldType
}

// seeds are the rows a column of the field's type holds.
func (s scoreDB) seeds() []nonFiniteSeed {
	if s.field == core.FieldTypeDecimal {
		return decimalSeeds()
	}
	return floatSeeds()
}

// scoreField is the field the score column is read as.
func (s scoreDB) scoreField() *filter.FieldSchema {
	field := &filter.FieldSchema{
		ExternalName: "score", Table: "scores", Column: "score", DataType: s.field, NumberKind: s.kind(), Facetable: true,
		AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin},
	}
	return field
}

func (s scoreDB) kind() reflect.Kind {
	if s.field == core.FieldTypeFloat {
		return reflect.Float64
	}
	return reflect.Invalid
}

// operand is n as a filter value of the field's type: a float, or a
// decimal's canonical text.
func (s scoreDB) operand(n float64) any {
	if s.field == core.FieldTypeDecimal {
		text, err := core.DecimalText(n)
		if err != nil {
			panic(err)
		}
		return text
	}
	return n
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
	for _, seed := range s.seeds() {
		if slices.Contains(ids, seed.id) {
			_, err = s.db.ExecContext(t.Context(), insert, seed.id, seed.score)
			require.NoError(t, err, "storing %d", seed.id)
		}
	}
	schema := &filter.ResourceSchema{
		Name: "scores", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "scores", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
		},
		Fields: map[string]*filter.FieldSchema{"score": s.scoreField()},
	}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"score": {Table: "scores", Column: "score", DataType: s.field, NumberKind: s.kind(), Nullable: nullable},
	}}
	return mustListQuery(t, schema, sorts, s.dialect)
}

// allIDs are every seed's id.
func allIDs(s scoreDB) []int64 {
	ids := make([]int64, len(s.seeds()))
	for i, seed := range s.seeds() {
		ids[i] = seed.id
	}
	return ids
}

// runNonFiniteWalks pages a number column holding NaN, +Inf and -Inf by
// score in both directions, every page size, forwards from the first page
// and backwards from the last, and holds every walk to want: the ids in
// the order the dialect's ORDER BY gives the column ascending, ties on the
// id. Only the seeds want lists are stored; nullable declares the column
// nullable.
func runNonFiniteWalks(t *testing.T, s scoreDB, nullable bool, want []int64) {
	t.Helper()
	lq := scoreList(t, s, nullable, want)
	w := listWalk[scoreRow]{
		lq:    lq,
		query: func(t *testing.T, res *filter.ListResult) []scoreRow { return queryScores(t, s.db, res) },
		id:    func(r scoreRow) int64 { return r.ID },
	}
	w.check(t, "score", want)
	descending := slices.Clone(want)
	slices.Reverse(descending)
	w.check(t, "-score", descending)
}

// runNonFiniteFilters holds filter.Evaluate to the database over a number
// column holding NaN, +Inf and -Inf: over the rows as the database reads
// them back, each filter is True on exactly the rows its statement
// selects.
func runNonFiniteFilters(t *testing.T, s scoreDB) {
	t.Helper()
	lq := scoreList(t, s, true, allIDs(s))
	field := s.scoreField()
	stored := queryScores(t, s.db, &filter.ListResult{Fragment: "ORDER BY scores.id"})
	for _, node := range []*filter.FilterNode{
		{Field: field, Op: filter.OpEq, Value: s.operand(1.5)},
		{Field: field, Op: filter.OpNe, Value: s.operand(1.5)},
		{Field: field, Op: filter.OpGt, Value: s.operand(1.5)},
		{Field: field, Op: filter.OpGte, Value: s.operand(0)},
		{Field: field, Op: filter.OpLt, Value: s.operand(1.5)},
		{Field: field, Op: filter.OpLte, Value: s.operand(0)},
		{Field: field, Op: filter.OpIn, Value: []any{s.operand(1.5), s.operand(0)}},
		{Field: field, Op: filter.OpNin, Value: []any{s.operand(1.5), s.operand(0)}},
	} {
		res, err := lq.Build(filter.ListFilterAST(&filter.FilterAST{Root: node}), filter.ListLimit(len(s.seeds())))
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

// runNonFiniteFacets holds a terms facet on a number column holding NaN,
// +Inf and -Inf to want, its buckets as the statement counts them and as
// Go counts the rows the database reads back (filter.FacetKeys,
// filter.FacetBuckets): no NaN or infinity is a key.
func runNonFiniteFacets(t *testing.T, s scoreDB, want []filter.FacetBucket) {
	t.Helper()
	scoreList(t, s, true, allIDs(s))
	field := s.scoreField()
	schema := &filter.ResourceSchema{Name: "scores", Fields: map[string]*filter.FieldSchema{"score": field}}
	q, err := filter.BuildFacetQuery(schema, s.dialect, "1=1", nil, []filter.FacetRequest{{Field: "score", Limit: len(s.seeds())}})
	require.NoError(t, err)
	rows, err := s.db.QueryContext(t.Context(), q.SQL, q.Args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err)
	require.NoError(t, rows.Err())
	require.Len(t, results, 1)
	assert.Equal(t, want, results[0].Buckets, "the statement's facet")

	counts := map[string]int64{}
	for _, r := range queryScores(t, s.db, &filter.ListResult{Fragment: "ORDER BY scores.id"}) {
		keys, err := filter.FacetKeys(r.Score, field)
		require.NoError(t, err, "row %d", r.ID)
		for _, k := range keys {
			counts[k]++
		}
	}
	assert.Equal(t, want, filter.FacetBuckets(field, counts, len(s.seeds())), "the facet counted in Go")
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
