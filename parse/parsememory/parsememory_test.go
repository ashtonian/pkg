//go:build unit

package parsememory_test

import (
	"cmp"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
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
	"github.com/ashtonian/pkg/parse/parsememory"
	"github.com/ashtonian/pkg/parse/parseprovider"
	"github.com/ashtonian/pkg/parse/patch"
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

type tagged struct {
	ID    string            `json:"id"    db:"id"    parse:"filter,sort"`
	Name  string            `json:"name"  db:"name"  parse:"filter,patch"`
	Attrs map[string]string `json:"attrs" db:"attrs" parse:"patch" patch_ops:"replace,add,remove"`
	Tags  []string          `json:"tags"  db:"-"     parse:"filter,patch,include" relation:"tagged_tags,tagged_id,tag_id,id"`
}

// taggedOn builds the tagged resource, whose patches may write members of
// attrs, and writes a, tagged x, and b, untagged.
func taggedOn(t *testing.T, db *parsememory.Database) *parse.Resource[tagged] {
	t.Helper()
	r, err := parse.NewResource[tagged](db.Provider(), "tagged", parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.AddPath("/attrs/*", &patch.PathSchema{
			AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeString, SQLColumn: "attrs",
		})
	}))
	require.NoError(t, err)
	require.NoError(t, db.Put(t.Context(), "tagged",
		parsememory.Row{"id": "a", "name": "alpha", "attrs": map[string]string{"k": "v"}},
		parsememory.Row{"id": "b", "name": "beta", "attrs": map[string]string{}}))
	return r
}

// stored is the row of id as r lists it, with its tags.
func stored(t *testing.T, r *parse.Resource[tagged], id string) tagged {
	t.Helper()
	req, err := r.ParseListInput(parse.ListInput{Filter: `{"id":"` + id + `"}`, Include: []string{"tags"}})
	require.NoError(t, err)
	page, err := r.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	item := page.Items[0]
	item.Tags = page.Relations["tags"][id]
	return item
}

// An update writes the patch's columns and relations together, or, when
// any part fails, nothing: a member operation the stored document does not
// allow is the operation's own error and writes no target, and a write in
// a transaction that fails restores the transaction to where it stood,
// which goes on.
func TestUpdate_WritesAllOrNothing(t *testing.T) {
	db := open(t, 10)
	r := taggedOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "tagged_tags", parsememory.Row{"tagged_id": "a", "tag_id": "x"}))

	p, err := r.ParseMergePatch([]byte(`{"name":"renamed","tags":["y","z"]}`))
	require.NoError(t, err)
	res, err := r.Update(ctx, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, int64(3), res.RelationRows, "x removed, y and z written")
	assert.Equal(t, tagged{ID: "a", Name: "renamed", Attrs: map[string]string{"k": "v"}, Tags: []string{"y", "z"}}, stored(t, r, "a"))

	missing, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"lost"},{"op":"remove","path":"/attrs/k"}]`))
	require.NoError(t, err)
	_, err = r.Update(ctx, missing, parse.ByIDs("a", "b"), parse.Scope{})
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "the operation's own error: %v", err)
	assert.Equal(t, "/attrs/k", pe.Path)
	_, ran := errors.AsType[*parse.StatementError](err)
	assert.False(t, ran, "a refused operation is the patch's error, as the SQL providers' conflict statement reports it")
	assert.Equal(t, "renamed", stored(t, r, "a").Name, "a, which the operation applies to, is not written either")

	txn, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, txn.Put(ctx, "tagged", parsememory.Row{"id": "c", "name": "gamma", "attrs": map[string]string{}}))
	inTxn, err := r.In(parsememory.Tx(txn))
	require.NoError(t, err)
	_, err = inTxn.Update(ctx, missing, parse.ByIDs("a", "c"), parse.Scope{})
	require.Error(t, err)
	res, err = inTxn.Update(ctx, p, parse.ByID("c"), parse.Scope{})
	require.NoError(t, err, "the transaction goes on after the failed update")
	require.Len(t, res.Changed, 1)
	require.NoError(t, txn.Commit(ctx))
	assert.Equal(t, tagged{ID: "c", Name: "renamed", Attrs: map[string]string{}, Tags: []string{"y", "z"}}, stored(t, r, "c"))
}

// A relation write past MaxRows fails with ErrRowLimit and keeps nothing
// the update wrote, its column changes included; in a transaction it ends
// the transaction.
func TestUpdate_RowLimitKeepsNothingTheUpdateWrote(t *testing.T) {
	db := open(t, 2)
	r := taggedOn(t, db)
	ctx := t.Context()
	require.NoError(t, db.Put(ctx, "tagged_tags", parsememory.Row{"tagged_id": "a", "tag_id": "x"}))
	p, err := r.ParseMergePatch([]byte(`{"name":"renamed","tags":["x","y","z"]}`))
	require.NoError(t, err)
	_, err = r.Update(ctx, p, parse.ByID("a"), parse.Scope{})
	require.ErrorIs(t, err, parsememory.ErrRowLimit)
	assert.Equal(t, tagged{ID: "a", Name: "alpha", Attrs: map[string]string{"k": "v"}, Tags: []string{"x"}}, stored(t, r, "a"))

	txn, err := db.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, txn.Delete(ctx, "tagged", parse.Key{"b"}))
	inTxn, err := r.In(parsememory.Tx(txn))
	require.NoError(t, err)
	_, err = inTxn.Update(ctx, p, parse.ByID("a"), parse.Scope{})
	require.ErrorIs(t, err, parsememory.ErrRowLimit)
	require.ErrorIs(t, txn.Commit(ctx), parsememory.ErrTxnDone)
	assert.Equal(t, "beta", stored(t, r, "b").Name, "the delete before the limit was dropped with the transaction")
}

// A pair table a resource keys by a column of its own, which no pair
// writes, reads its relation's pairs but holds no pair a membership change
// writes: the memory provider generates no keys.
func TestUpdate_RefusesPairsATableCannotKey(t *testing.T) {
	type pairRow struct {
		Seq      int64  `json:"seq"       db:"seq"`
		TaggedID string `json:"tagged_id" db:"tagged_id"`
		TagID    string `json:"tag_id"    db:"tag_id"`
	}
	db := open(t, 10)
	_, err := parse.NewResource[pairRow](db.Provider(), "tagged_tags", parse.PrimaryKey("seq"))
	require.NoError(t, err)
	r := taggedOn(t, db)
	require.NoError(t, db.Put(t.Context(), "tagged_tags", parsememory.Row{"seq": int64(9), "tagged_id": "b", "tag_id": "x"}))
	assert.Equal(t, []string{"x"}, stored(t, r, "b").Tags)

	p, err := r.ParseMergePatch([]byte(`{"tags":["y"]}`))
	require.NoError(t, err)
	_, err = r.Update(t.Context(), p, parse.ByID("b"), parse.Scope{})
	require.ErrorContains(t, err, `parse: tagged: relations: parsememory: relation tags: pair table "tagged_tags": key column seq: NULL`)
	assert.Equal(t, []string{"x"}, stored(t, r, "b").Tags, "the replace's removal was undone with it")
}

// patched is a resource whose relation its patches write and its filters
// do not read.
type patched struct {
	ID   string   `json:"id"   db:"id"   parse:"filter,sort"`
	Tags []string `json:"tags" db:"-"    parse:"patch" relation:"patched_tags,patched_id,tag_id,id"`
}

// patchedPair is a row of patched's pair table, keyed by both its columns.
type patchedPair struct {
	PatchedID string `json:"patched_id" db:"patched_id" parse:"filter,sort"`
	TagID     string `json:"tag_id"     db:"tag_id"     parse:"filter,sort"`
}

// A relation a resource patches but does not filter declares its pair
// table as a filtered one does, so a patch writes and removes its pairs.
func TestUpdate_WritesARelationOnlyPatchesReach(t *testing.T) {
	db := open(t, 10)
	r, err := parse.NewResource[patched](db.Provider(), "patched")
	require.NoError(t, err)
	pairs, err := parse.NewResource[patchedPair](db.Provider(), "patched_tags", parse.PrimaryKey("patched_id", "tag_id"))
	require.NoError(t, err)
	require.NoError(t, db.Put(t.Context(), "patched", parsememory.Row{"id": "a"}, parsememory.Row{"id": "b"}))
	held := func() []patchedPair {
		page, err := pairs.List(t.Context(), nil, parse.Scope{})
		require.NoError(t, err)
		return page.Items
	}

	p, err := r.ParseMergePatch([]byte(`{"tags":["x","y"]}`))
	require.NoError(t, err)
	res, err := r.Update(t.Context(), p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.RelationRows)
	assert.Equal(t, []patchedPair{{"a", "x"}, {"a", "y"}}, held())

	p, err = r.ParsePatch([]byte(`[{"op":"remove","path":"/tags","value":["x"]}]`))
	require.NoError(t, err)
	res, err = r.Update(t.Context(), p, parse.ByIDs("a", "b"), parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.RelationRows)
	assert.Equal(t, []patchedPair{{"a", "y"}}, held())
}

// slot is a row keyed by two columns.
type slot struct {
	Shelf string `json:"shelf" db:"shelf" parse:"filter,sort"`
	Bin   int64  `json:"bin"   db:"bin"   parse:"filter,sort"`
	Label string `json:"label" db:"label" parse:"filter,sort"`
}

// walkPages lists every page of slots under input, forward from the first
// page by each page's end cursor, and backward from the last page by each
// page's start cursor, and returns each walk's rows in walk order and the
// totals its pages report, nil for none.
func walkPages(t *testing.T, r *parse.Resource[slot], input parse.ListInput) (forward, backward []slot, totals []*int64) {
	t.Helper()
	list := func() *filter.Page[slot] {
		req, err := r.ParseListInput(input)
		require.NoError(t, err)
		page, err := r.List(t.Context(), req, parse.Scope{})
		require.NoError(t, err)
		totals = append(totals, page.PageInfo.TotalCount)
		return page
	}
	for {
		page := list()
		forward = append(forward, page.Items...)
		if !page.PageInfo.HasNextPage {
			break
		}
		input.Cursor = string(page.PageInfo.EndCursor)
	}
	for {
		page := list()
		backward = append(slices.Clone(page.Items), backward...)
		if !page.PageInfo.HasPrevPage {
			break
		}
		input.Cursor = string(page.PageInfo.StartCursor)
	}
	return forward, backward, totals
}

// A page sorted by the table's key in one direction, which counts no
// filter, reads the tree from its cursor and stops once it holds its rows,
// its count the table's size: it pages exactly as a page that counts a
// filter, which reads every row, forward and backward, and as a sort by
// other keys does.
func TestList_KeyOrderPagesAsAFullPass(t *testing.T) {
	db := open(t, 100)
	r, err := parse.NewResource[slot](db.Provider(), "slots", parse.PrimaryKey("shelf", "bin"))
	require.NoError(t, err)
	var rows []parsememory.Row
	var want []slot
	for _, shelf := range []string{"z", "x", "y"} {
		for bin := int64(5); bin >= 1; bin-- {
			label := fmt.Sprintf("%s%d", shelf, bin)
			rows = append(rows, parsememory.Row{"shelf": shelf, "bin": bin, "label": label})
			want = append(want, slot{Shelf: shelf, Bin: bin, Label: label})
		}
	}
	require.NoError(t, db.Put(t.Context(), "slots", rows...))
	byKey := func(a, b slot) int { return cmp.Or(strings.Compare(a.Shelf, b.Shelf), cmp.Compare(a.Bin, b.Bin)) }
	slices.SortFunc(want, byKey)
	descending := slices.Clone(want)
	slices.Reverse(descending)

	for _, tt := range []struct {
		sort string
		want []slot
	}{
		{"", want},
		{"shelf", want},
		{"-shelf", descending},
		{"-shelf,-bin", descending},
		{"label", want},
	} {
		t.Run("sort "+tt.sort, func(t *testing.T) {
			total := int64(len(tt.want))
			for _, input := range []parse.ListInput{
				{Sort: tt.sort, PageSize: 4},
				{Sort: tt.sort, PageSize: 4, IncludeTotal: true},
				{Sort: tt.sort, PageSize: 4, IncludeTotal: true, Filter: `{"bin":{"$gte":1}}`},
			} {
				forward, backward, totals := walkPages(t, r, input)
				assert.Equal(t, tt.want, forward, "%+v", input)
				assert.Equal(t, tt.want, backward, "%+v", input)
				for _, got := range totals {
					if input.IncludeTotal {
						assert.Equal(t, &total, got, "%+v", input)
					} else {
						assert.Nil(t, got, "%+v", input)
					}
				}
			}
		})
	}
}
