//go:build integration

package corpus

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// listWalk pages through a list's rows: page lists the page of at most
// limit rows by sort after cursor (from the start without one), and id
// names a row.
type listWalk[T any] struct {
	page func(t *testing.T, sort string, limit int, cursor string) *filter.Page[T]
	id   func(T) int64
}

// queryWalk walks the pages lq builds, query reading each built page's rows
// (filter.NewPage).
func queryWalk[T any](lq *filter.ListQuery, query func(t *testing.T, res *filter.ListResult) []T, id func(T) int64) listWalk[T] {
	page := func(t *testing.T, sort string, limit int, cursor string) *filter.Page[T] {
		t.Helper()
		opts := []filter.ListOption{filter.ListSort(sort), filter.ListLimit(limit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		p, err := filter.NewPage(query(t, res), res.Limit, res.Cursor, func(r T, column string) (any, error) {
			return filter.CursorValueByTag(r, "db", column)
		})
		require.NoError(t, err, "sort=%s, %d a page", sort, limit)
		return p
	}
	walk := listWalk[T]{page: page, id: id}
	return walk
}

// resourceWalk walks the pages res lists, as its provider lists them. res's
// largest page must hold as many rows as a walk's.
func resourceWalk[T any](res *parse.Resource[T], id func(T) int64) listWalk[T] {
	page := func(t *testing.T, sort string, limit int, cursor string) *filter.Page[T] {
		t.Helper()
		req, err := res.ParseListInput(parse.ListInput{Sort: sort, PageSize: limit, Cursor: cursor})
		require.NoError(t, err)
		return listPage(t, res, req, parse.Scope{})
	}
	walk := listWalk[T]{page: page, id: id}
	return walk
}

// check walks the list by sort at every page size, forwards from the first
// page and backwards from the last page's start cursor, and holds every
// walk to want, the ids in the order the sort gives: no row is repeated or
// skipped.
func (w listWalk[T]) check(t *testing.T, sort string, want []int64) {
	t.Helper()
	for limit := 1; limit <= len(want); limit++ {
		forward, lastStart := w.walk(t, sort, limit, "", false, len(want))
		assert.Equal(t, want, forward, "sort=%s, %d a page, forwards", sort, limit)

		// The rows before the last page, walked back from its start
		// cursor, then the last page, are the forward walk. A single page
		// has no start cursor: nothing is before it.
		last := len(want) % limit
		if last == 0 {
			last = limit
		}
		var backward []int64
		if lastStart != "" {
			backward, _ = w.walk(t, sort, limit, lastStart, true, len(want))
		}
		assert.Equal(t, want, append(backward, want[len(want)-last:]...), "sort=%s, %d a page, backwards", sort, limit)
	}
}

// walk pages from cursor, following end cursors forwards or start cursors
// backwards, and returns the ids in presentation order and, forwards, the
// last page's start cursor. A walk of more pages than rows does not end.
func (w listWalk[T]) walk(t *testing.T, sort string, limit int, cursor string, backward bool, rows int) ([]int64, string) {
	t.Helper()
	var pages [][]int64
	var lastStart string
	for n := 0; ; n++ {
		require.LessOrEqual(t, n, rows, "sort=%s, %d a page: the walk does not end, pages %v", sort, limit, pages)
		page := w.page(t, sort, limit, cursor)
		ids := make([]int64, len(page.Items))
		for i, r := range page.Items {
			ids[i] = w.id(r)
		}
		pages = append(pages, ids)
		lastStart = string(page.PageInfo.StartCursor)
		next, more := page.PageInfo.EndCursor, page.PageInfo.HasNextPage
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

// PostgreSQL orders the codec's text column by its collation; the
// declared key orders and pages by code point, as the cursor compares, on
// every engine.
func codedWalks(t *testing.T, engine Engine) {
	runOn(t, engine, runCodedWalks)
}

// sortCode is an application's codec: a struct its column holds as text.
type sortCode struct{ s string }

func (c sortCode) Value() (driver.Value, error) { return c.s, nil }

// Scan reads the text a column holds, as a resource scans its rows.
func (c *sortCode) Scan(src any) error {
	text, ok := src.(string)
	if !ok {
		return fmt.Errorf("scanning a sortCode: %T is not text", src)
	}
	c.s = text
	return nil
}

// codedItem sorts by a codec its storage tag declares a string.
type codedItem struct {
	ID   int64    `json:"id"   db:"id"`
	Code sortCode `json:"code" db:"code" parse:"sort" storage:"scalar:string"`
}

// runCodedWalks stores codedSeeds in a text column of the database's
// default collation and walks the list by the declared codec, forwards and
// backwards at every page size, holding each walk to the order the cursor
// comparator gives the codes (filter.CompareKeyValues): code points, not
// the collation the column's own ORDER BY follows. The Memory engine walks
// the codec's resource.
func runCodedWalks(t *testing.T, b sqlBackend) {
	// Codes whose code-point order (A B a b z é) is not a collation's
	// (a A b B é z).
	codedSeeds := []string{"B", "a", "A", "b", "é", "z"}
	sorts, err := filter.GenerateSortSchema[codedItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	code := sorts.Fields["code"]
	require.Equal(t, core.FieldTypeString, code.DataType, "the codec sorts as the string it declares")
	table := code.Table
	p, db := b.open(t)
	ids := make([]int64, len(codedSeeds))
	rows := make([][]any, len(codedSeeds))
	for i, s := range codedSeeds {
		ids[i] = int64(i + 1)
		rows[i] = []any{ids[i], sortCode{s}}
	}
	b.load(t, db, sqlTable{name: table, key: []string{"id"}, columns: []sqlColumn{column[int64]("id"), column[string]("code").required()}}, rows)

	key := filter.CursorKey{Name: "code", DataType: code.DataType}
	want := slices.Clone(ids)
	slices.SortStableFunc(want, func(a, b int64) int {
		c, err := filter.CompareKeyValues(key, sortCode{codedSeeds[a-1]}, sortCode{codedSeeds[b-1]})
		require.NoError(t, err)
		return c
	})

	schema := &filter.ResourceSchema{
		Name: table, PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: table, Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
		},
		Fields: map[string]*filter.FieldSchema{},
	}
	id := func(r codedItem) int64 { return r.ID }
	var w listWalk[codedItem]
	if b.engine == Memory {
		w = resourceWalk(parse.MustResource[codedItem](p, table, parse.PageSize(1, len(codedSeeds))), id)
	} else {
		w = queryWalk(mustListQuery(t, schema, sorts, b.dialect), func(t *testing.T, res *filter.ListResult) []codedItem { return queryCoded(t, db, table, res) }, id)
	}
	w.check(t, "code", want)
	descending := slices.Clone(want)
	slices.Reverse(descending)
	w.check(t, "-code", descending)

	if b.engine == Postgres {
		collated := orderedIDs(t, db, "SELECT id FROM "+table+" ORDER BY code, id")
		assert.NotEqual(t, toInts(want), collated, "the column's own collation orders the codes otherwise")
	}
}

func queryCoded(t *testing.T, db *sql.DB, table string, res *filter.ListResult) []codedItem {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT "+table+".id, "+table+".code FROM "+table+" "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []codedItem
	for rows.Next() {
		var (
			r    codedItem
			text string
		)
		require.NoError(t, rows.Scan(&r.ID, &text))
		r.Code = sortCode{text}
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}

func toInts(ids []int64) []int {
	out := make([]int, len(ids))
	for i, id := range ids {
		out[i] = int(id)
	}
	return out
}

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

// SQLite orders a REAL column -Infinity, the finite values, +Infinity,
// and stores NaN as NULL: pages end on the infinities and walk on, and
// the NaN rows page as the NULL ones they are.
func nonFiniteFloatWalk(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	t.Run("nullable", func(t *testing.T) {
		runNonFiniteWalks(t, scoresOn(t, b, core.FieldTypeFloat), true, []int64{3, 7, 9, 4, 2, 8, 1, 5, 6, 10})
	})
	t.Run("not null", func(t *testing.T) {
		runNonFiniteWalks(t, scoresOn(t, b, core.FieldTypeFloat), false, []int64{3, 7, 9, 4, 2, 8})
	})
}

// Evaluate reads a REAL column's infinities as SQLite compares them, and
// its NaN, which SQLite stores as NULL, as the NULL it reads back.
func nonFiniteFloatFilter(t *testing.T, engine Engine) {
	runNonFiniteFilters(t, scoresOn(t, sqlBackendOf(engine), core.FieldTypeFloat))
}

// No NaN or infinity is a facet key, in the statement or in Go.
func nonFiniteFloatFacet(t *testing.T, engine Engine) {
	runNonFiniteFacets(t, scoresOn(t, sqlBackendOf(engine), core.FieldTypeFloat), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
}

// SQLite has no decimal NaN or infinity: a decimal column holds a number
// literal, and parse_decimal reads PostgreSQL's NaN and infinity texts,
// which only a table written outside parse holds, as NULL. A page ending
// on one is a NULL boundary there (the cursor carries the text), so the
// rows page as the NULL ones the statement orders them as, and no facet
// keys them.
func nonFiniteDecimalSQLiteReadsThemAsNull(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	t.Run("walk", func(t *testing.T) {
		runNonFiniteWalks(t, scoresOn(t, b, core.FieldTypeDecimal), true, []int64{9, 4, 1, 2, 3, 5, 6, 7, 8, 10})
	})
	t.Run("facet", func(t *testing.T) {
		runNonFiniteFacets(t, scoresOn(t, b, core.FieldTypeDecimal), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
	})
}

// A NaN boundary, which a database that stores NaN mints, is a NULL one
// where SQLite stores NaN as NULL: the seek resumes after the NULL rows'
// boundary, never at a NULL bound in its place.
func nonFiniteFloatSeekSQLiteNaNIsNull(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	_, err := db.ExecContext(t.Context(), "CREATE TABLE scores (id INTEGER PRIMARY KEY, score REAL)")
	require.NoError(t, err)
	insert := "INSERT INTO scores (id, score) VALUES (" + b.dialect.Placeholder(1) + ", " + b.dialect.Placeholder(2) + ")"
	for _, seed := range floatSeeds() {
		_, err = db.ExecContext(t.Context(), insert, seed.id, seed.score)
		require.NoError(t, err)
	}
	key := filter.CursorKey{Name: "score", Table: "scores", Column: "score", DataType: core.FieldTypeFloat, NumberKind: reflect.Float64, Nullable: true, Direction: filter.SortAsc}
	id := filter.CursorKey{Name: "id", Table: "scores", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64, Direction: filter.SortAsc}
	keys := []filter.CursorKey{key, id}
	for _, tc := range []struct {
		name    string
		reverse bool
		want    []int64
	}{
		{"forward", false, []int64{5, 6, 10}},
		{"reverse", true, []int64{8, 2, 4, 9, 7, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := &filter.PageToken{Reverse: tc.reverse, Fields: []filter.CursorField{
				{Name: "score", Value: math.NaN(), Direction: filter.SortAsc},
				{Name: "id", Value: int64(1), Direction: filter.SortAsc},
			}}
			var args []any
			where, err := filter.SeekPredicate(b.dialect, keys, token, func(v any) string {
				args = append(args, v)
				return b.dialect.Placeholder(len(args))
			})
			require.NoError(t, err)
			got := dbtest.QueryRows[int64](t, db, "SELECT id FROM scores WHERE "+where+" "+filter.OrderByKeys(b.dialect, keys, tc.reverse), args...)
			assert.Equal(t, tc.want, got)
		})
	}
}

// PostgreSQL orders float8 and numeric -Infinity, the finite values,
// +Infinity, NaN, and holds NaN equal to NaN: pages end on each and walk
// on, a nullable column by the expanded seek, a NOT NULL one by the row
// comparison.
func nonFiniteWalk(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	for _, field := range []core.FieldType{core.FieldTypeFloat, core.FieldTypeDecimal} {
		t.Run(string(field)+" nullable", func(t *testing.T) {
			runNonFiniteWalks(t, scoresOn(t, b, field), true, []int64{3, 7, 9, 4, 2, 8, 1, 6, 5, 10})
		})
		t.Run(string(field)+" not null", func(t *testing.T) {
			runNonFiniteWalks(t, scoresOn(t, b, field), false, []int64{3, 7, 9, 4, 2, 8, 1, 6})
		})
	}
}

// Evaluate reads a float8 or numeric column's NaN and infinities as
// PostgreSQL compares them: NaN after +Infinity, so greater than every
// number; and no facet keys one, in the statement or in Go.
func nonFiniteFilterAndFacet(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	for _, field := range []core.FieldType{core.FieldTypeFloat, core.FieldTypeDecimal} {
		t.Run(string(field), func(t *testing.T) {
			runNonFiniteFilters(t, scoresOn(t, b, field))
		})
		t.Run(string(field)+" facet", func(t *testing.T) {
			runNonFiniteFacets(t, scoresOn(t, b, field), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
		})
	}
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

// scoresOn is a fresh database of b whose score column holds field's
// values, declared as b declares a column of field.
func scoresOn(t *testing.T, b sqlBackend, field core.FieldType) scoreDB {
	t.Helper()
	s := scoreDB{db: b.db(t), dialect: b.dialect, field: field}
	s.column = b.columnType(core.TypeInfo{Type: field, NumberKind: s.kind()})
	return s
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
	w := queryWalk(lq, func(t *testing.T, res *filter.ListResult) []scoreRow { return queryScores(t, s.db, res) },
		func(r scoreRow) int64 { return r.ID })
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
			truth, err := filter.Evaluate(node, filter.ColumnRow(func(column filter.ColumnRef) (any, bool) {
				return r.Score, column == filter.ColumnRef{Table: "scores", Column: "score"}
			}))
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
	results := queryFacets(t, s.db, q.SQL, q.Args...)
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

// A float path's members page in the order the engine's ORDER BY gives the
// member, the walk resuming at each boundary without repeating or skipping a
// row. PostgreSQL orders a number member exactly (::numeric), so members
// one float64 apart order apart. SQLite reads it as the INTEGER or REAL
// json_extract returns, so members one float64 apart tie and break on the
// id.
func pathNumberWalk(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		want := parsetest.PostgresMeasureOrder()
		if b.engine == SQLite {
			want = sqliteMeasureOrder()
		}
		runPathNumberWalks(t, measuresOn(t, b), want)
	})
}

// A token an earlier release minted carries the member as a float64, which
// rounds 0.10000000000000001 to 0.1: resuming from the page that ended on
// row 2 returns row 3 (0.1) and row 2 itself again, as that release did.
func pathNumberWalkPostgresResumesAFloat64TokenFromItsRoundedValue(t *testing.T, engine Engine) {
	m := measuresOn(t, sqlBackendOf(engine))
	storeMeasures(t, m)
	lq := mustListQuery(t, measureSchema(), parsetest.MeasureSorts(), m.dialect)
	cursor, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{
		{Name: "n", Value: 0.10000000000000001, Direction: filter.SortAsc},
		{Name: "id", Value: int64(2), Direction: filter.SortAsc},
	}})
	require.NoError(t, err)
	res, err := lq.Build(filter.ListSort("n"), filter.ListLimit(len(parsetest.MeasureSeeds())), filter.ListCursor(cursor))
	require.NoError(t, err)
	var ids []int64
	for _, r := range queryMeasures(t, m.db, res) {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []int64{3, 2, 5, 6, 9, 10, 13, 7, 8, 11, 12}, ids, "the rows after (0.1, 2)")
}

// Every engine agrees with Evaluate on which members are numbers.
// PostgreSQL compares a number member exactly (::numeric, or containment),
// as Evaluate does: 0.10000000000000001 is not 0.1. SQLite decides each
// comparison the REAL json_extract reads decides as the exact value does.
func pathNumberFilters(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		filters := []string{
			`{"n":0.1}`, `{"n":{"$ne":1}}`, `{"n":{"$gt":0.1}}`, `{"n":{"$lt":0.1}}`, `{"n":{"$gte":1}}`, `{"n":{"$lte":0.1}}`,
			`{"n":{"$in":[0.1,1]}}`, `{"n":{"$nin":[0.1,1]}}`, `{"n":{"$null":true}}`, `{"n":{"$null":false}}`,
			`{"n":{"$gte":1e300}}`, `{"n":{"$lte":-1e300}}`, `{"n":{"$gt":0}}`,
			`{"ns":{"$some":{"$eq":0.1}}}`, `{"ns":{"$some":{"$gt":0.1}}}`, `{"ns":{"$contains":0.1}}`, `{"ns":{"$null":true}}`,
		}
		if b.engine == SQLite {
			filters = []string{
				`{"n":{"$null":true}}`, `{"n":{"$null":false}}`, `{"n":{"$gt":0.5}}`, `{"n":{"$lt":0.5}}`,
				`{"n":{"$gte":1e300}}`, `{"n":{"$lte":-1e300}}`, `{"ns":{"$some":{"$gt":0.5}}}`, `{"ns":{"$null":true}}`,
			}
		}
		runPathNumberFilters(t, measuresOn(t, b), filters)
	})
}

// A float path's terms facets count as Go counts the rows the engine reads
// back.
func pathNumberFacets(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) { runPathNumberFacets(t, measuresOn(t, b)) })
}

// sqliteMeasureOrder is the seeds ascending by n as SQLite orders the
// INTEGER or REAL json_extract reads: members one float64 apart tie and
// break on the id, 1e-999 is 0.0 and ±1e400 are ±Inf.
func sqliteMeasureOrder() []int64 {
	order := []int64{14, 15, 1, 2, 3, 4, 5, 6, 9, 10, 13, 7, 8, 11, 12}
	return order
}

// measureDB is a database a document column's float path pages on: b's,
// with p the provider on it.
type measureDB struct {
	b       sqlBackend
	p       *parse.Provider
	db      *sql.DB
	dialect filter.Dialect
}

// measuresOn is a fresh database of b whose measures document column is
// declared as b declares a document.
func measuresOn(t *testing.T, b sqlBackend) measureDB {
	t.Helper()
	p, db := b.open(t)
	m := measureDB{b: b, p: p, db: db, dialect: b.dialect}
	return m
}

// measureItem is a measure as a resource lists it, its doc's paths
// measureSchema's: the Memory engine, which compiles no list, walks it.
type measureItem struct {
	ID  int64           `json:"id"  db:"id"`
	Doc json.RawMessage `json:"doc" db:"doc" paths:"n:float:filter+sort+facet;ns:[]float:filter+facet"`
}

// measureWalk walks the stored measures by the float path n, whose sort
// is named n on a SQL engine and doc.n on the Memory engine's resource.
func measureWalk(t *testing.T, m measureDB) (w listWalk[parsetest.MeasureRow], n string) {
	t.Helper()
	id := func(r parsetest.MeasureRow) int64 { return r.ID }
	if m.b.engine != Memory {
		lq := mustListQuery(t, measureSchema(), parsetest.MeasureSorts(), m.dialect)
		return queryWalk(lq, func(t *testing.T, res *filter.ListResult) []parsetest.MeasureRow { return queryMeasures(t, m.db, res) }, id), "n"
	}
	items := resourceWalk(parse.MustResource[measureItem](m.p, "measures", parse.PageSize(1, len(parsetest.MeasureSeeds()))),
		func(r measureItem) int64 { return r.ID })
	page := func(t *testing.T, sort string, limit int, cursor string) *filter.Page[parsetest.MeasureRow] {
		t.Helper()
		p := items.page(t, sort, limit, cursor)
		rows := make([]parsetest.MeasureRow, len(p.Items))
		for i, it := range p.Items {
			rows[i] = parsetest.MeasureRow{ID: it.ID, Doc: string(it.Doc)}
		}
		measured := &filter.Page[parsetest.MeasureRow]{Items: rows, PageInfo: p.PageInfo}
		return measured
	}
	walk := listWalk[parsetest.MeasureRow]{page: page, id: id}
	return walk, "doc.n"
}

// storedMeasures are the measures as the database reads them back: the
// seeds as the Memory engine, which reads nothing back outside a list,
// was given them.
func storedMeasures(t *testing.T, m measureDB) []parsetest.MeasureRow {
	t.Helper()
	if m.b.engine == Memory {
		return parsetest.MeasureSeeds()
	}
	return queryMeasures(t, m.db, &filter.ListResult{Fragment: "ORDER BY measures.id"})
}

// runPathNumberWalks pages the seeds by the float path n in both
// directions, every page size, forwards and backwards, and holds every walk
// to want: the ids in the order the dialect's ORDER BY gives the member
// ascending, ties on the id.
func runPathNumberWalks(t *testing.T, m measureDB, want []int64) {
	t.Helper()
	storeMeasures(t, m)
	w, n := measureWalk(t, m)
	w.check(t, n, want)
	descending := slices.Clone(want)
	slices.Reverse(descending)
	w.check(t, "-"+n, descending)
}

// storeMeasures stores the seeds in a measures table.
func storeMeasures(t *testing.T, m measureDB) {
	t.Helper()
	seeds := parsetest.MeasureSeeds()
	rows := make([][]any, len(seeds))
	for i, seed := range seeds {
		rows[i] = []any{seed.ID, seed.Doc}
	}
	m.b.load(t, m.db, sqlTable{name: "measures", key: []string{"id"}, columns: []sqlColumn{
		column[int64]("id"), column[json.RawMessage]("doc").required(),
	}}, rows)
}

// measureSchema filters and facets on the float path n and the float array
// path ns.
func measureSchema() *filter.ResourceSchema {
	schema := &filter.ResourceSchema{
		Name: "measures", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "measures", Column: "id", DataType: core.FieldTypeInt},
		},
		Fields: map[string]*filter.FieldSchema{
			"n": {ExternalName: "n", DataType: core.FieldTypeFloat, Path: parsetest.MeasurePath("n"), Facetable: true,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin, filter.OpNull}},
			"ns": {ExternalName: "ns", DataType: core.FieldTypeArray, Elem: core.FieldTypeFloat, Path: parsetest.MeasurePath("ns"), Facetable: true,
				AllowedOperators: []filter.FilterOperator{filter.OpContains, filter.OpSome, filter.OpNone, filter.OpNull}},
		},
	}
	return schema
}

func queryMeasures(t *testing.T, db *sql.DB, res *filter.ListResult) []parsetest.MeasureRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT measures.id, measures.doc FROM measures "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []parsetest.MeasureRow
	for rows.Next() {
		var r parsetest.MeasureRow
		require.NoError(t, rows.Scan(&r.ID, &r.Doc))
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}

// runPathNumberFilters holds filter.Evaluate to the database over the
// seeds: each filter is True on exactly the rows its statement selects, or
// on the Memory engine its list.
func runPathNumberFilters(t *testing.T, m measureDB, filters []string) {
	t.Helper()
	storeMeasures(t, m)
	schema := measureSchema()
	stored := storedMeasures(t, m)
	for _, q := range filters {
		ast, err := filter.NewFilterParser(schema, nil).Parse(q)
		require.NoError(t, err, q)
		selected := selectedMeasures(t, m, schema, ast)
		var evaluated []int64
		for _, r := range stored {
			truth, err := filter.Evaluate(ast.Root, filter.ColumnRow(func(column filter.ColumnRef) (any, bool) {
				return r.Doc, column == filter.ColumnRef{Table: "measures", Column: "doc"}
			}))
			require.NoError(t, err, "%s over %d", q, r.ID)
			if truth == filter.True {
				evaluated = append(evaluated, r.ID)
			}
		}
		require.NotEmpty(t, selected, q)
		assert.Equal(t, selected, evaluated, q)
	}
}

// selectedMeasures are the ids of the measures ast selects, in id order.
func selectedMeasures(t *testing.T, m measureDB, schema *filter.ResourceSchema, ast *filter.FilterAST) []int64 {
	t.Helper()
	var selected []int64
	if m.b.engine == Memory {
		ids, err := m.b.filterIDs(m.db, schema)(t, filter.NewParsedFilter(ast))
		require.NoError(t, err)
		for _, id := range ids {
			n, err := strconv.ParseInt(id, 10, 64)
			require.NoError(t, err)
			selected = append(selected, n)
		}
		slices.Sort(selected)
		return selected
	}
	lq := mustListQuery(t, schema, parsetest.MeasureSorts(), m.dialect)
	res, err := lq.Build(filter.ListFilterAST(ast), filter.ListLimit(len(parsetest.MeasureSeeds())))
	require.NoError(t, err)
	for _, r := range queryMeasures(t, m.db, res) {
		selected = append(selected, r.ID)
	}
	return selected
}

// runPathNumberFacets holds the terms facets of n and ns to the statement's
// count, or the Memory engine's, and to Go's count of the rows the database
// reads back (filter.FacetKeys, filter.FacetBuckets): a member beyond the
// bound is in no bucket.
func runPathNumberFacets(t *testing.T, m measureDB) {
	t.Helper()
	storeMeasures(t, m)
	schema := measureSchema()
	stored := storedMeasures(t, m)
	limit := len(parsetest.MeasureSeeds())
	for _, name := range []string{"n", "ns"} {
		t.Run(name, func(t *testing.T) {
			field := schema.Fields[name]
			results := facetQuery(t, m.db, m.dialect, schema, []filter.FacetRequest{{Field: name, Limit: limit}})
			require.Len(t, results, 1)

			counts := map[string]int64{}
			for _, r := range stored {
				keys, err := filter.FacetKeys(r.Doc, field)
				require.NoError(t, err, "row %d", r.ID)
				for _, k := range keys {
					counts[k]++
				}
			}
			assert.Equal(t, results[0].Buckets, filter.FacetBuckets(field, counts, limit))
		})
	}
}

// visit's nullable columns are database/sql's wrappers, which a column holds
// as their value, NULL while Valid is false. A scalar's default operators
// leave out the NULL tests, as a pointer's do; score and seen_at add them.
type visit struct {
	ID        string        `json:"id"         db:"id"         parse:"filter,sort"`
	Score     sql.NullInt64 `json:"score"      db:"score"      parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte,null,not_null"`
	SeenAt    sql.NullTime  `json:"seen_at"    db:"seen_at"    parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte,null,not_null"`
	DeletedAt sql.NullTime  `json:"deleted_at" db:"deleted_at" parse:"filter"`
}

func visitAt(sec int) sql.NullTime {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	at := sql.NullTime{Time: base.Add(time.Duration(sec) * time.Second), Valid: true}
	return at
}

func visitScore(n int64) sql.NullInt64 {
	score := sql.NullInt64{Int64: n, Valid: true}
	return score
}

// seededVisits are the stored visits: c and e hold NULL scores, b and e
// NULL seen_at, and d is deleted.
func seededVisits() []visit {
	visits := []visit{
		{ID: "a", Score: visitScore(5), SeenAt: visitAt(1)},
		{ID: "b", Score: visitScore(2)},
		{ID: "c", SeenAt: visitAt(3)},
		{ID: "d", Score: visitScore(7), SeenAt: visitAt(2), DeletedAt: visitAt(4)},
		{ID: "e"},
		{ID: "f", Score: visitScore(5), SeenAt: visitAt(5)},
	}
	return visits
}

// A wrapper column means one thing on every engine. SQLite holds a
// timestamp as its canonical text, in a DATETIME column the driver scans
// into the time a sql.NullTime reads.
func nullableWrappers(t *testing.T, engine Engine) {
	runOn(t, engine, runNullableWrappers, on(SQLite, storing(core.FieldTypeTimestamp, "DATETIME")))
}

// visitTable is the visits as b declares them.
func visitTable() sqlTable {
	table := sqlTable{name: "visits", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[sql.NullInt64]("score"), column[sql.NullTime]("seen_at"), column[sql.NullTime]("deleted_at"),
	}}
	return table
}

// runNullableWrappers holds a database and Evaluate to one meaning of a
// wrapper column: a range compares its value, $null its NULL, a sort orders
// NULL greatest and a keyset walk visits every row once, and the soft-delete
// floor admits exactly the rows whose deleted_at is NULL. A SQL engine runs
// the list statements ListQuery compiles; the Memory engine lists the
// visits' resource.
func runNullableWrappers(t *testing.T, b sqlBackend) {
	t.Helper()
	p, db := b.open(t)
	visits := seededVisits()
	b.load(t, db, visitTable(), recordRows(t, visitTable(), visits))

	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[visit](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[visit](g)
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeInt, fs.Fields["score"].DataType)
	assert.Equal(t, core.FieldTypeTimestamp, fs.Fields["seen_at"].DataType)
	assert.Equal(t, reflect.Int64, ss.Fields["score"].NumberKind, "a sort key ranges as its value's kind")
	assert.True(t, ss.Fields["score"].Nullable)
	floor := filter.ComposeConstraints(filter.RepositoryScope{DeleteColumn: "deleted_at"}, nil, nil)
	list := visitLists(t, b, p, db, fs, ss, len(visits))

	t.Run("filters", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			filter string
			policy *filter.PolicyConstraints
			want   string
		}{
			{"a score above", `{"score":{"$gt":3}}`, nil, "adf"},
			{"a score at most", `{"score":{"$lte":2}}`, nil, "b"},
			{"a score that is NULL", `{"score":{"$null":true}}`, nil, "ce"},
			{"a time before", `{"seen_at":{"$lt":"2026-03-04T05:06:10Z"}}`, nil, "ad"},
			{"a time from", `{"seen_at":{"$gte":"2026-03-04T05:06:10Z"}}`, nil, "cf"},
			{"a time that is NULL", `{"seen_at":{"$null":true}}`, nil, "be"},
			{"the live rows", "", floor, "abcef"},
			{"the live rows scoring above", `{"score":{"$gt":3}}`, floor, "af"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, visitIDs(list(t, tt.filter, tt.policy, "id", len(visits), "").Items), "the database")

				ast, err := filter.NewFilterParser(fs, nil).Parse(tt.filter)
				require.NoError(t, err)
				if tt.policy != nil {
					ast, err = filter.ApplyPolicyConstraints(fs, ast, tt.policy)
					require.NoError(t, err)
				}
				var evaluated []visit
				for _, v := range visits {
					truth, err := filter.Evaluate(ast.Root, visitRow(v))
					require.NoError(t, err)
					if truth == filter.True {
						evaluated = append(evaluated, v)
					}
				}
				assert.Equal(t, tt.want, visitIDs(evaluated), "Evaluate")
			})
		}
	})

	t.Run("keyset walks", func(t *testing.T) {
		for _, tt := range []struct {
			sort string
			want string
		}{
			{"score", "bafdce"},
			{"-score", "ecdfab"},
			{"seen_at", "adcfbe"},
			{"-seen_at", "ebfcda"},
		} {
			t.Run(tt.sort, func(t *testing.T) {
				var walked []visit
				cursor := ""
				for range len(visits) + 1 {
					page := list(t, "", nil, tt.sort, 2, cursor)
					walked = append(walked, page.Items...)
					if !page.PageInfo.HasNextPage {
						break
					}
					cursor = string(page.PageInfo.EndCursor)
				}
				assert.Equal(t, tt.want, visitIDs(walked))
			})
		}
	})
}

// visitList lists the visits filter and policy select, by sort, at most
// limit after cursor (from the start without one).
type visitList func(t *testing.T, filter string, policy *filter.PolicyConstraints, sort string, limit int, cursor string) *filter.Page[visit]

// visitLists lists the visits on db: through the statements ListQuery
// compiles of fs and ss on a SQL engine, through a resource of visit on
// p, at most rows a page, on the Memory engine.
func visitLists(t *testing.T, b sqlBackend, p *parse.Provider, db *sql.DB, fs *filter.ResourceSchema, ss *filter.SortSchema, rows int) visitList {
	t.Helper()
	if b.engine == Memory {
		visits := parse.MustResource[visit](p, "visits", parse.PageSize(1, rows))
		return func(t *testing.T, f string, policy *filter.PolicyConstraints, sort string, limit int, cursor string) *filter.Page[visit] {
			t.Helper()
			req, err := visits.ParseListInput(parse.ListInput{Filter: f, Sort: sort, PageSize: limit, Cursor: cursor})
			require.NoError(t, err)
			return listPage(t, visits, req, parse.Scope{Policy: policy})
		}
	}
	lq := mustListQuery(t, fs, ss, b.dialect)
	return func(t *testing.T, f string, policy *filter.PolicyConstraints, sort string, limit int, cursor string) *filter.Page[visit] {
		t.Helper()
		opts := []filter.ListOption{filter.ListFilter(f), filter.ListSort(sort), filter.ListLimit(limit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		if policy != nil {
			opts = append(opts, filter.ListPolicy(policy))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		page, err := filter.NewPage(selectVisits(t, db, res), res.Limit, res.Cursor, func(v visit, column string) (any, error) {
			return filter.CursorValueByTag(v, "db", column)
		})
		require.NoError(t, err)
		return page
	}
}

// selectVisits runs res over the visits table.
func selectVisits(t *testing.T, db *sql.DB, res *filter.ListResult) []visit {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id, score, seen_at, deleted_at FROM visits "+res.Fragment, res.Args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []visit
	for rows.Next() {
		var v visit
		require.NoError(t, rows.Scan(&v.ID, &v.Score, &v.SeenAt, &v.DeletedAt))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}

// visitRow is v as an in-memory store holds it: its Go values by column.
func visitRow(v visit) filter.ColumnRow {
	columns := map[string]any{"id": v.ID, "score": v.Score, "seen_at": v.SeenAt, "deleted_at": v.DeletedAt}
	return func(column filter.ColumnRef) (any, bool) {
		value, ok := columns[column.Column]
		return value, ok && column.Table == "visits"
	}
}

func visitIDs(vs []visit) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString(v.ID)
	}
	return b.String()
}
