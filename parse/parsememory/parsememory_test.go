//go:build unit

package parsememory_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsememory"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

type note struct {
	ID        int64     `json:"id"         db:"id"         parse:"filter,sort"`
	Title     string    `json:"title"      db:"title"      parse:"filter,search,patch,facet,sort" facet:"terms"`
	Slug      string    `json:"slug"       db:"slug"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// noteTitle is the narrower resource another part of an application
// builds on the notes table.
type noteTitle struct {
	ID    int64  `json:"id"    db:"id"    parse:"filter,sort"`
	Title string `json:"title" db:"title" parse:"filter"`
}

// open opens a database of maxRows rows a table, closed when the test
// ends.
func open(t *testing.T, maxRows int) *parsememory.Database {
	t.Helper()
	db, err := parsememory.Open(t.Context(), parsememory.Config{MaxRows: maxRows})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func notesOn(t *testing.T, db *parsememory.Database) *parse.Resource[note] {
	t.Helper()
	notes, err := parse.NewResource[note](db.Provider(), "notes")
	require.NoError(t, err)
	return notes
}

// list is the ids of every note r lists, in key order.
func list(t *testing.T, r *parse.Resource[note]) []int64 {
	t.Helper()
	return listIDs(t, r, func(n note) int64 { return n.ID })
}

// listIDs is the id of every row r lists, in key order.
func listIDs[T any](t *testing.T, r *parse.Resource[T], id func(T) int64) []int64 {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamPageSize: {"100"}})
	require.NoError(t, err)
	page, err := r.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	ids := make([]int64, len(page.Items))
	for i, item := range page.Items {
		ids[i] = id(item)
	}
	return ids
}

// row is a note holding every column a note reads.
func row(id int64, title string) parsememory.Row {
	r := parsememory.Row{"id": id, "title": title, "slug": "", "updated_at": time.Unix(id, 0)}
	return r
}

// The configuration's default opens a database a service runs on with no
// setting given, and Open refuses a setting out of range.
func TestOpen_DefaultAndRange(t *testing.T) {
	field, ok := reflect.TypeFor[parsememory.Config]().FieldByName("MaxRows")
	require.True(t, ok)
	def, err := strconv.Atoi(field.Tag.Get("default"))
	require.NoError(t, err)
	assert.Equal(t, 100000, def)
	db := open(t, def)
	assert.Equal(t, parsememory.Name, db.Provider().Name())
	require.NoError(t, db.Ping(t.Context()))

	for _, maxRows := range []int{0, -1} {
		opened, err := parsememory.Open(t.Context(), parsememory.Config{MaxRows: maxRows})
		require.EqualError(t, err, "parsememory: max_rows: "+strconv.Itoa(maxRows)+": must be at least 1")
		assert.Nil(t, opened)
	}
}

// parseprovider opens the memory provider by its name, and refuses another
// name, naming the one the binary links.
func TestChoice_OpensByName(t *testing.T) {
	cfg := parsememory.Config{MaxRows: 10}
	opened, err := parseprovider.Open(t.Context(), parsememory.Name, parsememory.Choice(cfg))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	db, ok := opened.(*parsememory.Database)
	require.True(t, ok, "the opened database is the provider's own, for Put and Begin")
	require.NoError(t, db.Ping(t.Context()))

	_, err = parseprovider.Open(t.Context(), "sqlite", parsememory.Choice(cfg))
	require.EqualError(t, err, `parseprovider: provider: "sqlite": this binary links memory`)
	_, err = parseprovider.Open(t.Context(), parsememory.Name, parsememory.Choice(parsememory.Config{}))
	require.EqualError(t, err, "parsememory: max_rows: 0: must be at least 1")
}

// requireUnsupported asserts err is the memory provider's refusal of
// capability for the notes resource.
func requireUnsupported(t *testing.T, err error, capability string) {
	t.Helper()
	require.ErrorIs(t, err, parse.ErrUnsupported)
	unsupported, ok := errors.AsType[*parse.UnsupportedError](err)
	require.True(t, ok)
	assert.Equal(t, parsememory.Name, unsupported.Provider)
	assert.Equal(t, capability, unsupported.Capability)
	require.EqualError(t, err, "parse: notes: the memory provider does not support "+capability)
}

// The provider compiles no SQL: everything that needs SQL is refused with
// a typed error before anything runs, and nothing is written.
func TestCapabilities_RefuseWhatNeedsSQL(t *testing.T) {
	db := open(t, 10)
	_, err := parse.NewResource[note](db.Provider(), "notes", parse.ReadExpr("slug", parse.SQL("lower(title)")))
	requireUnsupported(t, err, "computed columns")

	notes := notesOn(t, db)
	require.NoError(t, db.Put(t.Context(), "notes", row(1, "first")))
	assert.Nil(t, notes.Columns(), "no projection without a dialect")
	patch, err := notes.ParseMergePatch([]byte(`{"title":"x"}`))
	require.NoError(t, err)
	search, err := notes.ParseList(url.Values{"search": {"x"}})
	require.NoError(t, err)
	facets, err := notes.ParseFacets(url.Values{"facets": {"title"}})
	require.NoError(t, err)

	t.Run("operations", func(t *testing.T) {
		ctx := t.Context()
		_, err := notes.Query(ctx, parse.Statement{SQL: "SELECT 1"})
		requireUnsupported(t, err, "Query")
		_, err = notes.List(ctx, search, parse.Scope{})
		requireUnsupported(t, err, "search")
		_, err = notes.Facets(ctx, search, parse.Scope{})
		requireUnsupported(t, err, "search")
		_, err = notes.Update(ctx, patch, parse.ByID(int64(1)), parse.Scope{}, parse.StampSQL("updated_at", "CURRENT_TIMESTAMP"))
		requireUnsupported(t, err, "StampSQL")
		_, err = notes.UpdateBatch(ctx, []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{}, parse.StampSQL("updated_at", "CURRENT_TIMESTAMP"))
		requireUnsupported(t, err, "StampSQL")
	})

	t.Run("SQL builders", func(t *testing.T) {
		_, err := notes.BuildList(nil, parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildFacets(facets, parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.IncludeQueries(&parse.ListPlan{}, []parse.Key{{int64(1)}})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.Page(nil, &parse.ListPlan{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.ListQuery()
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildUpdate(patch, parse.ByID(int64(1)), parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{})
		requireUnsupported(t, err, "SQL builders")
	})

	t.Run("a raw SQL policy", func(t *testing.T) {
		raw := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{RawSQL: "notes.title <> $1", RawArgs: []any{"x"}}}}
		_, err := notes.List(t.Context(), nil, parse.Scope{Policy: raw})
		requireUnsupported(t, err, "raw SQL policy constraints")
		_, err = notes.Facets(t.Context(), facets, parse.Scope{Policy: raw})
		requireUnsupported(t, err, "raw SQL policy constraints")
	})

	assert.Equal(t, []int64{1}, list(t, notes))
}

// Resources on one table declare it together: each adds the columns its
// type reads, and a column declared after rows were written reads as NULL
// in them. Resources that disagree on the key or on a column's type are
// refused, and the refused one declares nothing.
func TestDeclare_ResourcesShareATable(t *testing.T) {
	type keyedByTitle struct {
		Title string `json:"title" db:"title" parse:"filter"`
	}
	type titleAsNumber struct {
		ID    int64 `json:"id"    db:"id"`
		Title int64 `json:"title" db:"title"`
	}
	type withColor struct {
		ID    int64   `json:"id"    db:"id"    parse:"filter,sort"`
		Color *string `json:"color" db:"color" parse:"filter"`
	}
	db := open(t, 10)
	_, err := parse.NewResource[noteTitle](db.Provider(), "notes")
	require.NoError(t, err)
	require.NoError(t, db.Put(t.Context(), "notes", parsememory.Row{"id": int64(1), "title": "first"}))
	require.ErrorContains(t, db.Put(t.Context(), "notes", parsememory.Row{"id": int64(2), "slug": "s"}), "no resource declares the column slug")

	notes := notesOn(t, db)
	require.NoError(t, db.Put(t.Context(), "notes", row(2, "second")))
	titles, err := parse.NewResource[noteTitle](db.Provider(), "notes")
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, listIDs(t, titles, func(n noteTitle) int64 { return n.ID }))
	_, err = notes.List(t.Context(), nil, parse.Scope{})
	require.ErrorContains(t, err, "column slug: converting NULL to string is unsupported", "a NULL reads into a field as database/sql reads it")

	_, err = parse.NewResource[keyedByTitle](db.Provider(), "notes", parse.PrimaryKey("title"))
	require.ErrorIs(t, err, parsememory.ErrTableMismatch)
	require.ErrorContains(t, err, `parse: notes: declaring the table: parsememory: resources disagree on a table: table "notes": `)
	_, err = parse.NewResource[titleAsNumber](db.Provider(), "notes")
	require.ErrorIs(t, err, parsememory.ErrTableMismatch)
	require.ErrorContains(t, err, `column "title" is int (int64), declared before as string`)

	colored, err := parse.NewResource[withColor](db.Provider(), "notes")
	require.NoError(t, err)
	page, err := colored.List(t.Context(), nil, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Nil(t, page.Items[0].Color, "a column declared after its rows were written reads as NULL")

	// A Valuer's column stores its driver value unless it is declared JSON,
	// which stores its JSON text, so the two declarations disagree.
	_, err = parse.NewResource[located](db.Provider(), "places")
	require.NoError(t, err)
	_, err = parse.NewResource[located](db.Provider(), "places", parse.ColumnType("at", core.SQLJSONB))
	require.ErrorIs(t, err, parsememory.ErrTableMismatch)
	require.ErrorContains(t, err, `column "at"`)
}

// spot is a document its driver value writes as text.
type spot struct{ X, Y int }

func (s spot) Value() (driver.Value, error) { return strconv.Itoa(s.X) + "," + strconv.Itoa(s.Y), nil }

type located struct {
	ID int64 `json:"id" db:"id" parse:"filter,sort"`
	At spot  `json:"at" db:"at"`
}

// Put and Delete write only what a resource declared, and each call writes
// all of its rows or none.
func TestPut_WritesAllOrNothing(t *testing.T) {
	db := open(t, 10)
	notes := notesOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "notes", row(2, "b"), row(1, "a")))

	for _, tc := range []struct {
		name string
		put  func() error
		want string
	}{
		{"an undeclared table", func() error { return db.Put(ctx, "drafts", row(3, "c")) }, `parsememory: putting into "drafts": no resource declares the table`},
		{"an undeclared column", func() error {
			return db.Put(ctx, "notes", row(3, "c"), parsememory.Row{"id": int64(4), "body": "x", "author": "y"})
		}, `parsememory: putting into "notes": row 1: no resource declares the column author, body`},
		{"no key", func() error { return db.Put(ctx, "notes", row(3, "c"), parsememory.Row{"title": "d"}) }, `parsememory: putting into "notes": row 1: key column id: NULL`},
		{"a value its column cannot hold", func() error { return db.Put(ctx, "notes", row(3, "c"), parsememory.Row{"id": "four"}) }, `parsememory: putting into "notes": row 1: column id:`},
		{"a timestamp that is no time", func() error {
			return db.Put(ctx, "notes", parsememory.Row{"id": int64(3), "updated_at": "yesterday"})
		}, `parsememory: putting into "notes": row 0: column updated_at:`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, tc.put(), tc.want)
			assert.Equal(t, []int64{1, 2}, list(t, notes), "the refused call wrote nothing")
		})
	}

	require.NoError(t, db.Put(ctx, "notes", row(1, "replaced")))
	page, err := notes.List(ctx, nil, parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, "replaced", page.Items[0].Title, "a row replaces the row of its key")

	require.NoError(t, db.Delete(ctx, "notes", parse.Key{int64(2)}, parse.Key{int64(9)}))
	assert.Equal(t, []int64{1}, list(t, notes), "a key no row holds deletes nothing")
	require.ErrorContains(t, db.Delete(ctx, "notes", parse.Key{"x"}), `parsememory: deleting from "notes": key 0:`)
	require.ErrorContains(t, db.Delete(ctx, "drafts", parse.Key{int64(1)}), `parsememory: deleting from "drafts": no resource declares the table`)
}

// A write past MaxRows fails with ErrRowLimit and keeps nothing it wrote;
// in a transaction it ends the transaction, keeping nothing the
// transaction wrote.
func TestMaxRows_KeepsNothingTheWriteWrote(t *testing.T) {
	db := open(t, 2)
	notes := notesOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "notes", row(1, "a")))
	err := db.Put(ctx, "notes", row(2, "b"), row(3, "c"))
	require.ErrorIs(t, err, parsememory.ErrRowLimit)
	require.EqualError(t, err, `parsememory: table "notes": more than 2 rows: parsememory: row limit`)
	assert.Equal(t, []int64{1}, list(t, notes))
	require.NoError(t, db.Put(ctx, "notes", row(1, "replaced"), row(2, "b")), "a replaced row is no new row")

	txn, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, txn.Delete(ctx, "notes", parse.Key{int64(1)}))
	require.ErrorIs(t, txn.Put(ctx, "notes", row(3, "c"), row(4, "d")), parsememory.ErrRowLimit)
	require.ErrorIs(t, txn.Put(ctx, "notes", row(5, "e")), parsememory.ErrTxnDone)
	require.ErrorIs(t, txn.Commit(ctx), parsememory.ErrTxnDone)
	require.NoError(t, txn.Rollback(ctx))
	assert.Equal(t, []int64{1, 2}, list(t, notes), "the delete before the limit was dropped with the transaction")
	require.NoError(t, db.Put(ctx, "notes", row(2, "b")), "the ended transaction released the writer")
}

// A transaction's reads see its own writes, which the database's other
// reads see only once it commits; a rollback drops them. It holds the
// writer, so another write waits for it under its context.
func TestTxn_IsolatesItsWrites(t *testing.T) {
	db := open(t, 10)
	notes := notesOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "notes", row(1, "a")))

	txn, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, txn.Put(ctx, "notes", row(2, "b")))
	inTxn, err := notes.In(parsememory.Tx(txn))
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, list(t, inTxn))
	assert.Equal(t, []int64{1}, list(t, notes), "the write is the transaction's until it commits")

	waiting, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	err = db.Put(waiting, "notes", row(3, "c"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "parsememory: waiting for the writer")
	_, err = db.Begin(waiting)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, txn.Commit(ctx))
	assert.Equal(t, []int64{1, 2}, list(t, notes))
	require.ErrorIs(t, txn.Commit(ctx), parsememory.ErrTxnDone)
	require.NoError(t, txn.Rollback(ctx), "a settled transaction rolls back to nothing")
	_, err = inTxn.List(ctx, nil, parse.Scope{})
	require.ErrorIs(t, err, parsememory.ErrTxnDone)

	rolled, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, rolled.Delete(ctx, "notes", parse.Key{int64(1)}))
	require.NoError(t, rolled.Rollback(ctx))
	assert.Equal(t, []int64{1, 2}, list(t, notes))

	tx, err := notes.Provider().Begin(ctx)
	require.NoError(t, err)
	scoped, err := notes.In(tx)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, list(t, scoped), "parse.Provider.Begin runs on the same transactions")
	require.NoError(t, tx.Rollback(ctx))

	other := open(t, 10)
	_, err = parse.NewResource[note](other.Provider(), "notes")
	require.NoError(t, err)
	otherTxn, err := other.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otherTxn.Rollback(t.Context())) })
	_, err = notes.In(parsememory.Tx(otherTxn))
	require.ErrorContains(t, err, "parsememory: not a transaction of this database")
}

// Close drops every row, and every later operation reports ErrClosed.
func TestClose_EndsEveryOperation(t *testing.T) {
	db, err := parsememory.Open(t.Context(), parsememory.Config{MaxRows: 10})
	require.NoError(t, err)
	notes := notesOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "notes", row(1, "a")))
	txn, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, txn.Put(ctx, "notes", row(2, "b")))

	require.NoError(t, db.Close())
	require.ErrorIs(t, db.Ping(ctx), parsememory.ErrClosed)
	require.ErrorIs(t, db.Put(ctx, "notes", row(3, "c")), parsememory.ErrClosed)
	require.ErrorIs(t, db.Delete(ctx, "notes", parse.Key{int64(1)}), parsememory.ErrClosed)
	_, err = db.Begin(ctx)
	require.ErrorIs(t, err, parsememory.ErrClosed)
	_, err = notes.List(ctx, nil, parse.Scope{})
	require.ErrorIs(t, err, parsememory.ErrClosed)
	require.ErrorIs(t, txn.Commit(ctx), parsememory.ErrClosed, "a transaction open at Close commits nothing")
}

type listed struct {
	ID     string   `json:"id"     db:"id"     parse:"filter,sort"`
	Name   string   `json:"name"   db:"name"   parse:"filter,sort,facet"`
	Rank   int64    `json:"rank"   db:"rank"   parse:"filter,sort,facet"`
	Labels []string `json:"labels" db:"labels" parse:"filter,facet"`
	Meta   struct {
		Color string `json:"color"`
	} `json:"meta" db:"meta"`
	Tags []string `json:"tags" db:"-" parse:"filter,include" relation:"listed_tags,listed_id,tag_id,id"`
}

// A list evaluates its request over the stored rows: the page holds the
// rows past its cursor in sort order, either way, and the count, the facets
// and the includes come from the same rows, the count and facets over the
// whole population whatever the cursor.
func TestList_PagesCountsFacetsAndIncludes(t *testing.T) {
	db := open(t, 10)
	items, err := parse.NewResource[listed](db.Provider(), "listed")
	require.NoError(t, err)
	require.NoError(t, db.Put(t.Context(), "listed",
		parsememory.Row{"id": "a", "name": "alpha", "rank": int64(3), "labels": []string{"x", "y"}, "meta": map[string]any{"color": "red"}},
		parsememory.Row{"id": "b", "name": "beta", "rank": int64(1), "labels": []string{"y"}},
		parsememory.Row{"id": "c", "name": "gamma", "rank": int64(2)},
	))
	require.NoError(t, db.Put(t.Context(), "listed_tags",
		parsememory.Row{"listed_id": "a", "tag_id": "t1"}, parsememory.Row{"listed_id": "a", "tag_id": "t2"}, parsememory.Row{"listed_id": "c", "tag_id": "t1"}))
	list := func(t *testing.T, cursor filter.Cursor) *filter.Page[listed] {
		t.Helper()
		req, err := items.ParseListInput(parse.ListInput{
			Filter: `{"rank":{"$gte":2}}`, Sort: "-rank", Cursor: string(cursor), PageSize: 1,
			IncludeTotal: true, Facets: []string{"name", "labels"}, Include: []string{"tags"},
		})
		require.NoError(t, err)
		page, err := items.List(t.Context(), req, parse.Scope{})
		require.NoError(t, err)
		return page
	}
	ids := func(page *filter.Page[listed]) []string {
		out := make([]string, len(page.Items))
		for i, item := range page.Items {
			out[i] = item.ID
		}
		return out
	}

	first := list(t, "")
	assert.Equal(t, []string{"a"}, ids(first))
	assert.Equal(t, []string{"x", "y"}, first.Items[0].Labels)
	assert.Equal(t, "red", first.Items[0].Meta.Color)
	assert.True(t, first.PageInfo.HasNextPage)
	assert.False(t, first.PageInfo.HasPrevPage)
	require.NotNil(t, first.PageInfo.TotalCount)
	assert.Equal(t, int64(2), *first.PageInfo.TotalCount, "b falls outside the filter")
	assert.Equal(t, map[string][]filter.FacetBucket{
		"name":   {{Key: "alpha", Count: 1}, {Key: "gamma", Count: 1}},
		"labels": {{Key: "x", Count: 1}, {Key: "y", Count: 1}},
	}, first.Facets)
	assert.Equal(t, map[string]map[string][]string{"tags": {"a": {"t1", "t2"}}}, first.Relations, "the page's own pairs")

	second := list(t, first.PageInfo.EndCursor)
	assert.Equal(t, []string{"c"}, ids(second))
	assert.False(t, second.PageInfo.HasNextPage)
	assert.True(t, second.PageInfo.HasPrevPage)
	require.NotNil(t, second.PageInfo.TotalCount)
	assert.Equal(t, int64(2), *second.PageInfo.TotalCount, "the count ignores the cursor")
	assert.Equal(t, first.Facets, second.Facets, "so do the facets")
	assert.Equal(t, map[string]map[string][]string{"tags": {"c": {"t1"}}}, second.Relations)

	back := list(t, second.PageInfo.StartCursor)
	assert.Equal(t, []string{"a"}, ids(back), "a backward cursor reads the page before")
	assert.True(t, back.PageInfo.HasNextPage)
}
