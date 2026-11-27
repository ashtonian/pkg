//go:build integration

package corpus

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// A column with a linguistic collation still walks in code-point order —
// the order the memory backend and filters use — page after page, under
// either code-point collation (pg_c_utf8, the default, and "C" for
// PostgreSQL 14–16), and a string range filter seeks in the same order.
func keysetWalkLinguisticCollationWalksByCodePoint(t *testing.T, engine Engine) {
	schema := &filter.ResourceSchema{Name: "probe", PrimaryKey: []string{"id"}, Fields: map[string]*filter.FieldSchema{
		"name": {ExternalName: "name", Table: "probe", Column: "name", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpGt}},
	}}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {Table: "probe", Column: "name", DataType: core.FieldTypeString},
		"id":   {Table: "probe", Column: "id", DataType: core.FieldTypeInt},
	}}
	for name, dialect := range map[string]filter.PostgresDialect{
		"pg_c_utf8": {},
		"C":         {Collation: filter.CollationC},
	} {
		t.Run(name, func(t *testing.T) {
			db := sqlBackendOf(engine).db(t)
			_, err := db.ExecContext(t.Context(), `CREATE TABLE probe (id INT PRIMARY KEY, name TEXT NOT NULL COLLATE "en-US-x-icu")`)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), `INSERT INTO probe VALUES (1, 'b'), (2, 'B'), (3, 'a'), (4, 'A'), (5, 'é'), (6, 'z')`)
			require.NoError(t, err)
			lq, err := filter.NewListQuery(schema, sorts, dialect)
			require.NoError(t, err)

			var walked []string
			cursor := ""
			for range 6 {
				opts := []filter.ListOption{filter.ListSort("name"), filter.ListLimit(1)}
				if cursor != "" {
					opts = append(opts, filter.ListCursor(cursor))
				}
				q, err := lq.Build(opts...)
				require.NoError(t, err)
				rows := queryNames(t, db, "SELECT id, name FROM probe "+q.Fragment, q.Args...)
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
			memory, err := filter.CompareKeyValues(filter.CursorKey{DataType: core.FieldTypeString}, "B", "a")
			require.NoError(t, err)
			assert.Negative(t, memory, "memory orders the same way")

			q, err := lq.Build(filter.ListFilter(`{"name":{"$gt":"b"}}`), filter.ListSort("name"))
			require.NoError(t, err)
			above := parsetest.ItemIDs(queryNames(t, db, "SELECT id, name FROM probe "+q.Fragment, q.Args...), func(r namedRow) string { return r.name })
			assert.Equal(t, []string{"z", "é"}, above)
		})
	}
}

type namedRow struct {
	id   int64
	name string
}

// queryNames reads rows of an integer id and a text name.
func queryNames(t *testing.T, db *sql.DB, q string, args ...any) []namedRow {
	t.Helper()
	var out []namedRow
	for _, row := range dbtest.QueryRows[[]any](t, db, q, args...) {
		id, idOK := row[0].(int64)
		name, nameOK := row[1].(string)
		require.True(t, idOK && nameOK, "row %v", row)
		out = append(out, namedRow{id: id, name: name})
	}
	return out
}

// trackItem is a generated-schema entity: table-qualified sort expressions,
// a UUID primary key, nullable keys (pointers and sql.Null types), a
// timestamp key and a decimal key.
type trackItem struct {
	ID        uuid.UUID        `json:"id"         db:"id"         parse:"filter,sort"`
	Rank      *int64           `json:"rank"       db:"rank"       parse:"filter,sort"`
	Score     sql.NullInt64    `json:"score"      db:"score"      parse:"filter,sort"`
	Name      string           `json:"name"       db:"name"       parse:"filter,sort"`
	CreatedAt time.Time        `json:"created_at" db:"created_at" parse:"filter,sort"`
	SeenAt    sql.NullTime     `json:"seen_at"    db:"seen_at"    parse:"filter,sort"`
	Price     *decimal.Decimal `json:"price"      db:"price"      parse:"filter,sort"`
}

// A seed's score is its rank and its seen_at its created_at, NULL where its
// rank is: the sql.Null keys order as the pointer key does.
type trackSeed struct {
	id      string
	rank    *int64
	name    string
	created int     // seconds after the base instant
	price   *string // decimal text, inserted as written
}

// trackSeeds ids sort a < b < … < g in both dialects. Prices order
// differently as numbers than as text ("10.25" < "9.5"), and d and g hold
// equal prices at different scales.
func trackSeeds() []trackSeed {
	seeds := []trackSeed{
		{id: "a", rank: new(int64(1)), name: "e", created: 5, price: new("10.25")},
		{id: "b", rank: new(int64(2)), name: "d", created: 1, price: new("9.5")},
		{id: "c", rank: nil, name: "c", created: 3, price: new("-1")},
		{id: "d", rank: new(int64(2)), name: "b", created: 4, price: new("2.00")},
		{id: "e", rank: nil, name: "a", created: 2, price: new("100")},
		{id: "f", rank: new(int64(3)), name: "g", created: 6, price: nil},
		{id: "g", rank: nil, name: "f", created: 0, price: new("2")},
	}
	return seeds
}

// Expected presentation order per sort, NULL greatest, ties on id in the
// primary direction.
func trackOrders() map[string]string {
	orders := map[string]string{
		"":            "abcdefg",
		"rank":        "abdfceg",
		"-rank":       "gecfdba",
		"score":       "abdfceg",
		"-score":      "gecfdba",
		"seen_at":     "bdafceg",
		"-seen_at":    "gecfadb",
		"seen_at,id":  "bdafceg",
		"rank,-name":  "abdfgce",
		"-created_at": "fadcebg",
		"name":        "edcbagf",
		"price":       "cdgbaef",
		"-price":      "feabgdc",
	}
	return orders
}

// trackID maps a row letter to a UUID whose last hex digit preserves the
// letter order (a → 1 … g → 7).
func trackID(s string) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-00000000000%x", s[0]-'a'+1))
}

func trackBase() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
}

// Every generated sort walks forwards and backwards over exactly the rows,
// in the expected order, on each backend: UUID text keys, NULL keys, mixed
// directions, timestamps and decimals included. SQLite's decimals walk over
// both numeric affinities a decimal column may have, NUMERIC (INTEGER or
// REAL per value) and REAL; PostgreSQL's native uuid, timestamptz and
// numeric columns place NULLs otherwise by default.
func keysetWalk(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Postgres {
			runTrackWalks(t, b)
			return
		}
		for _, priceType := range []string{"NUMERIC", "REAL"} {
			t.Run("price "+priceType, func(t *testing.T) { runTrackWalks(t, b.with(storing(core.FieldTypeDecimal, priceType))) })
		}
	})
}

// trackTable holds trackSeeds; SQLite's seen_at is a DATETIME column, which
// holds the canonical text as written.
func trackTable() sqlTable {
	table := sqlTable{name: "track_items", key: []string{"id"}, columns: []sqlColumn{
		column[uuid.UUID]("id"),
		column[int64]("rank"),
		column[int64]("score"),
		column[string]("name").required(),
		column[time.Time]("created_at").required(),
		column[time.Time]("seen_at").holding(SQLite, "DATETIME"),
		column[decimal.Decimal]("price"),
	}}
	return table
}

func runTrackWalks(t *testing.T, b sqlBackend) {
	t.Helper()
	prov, db := b.open(t)
	seeds := trackSeeds()
	rows := make([][]any, len(seeds))
	for i, s := range seeds {
		rows[i] = []any{trackID(s.id), s.rank, s.rank, s.name, trackTime(s), trackSeen(s), s.price}
	}
	b.load(t, db, trackTable(), rows)
	p, err := parse.NewResource[trackItem](prov, "track_items")
	require.NoError(t, err)

	for sortParam, want := range trackOrders() {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := walkTrack(t, b.dialect, p, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")

			// Walk back from the last page's start cursor; the rows seen,
			// followed by the last page, must reproduce the forward order.
			lastPage := forward[len(forward)-lastPageLen(len(forward)):]
			backward, _ := walkTrack(t, b.dialect, p, sortParam, lastStart, true)
			assert.Equal(t, want, backward+lastPage, "backward walk")
		})
	}
}

const trackLimit = 2

func lastPageLen(n int) int {
	r := n % trackLimit
	if r != 0 {
		return r
	}
	return trackLimit
}

// walkTrack pages through the list from cursor, following end cursors
// forwards or start cursors backwards, and returns the ids seen in
// presentation order (plus, going forwards, the last page's start cursor).
func walkTrack(t *testing.T, d filter.Dialect, p *parse.Resource[trackItem], sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	lq, err := p.ListQuery()
	require.NoError(t, err)
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		opts := []filter.ListOption{filter.ListSort(sortParam), filter.ListLimit(trackLimit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		items := queryTrack(t, d, p, res)
		page, err := filter.NewPage(items, res.Limit, res.Cursor, func(it trackItem, col string) (any, error) {
			return filter.CursorValueByTag(it, "db", col)
		})
		require.NoError(t, err)
		var ids strings.Builder
		for _, it := range page.Items {
			ids.WriteString(trackSuffix(it.ID))
		}
		pages = append(pages, ids.String())
		lastStart = string(page.PageInfo.StartCursor)
		if backward {
			if !page.PageInfo.HasPrevPage {
				break
			}
			cursor = string(page.PageInfo.StartCursor)
		} else {
			if !page.PageInfo.HasNextPage {
				break
			}
			cursor = string(page.PageInfo.EndCursor)
		}
	}
	var out strings.Builder
	if backward {
		for _, page := range slices.Backward(pages) {
			out.WriteString(page)
		}
	} else {
		for _, pg := range pages {
			out.WriteString(pg)
		}
	}
	return out.String(), lastStart
}

func trackSuffix(id uuid.UUID) string {
	return string(rune('a' + id[15] - 1))
}

// queryTrack reads res's page of track_items on p's provider.
func queryTrack(t *testing.T, d filter.Dialect, p *parse.Resource[trackItem], res *filter.ListResult) []trackItem {
	t.Helper()
	st := parse.Statement{SQL: "SELECT " + parsetest.JoinColumns(p.Columns()) + " FROM " + d.QuoteIdent("track_items") + " " + res.Fragment, Args: res.Args}
	return parsetest.ResourceRows(t, p, st)
}

func trackTime(s trackSeed) time.Time {
	return trackBase().Add(time.Duration(s.created) * time.Second)
}

// trackSeen is a seed's seen_at: its created_at, or NULL where its rank is.
func trackSeen(s trackSeed) *time.Time {
	if s.rank == nil {
		return nil
	}
	at := trackTime(s)
	return &at
}

// walkPage is the page size walks take: small enough that every fixture
// order crosses several pages.
const walkPage = 2

// Keyset walks over path sort keys on each backend, PathRecords paged by
// every PathOrders sort.
func pathWalks(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, _ := loadPathRecords(t, b, pathSchema(t, ""))
		r, err := parse.NewResource[filtertest.PathRecord](p, "path_records", parse.PageSize(walkPage, 10))
		require.NoError(t, err)
		walkOrders(t, walker[filtertest.PathRecord]{resource: r,
			id: func(r filtertest.PathRecord) string { return r.ID }}, filtertest.PathOrders)
	})
}

// walker lists a resource's rows on its provider; id names a row.
type walker[T any] struct {
	resource *parse.Resource[T]
	id       func(T) string
}

// walkOrders pages through w's rows by every sort in orders, walkPage rows
// at a time, forwards and then backwards from the last page's start
// cursor; both walks must give the order (row ids) the sort promises.
func walkOrders[T any](t *testing.T, w walker[T], orders map[string]string) {
	t.Helper()
	for sortParam, want := range orders {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := w.walk(t, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")
			last := len(forward) % walkPage
			if last == 0 {
				last = walkPage
			}
			backward, _ := w.walk(t, sortParam, lastStart, true)
			assert.Equal(t, want, backward+forward[len(forward)-last:], "backward walk")
		})
	}
}

func (w walker[T]) walk(t *testing.T, sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 20, "walk did not terminate")
		q := url.Values{"sort": {sortParam}, "page_size": {strconv.Itoa(walkPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := w.resource.ParseList(q)
		require.NoError(t, err)
		page := listPage(t, w.resource, req, parse.Scope{})
		var ids strings.Builder
		for _, it := range page.Items {
			ids.WriteString(w.id(it))
		}
		pages = append(pages, ids.String())
		lastStart = string(page.PageInfo.StartCursor)
		if backward {
			if !page.PageInfo.HasPrevPage {
				break
			}
			cursor = string(page.PageInfo.StartCursor)
		} else {
			if !page.PageInfo.HasNextPage {
				break
			}
			cursor = string(page.PageInfo.EndCursor)
		}
	}
	out := ""
	if backward {
		for _, page := range slices.Backward(pages) {
			out += page
		}
		return out, lastStart
	}
	for _, s := range pages {
		out += s
	}
	return out, lastStart
}

// Keyset walks over computed and member keys, the document column jsonb on
// PostgreSQL and JSON text on SQLite.
func keysetRender(t *testing.T, engine Engine) {
	runOn(t, engine, runKeysetRender)
}

// renderSeed is a row the rendered keyset walks: its id, the score its
// computed rank doubles and its document.
type renderSeed struct {
	id    string
	score float64
	doc   string
}

// renderSeeds tie on the computed rank and on the member n, which some
// documents spell differently, hold as null, or lack.
func renderSeeds() []renderSeed {
	seeds := []renderSeed{
		{"a", 0.25, `{"n": 10}`},
		{"b", 0.45, `{"n": 2.5}`},
		{"c", 0.25, `{}`},
		{"d", 0.05, `{"n": 10.0}`},
		{"e", 0.45, `{"n": null}`},
		{"f", 0.25, `{"n": -1}`},
		{"g", 0.15, `{"n": 1e1}`},
	}
	return seeds
}

// renderRank is a computed key, the score doubled, which no sort schema
// declares: a statement outside ListQuery orders and seeks by it.
const renderRank = "(items.score * 2)"

// runKeysetRender walks each keyset two rows at a time with OrderByKeys
// and SeekPredicate, resuming from a token minted at each page's last row,
// and checks the walk visits every row once, in the keyset's order.
func runKeysetRender(t *testing.T, b sqlBackend) {
	t.Helper()
	ctx := t.Context()
	db := b.db(t)
	_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE items (id TEXT PRIMARY KEY, score DOUBLE PRECISION NOT NULL, doc %s NOT NULL)", b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})))
	require.NoError(t, err)
	seeds := renderSeeds()
	for _, s := range seeds {
		_, err = db.ExecContext(ctx, fmt.Sprintf("INSERT INTO items (id, score, doc) VALUES (%s, %s, %s)",
			b.dialect.Placeholder(1), b.dialect.Placeholder(2), b.dialect.Placeholder(3)), s.id, s.score, s.doc)
		require.NoError(t, err)
	}

	rank := filter.CursorKey{Name: "rank", Expr: renderRank, Column: "rank", DataType: core.FieldTypeFloat}
	n := filter.CursorKey{Name: "n", Column: "doc", DataType: core.FieldTypeDecimal, Nullable: true,
		Path: &filter.JSONPath{Table: "items", Column: "doc", Segments: []string{"n"}, Storage: filter.StorageJSONB}}
	id := filter.CursorKey{Name: "id", Table: "items", Column: "id", DataType: core.FieldTypeString}
	keyset := func(pairs ...any) []filter.CursorKey {
		var keys []filter.CursorKey
		for i := 0; i < len(pairs); i += 2 {
			k := pairs[i].(filter.CursorKey)
			k.Direction = pairs[i+1].(filter.SortDirection)
			keys = append(keys, k)
		}
		return keys
	}

	for _, tt := range []struct {
		name string
		keys []filter.CursorKey
		want string
	}{
		{"a computed key descending, then the id ascending", keyset(rank, filter.SortDesc, id, filter.SortAsc), "beacfgd"},
		{"a member ascending, nulls last", keyset(n, filter.SortAsc, id, filter.SortAsc), "fbadgce"},
		{"a member descending, nulls first", keyset(n, filter.SortDesc, id, filter.SortDesc), "ecgdabf"},
		{"a computed key, a member and the id", keyset(rank, filter.SortAsc, n, filter.SortDesc, id, filter.SortAsc), "dgcafeb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var walked strings.Builder
			var token *filter.PageToken
			for range len(seeds) + 1 {
				rows := renderPage(t, db, b.dialect, tt.keys, token, 2)
				for _, row := range rows {
					walked.WriteString(row["id"].(string))
				}
				if len(rows) < 2 {
					break
				}
				last := rows[len(rows)-1]
				token, err = filter.CursorPlan{Keys: tt.keys}.Token(false, func(column string) (any, error) {
					return last[column], nil
				})
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, walked.String())
		})
	}
}

// renderPage is the page of at most limit rows after token (from the
// start without one), in the order of keys.
func renderPage(t *testing.T, db *sql.DB, d filter.Dialect, keys []filter.CursorKey, token *filter.PageToken, limit int) []map[string]any {
	t.Helper()
	where := "1=1"
	var args []any
	if token != nil {
		var err error
		where, err = filter.SeekPredicate(d, keys, token, func(v any) string {
			args = append(args, v)
			return d.Placeholder(len(args))
		})
		require.NoError(t, err)
	}
	query := fmt.Sprintf("SELECT items.id, items.doc, %s AS rank FROM items WHERE %s %s LIMIT %d",
		renderRank, where, filter.OrderByKeys(d, keys, false), limit)
	rows, err := db.QueryContext(t.Context(), query, args...)
	require.NoError(t, err, query)
	defer func() { require.NoError(t, rows.Close()) }()
	var page []map[string]any
	for rows.Next() {
		var id, doc string
		var rank float64
		require.NoError(t, rows.Scan(&id, &doc, &rank))
		page = append(page, map[string]any{"id": id, "doc": doc, "rank": rank})
	}
	require.NoError(t, rows.Err())
	return page
}

// eventRow is a row of the events whose created_at three rows share.
type eventRow struct {
	ID        int64  `db:"id"`
	CreatedAt string `db:"created_at"`
}

// 5 rows with 3 sharing a created_at boundary value, page size 2; every
// page walked through the emitted cursor returns the exact set, the key
// breaking the ties.
func integrationCursorWalkNoLossNoDup(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		table := sqlTable{name: "events", key: []string{"id"}, columns: []sqlColumn{column[int64]("id"), column[string]("created_at").required()}}
		b.load(t, db, table, recordRows(t, table, []eventRow{
			{1, "2026-01-01T00:00:00Z"},
			{2, "2026-01-02T00:00:00Z"}, // ┐
			{3, "2026-01-02T00:00:00Z"}, // ├ 3 rows share the boundary value
			{4, "2026-01-02T00:00:00Z"}, // ┘
			{5, "2026-01-03T00:00:00Z"},
		}))

		schema := &filter.ResourceSchema{
			Name: "events", PrimaryKey: []string{"id"},
			SystemFields: map[string]*filter.FieldSchema{
				"id": {ExternalName: "id", Table: "events", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
			},
			Fields: map[string]*filter.FieldSchema{
				"created_at": {ExternalName: "created_at", Table: "events", Column: "created_at", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpGt, filter.OpLt}},
			},
		}
		sortSchema := &filter.SortSchema{
			Fields: map[string]*filter.SortFieldSchema{
				"created_at": {Table: "events", Column: "created_at"},
			},
		}
		w := listWalk[eventRow]{
			lq:    mustListQuery(t, schema, sortSchema, b.dialect),
			query: func(t *testing.T, res *filter.ListResult) []eventRow { return queryEvents(t, db, res) },
			id:    func(r eventRow) int64 { return r.ID },
		}
		for _, sort := range []string{"-created_at", "created_at"} {
			t.Run(sort, func(t *testing.T) {
				got, _ := w.walk(t, sort, 2, "", false, 5)
				assert.ElementsMatch(t, []int64{1, 2, 3, 4, 5}, got, "every row once across the walk")
			})
		}
	})
}

// queryEvents is the events res selects.
func queryEvents(t *testing.T, db *sql.DB, res *filter.ListResult) []eventRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT events.id, events.created_at FROM events "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []eventRow
	for rows.Next() {
		var r eventRow
		require.NoError(t, rows.Scan(&r.ID, &r.CreatedAt))
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}

type walkItem struct {
	ID   int64  `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
}

// walkItemList walks the walk_items rows by lq.
func walkItemList(db *sql.DB, lq *filter.ListQuery) listWalk[walkItem] {
	w := listWalk[walkItem]{
		lq:    lq,
		query: func(t *testing.T, res *filter.ListResult) []walkItem { return walkItems(t, db, res) },
		id:    func(it walkItem) int64 { return it.ID },
	}
	return w
}

// Six rows with one name walk as six rows two at a time: the key breaks
// every tie. A cursor whose key value the key's type cannot hold is E2082
// before any statement exists.
func listQueryKeyBreaksTies(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		dbtest.Exec(t, db,
			`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
			`INSERT INTO walk_items VALUES (1,'x'), (2,'x'), (3,'x'), (4,'x'), (5,'x'), (6,'x')`)
		g := core.NewSchemaGenerator()
		fs, err := filter.GenerateFilterSchema[walkItem](g)
		require.NoError(t, err)
		ss, err := filter.GenerateSortSchema[walkItem](g)
		require.NoError(t, err)
		_, err = filter.NewListQuery(fs, ss, b.dialect)
		require.ErrorContains(t, err, "declares no key", "a generated schema declares no key until its resource does")
		fs.PrimaryKey = []string{"id"}
		lq := mustListQuery(t, fs, ss, b.dialect)

		seen, _ := walkItemList(db, lq).walk(t, "name", 2, "", false, 6)
		assert.Equal(t, []int64{1, 2, 3, 4, 5, 6}, seen)

		for _, value := range []any{"zzz", true, 1.5, "a\x00b"} {
			cur := mintCursor(t, false,
				filter.CursorField{Name: "name", Value: "x", Direction: filter.SortAsc},
				filter.CursorField{Name: "id", Value: value, Direction: filter.SortAsc})
			_, err := lq.Build(filter.ListSort("name"), filter.ListCursor(cur))
			requireParseCode(t, err, filter.ECodeInvalidCursor)
		}
	})
}

type stockItem struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort"`
}

// A key of two columns ends every keyset with each column the sort does not
// name, in key order and in the sort's primary direction, so rows that tie
// on the sort and on either column alone still page apart: every sort, both
// directions, every page size, forwards and back, reads each row once. A
// cursor minted under another key is E2082.
func listQueryCompositeKeyBreaksTies(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		rows := []stockItem{{"w1", "a", 5}, {"w1", "b", 5}, {"w2", "a", 5}, {"w2", "b", 1}, {"w3", "a", 1}}
		b.load(t, db, stockTable(), recordRows(t, stockTable(), rows))
		g := core.NewSchemaGenerator()
		fs, err := filter.GenerateFilterSchema[stockItem](g)
		require.NoError(t, err)
		ss, err := filter.GenerateSortSchema[stockItem](g)
		require.NoError(t, err)
		fs.PrimaryKey = []string{"warehouse_id", "sku"}
		lq := mustListQuery(t, fs, ss, b.dialect)

		keyset := func(sort string) []string {
			res, err := lq.Build(filter.ListSort(sort))
			require.NoError(t, err)
			names := make([]string, len(res.Cursor.Keys))
			for i, k := range res.Cursor.Keys {
				names[i] = k.Name + " " + string(k.Direction)
			}
			return names
		}
		assert.Equal(t, []string{"qty desc", "warehouse_id desc", "sku desc"}, keyset("-qty"))
		assert.Equal(t, []string{"sku asc", "warehouse_id asc"}, keyset("sku"), "a key column the sort names ends it once")

		fetch := func(res *filter.ListResult) []stockItem {
			rs, err := db.QueryContext(t.Context(), "SELECT warehouse_id, sku, qty FROM stock_items "+res.Fragment, res.Args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rs.Close()) }()
			var items []stockItem
			for rs.Next() {
				var it stockItem
				require.NoError(t, rs.Scan(&it.WarehouseID, &it.SKU, &it.Qty))
				items = append(items, it)
			}
			require.NoError(t, rs.Err())
			return items
		}
		build := func(opts ...filter.ListOption) *filter.ListResult {
			res, err := lq.Build(opts...)
			require.NoError(t, err)
			return res
		}
		page := func(res *filter.ListResult) *filter.Page[stockItem] {
			p, err := filter.NewPage(fetch(res), res.Limit, res.Cursor, func(it stockItem, col string) (any, error) {
				return filter.CursorValueByTag(it, "db", col)
			})
			require.NoError(t, err)
			return p
		}
		for _, sort := range []string{"", "qty", "-qty", "sku", "-sku", "warehouse_id", "-warehouse_id", "qty,-sku"} {
			want := fetch(build(filter.ListSort(sort), filter.ListLimit(len(rows))))
			require.Len(t, want, len(rows))
			for size := 1; size <= len(rows); size++ {
				t.Run(fmt.Sprintf("sort=%q/size=%d", sort, size), func(t *testing.T) {
					var forward []stockItem
					var last *filter.Page[stockItem]
					cursor := ""
					for range len(rows) + 1 {
						opts := []filter.ListOption{filter.ListSort(sort), filter.ListLimit(size)}
						if cursor != "" {
							opts = append(opts, filter.ListCursor(cursor))
						}
						last = page(build(opts...))
						forward = append(forward, last.Items...)
						if !last.PageInfo.HasNextPage {
							break
						}
						cursor = string(last.PageInfo.EndCursor)
					}
					assert.Equal(t, want, forward)

					backward := []stockItem{}
					for cursor = string(last.PageInfo.StartCursor); cursor != ""; {
						prev := page(build(filter.ListSort(sort), filter.ListLimit(size), filter.ListCursor(cursor)))
						backward = append(slices.Clone(prev.Items), backward...)
						cursor = ""
						if prev.PageInfo.HasPrevPage {
							cursor = string(prev.PageInfo.StartCursor)
						}
					}
					assert.Equal(t, want[:len(want)-len(last.Items)], backward, "walking back from the last page")
				})
			}
		}

		oneColumn := mintCursor(t, false,
			filter.CursorField{Name: "qty", Value: int64(5), Direction: filter.SortAsc},
			filter.CursorField{Name: "warehouse_id", Value: "w1", Direction: filter.SortAsc})
		_, err = lq.Build(filter.ListSort("qty"), filter.ListCursor(oneColumn))
		requireParseCode(t, err, filter.ECodeInvalidCursor)
	})
}

// A text key the column declares NOCASE still walks by code point, every
// row once in each direction: the operand's BINARY collation decides each
// comparison of the row-value seek.
func keysetSeekSQLiteNoCaseKeyWalksByCodePoint(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	dbtest.Exec(t, db,
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE NOT NULL)`,
		`INSERT INTO walk_items VALUES (1, 'b'), (2, 'B'), (3, 'a'), (4, 'C'), (5, 'b'), (6, 'A')`)
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	w := walkItemList(db, mustListQuery(t, fs, ss, b.dialect))

	for _, tt := range []struct {
		sort string
		want []int64
	}{
		{"name", []int64{6, 2, 4, 3, 1, 5}},
		{"-name", []int64{5, 1, 3, 4, 2, 6}},
	} {
		t.Run(tt.sort, func(t *testing.T) {
			seen, _ := w.walk(t, tt.sort, 1, "", false, len(tt.want))
			assert.Equal(t, tt.want, seen)
		})
	}
}

// noCaseNames reads the name column through an expression of its own
// collation, as a mapper serving case-insensitive lookups might.
type noCaseNames struct{}

func (noCaseNames) MapColumn(table, column string, _ core.FieldType) string {
	expr := `"` + table + `"."` + column + `"`
	if column == "name" {
		return expr + " COLLATE NOCASE"
	}
	return expr
}

// A key whose expression carries its own collation still walks by code
// point, every row once in each direction: the seek collates the
// expression, where an explicit collation on the operand would lose to the
// expression's.
func keysetSeekSQLiteCollatedExpressionWalksByCodePoint(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	dbtest.Exec(t, db,
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO walk_items VALUES (1, 'B'), (2, 'a'), (3, 'C')`)
	g := core.NewSchemaGenerator(core.WithColumnMapper(noCaseNames{}), core.WithSQLTableName("walk_items"))
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	require.Equal(t, `"walk_items"."name" COLLATE NOCASE`, ss.Fields["name"].Expr)
	w := walkItemList(db, mustListQuery(t, fs, ss, b.dialect))

	for _, tt := range []struct {
		sort string
		want []int64
	}{
		{"name", []int64{1, 3, 2}},
		{"-name", []int64{2, 3, 1}},
	} {
		t.Run(tt.sort, func(t *testing.T) {
			seen, _ := w.walk(t, tt.sort, 1, "", false, len(tt.want))
			assert.Equal(t, tt.want, seen)
		})
	}
}

// walkItems are the walk_items rows res selects.
func walkItems(t *testing.T, db *sql.DB, res *filter.ListResult) []walkItem {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id, name FROM walk_items "+res.Fragment, res.Args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []walkItem
	for rows.Next() {
		var it walkItem
		require.NoError(t, rows.Scan(&it.ID, &it.Name))
		items = append(items, it)
	}
	require.NoError(t, rows.Err())
	return items
}

// mintCursor is the cursor of a token holding fields.
func mintCursor(t *testing.T, reverse bool, fields ...filter.CursorField) string {
	t.Helper()
	cursor, err := filter.EncodeCursor(&filter.PageToken{Fields: fields, Reverse: reverse})
	require.NoError(t, err)
	return cursor
}

// requireParseCode requires err to be a *core.ParseError of code.
func requireParseCode(t *testing.T, err error, code core.Code) {
	t.Helper()
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	require.Equal(t, code.String(), pe.Code, "%v", err)
}
