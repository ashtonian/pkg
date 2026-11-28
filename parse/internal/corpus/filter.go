//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// Exact decimals on each backend: SQLite holds amounts as decimal text and
// prices in JSON text, compared through parsesql's functions;
// PostgreSQL numeric amounts and jsonb prices.
func decimalMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, runDecimalMatrix)
}

// runDecimalMatrix writes DecimalRecords, each amount as its decimal text
// with its scale, runs the decimal cases, sorts, keyset walks and facets on
// b, and checks that each row's price reads in SQL as filter.PathValue reads
// it.
func runDecimalMatrix(t *testing.T, b sqlBackend) {
	provider, db := b.open(t)
	schema, err := filtertest.DecimalSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.DecimalRecords()))
	t.Run("cases", func(t *testing.T) {
		filtertest.RunDecimals(t, schema, b.filterIDs(db, schema))
	})
	t.Run("walks", func(t *testing.T) {
		r, err := parse.NewResource[filtertest.DecimalRecord](provider, schema.Name, parse.PageSize(walkPage, 10))
		require.NoError(t, err)
		walkOrders(t, walker[filtertest.DecimalRecord]{resource: r,
			id: func(r filtertest.DecimalRecord) string { return r.ID }}, filtertest.DecimalOrders)
	})
	t.Run("facets", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.DecimalFacets))...)
		assert.Equal(t, filtertest.DecimalFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, db, b.dialect, schema, filtertest.DecimalFacetOrders, filtertest.DecimalFacets)
	})
	t.Run("values match PathValue", func(t *testing.T) {
		b.sqlOnly(t, "reads each price through its SQL expression (filter.FieldExpr)")
		f := schema.Fields["doc.price"]
		for _, row := range dbtest.QueryRows[[]any](t, db, "SELECT id, doc, "+filter.FieldExpr(b.dialect, f)+" FROM "+schema.Name+" ORDER BY id") {
			id, doc, got := row[0], row[1], row[2]
			want, present, err := filter.PathValue(textOf(doc), f)
			require.NoError(t, err, id)
			if !present {
				want = nil
			}
			assert.True(t, sameDecimal(got, want), "row %v: SQL %v (%T), PathValue %v", id, got, got, want)
		}
	})
}

// elementRelationCases are element predicates through the children
// self-relation, which rebases the related fields onto an alias while each
// element predicate binds its own element.
func elementRelationCases() []parsetest.ElementCase {
	cases := []parsetest.ElementCase{
		{Name: "an element path through a relation", Filter: `{"children":{"$some":{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}}}`, Want: []string{"o1", "o4"}},
		{Name: "an array path's element through a relation", Filter: `{"children":{"$some":{"meta.tags":{"$some":{"$eq":"x"}}}}}`, Want: []string{"o1"}},
		{Name: "no related row with an element", Filter: `{"children":{"$none":{"items":{"$some":{}}}}}`, Want: []string{"o2", "o3", "o5", "o6"}},
	}
	return cases
}

// Same-element predicates over JSON arrays on each backend: JSON text on
// SQLite, jsonb on PostgreSQL.
func elementPredicates(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		orders := parsetest.OrderResource(t, p)
		loadOrders(t, b, db)
		runElementCases(t, b, orders, db)
	})
}

// loadOrders writes parsetest.OrderRows into orders on db: its id and
// parent_id text, its other columns documents.
func loadOrders(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	table := sqlTable{name: "orders", key: []string{"id"}}
	for _, name := range parsetest.OrderColumns() {
		c := column[json.RawMessage](name)
		if name == "id" || name == "parent_id" {
			c = column[string](name)
		}
		table.columns = append(table.columns, c)
	}
	rows := make([][]any, len(parsetest.OrderRows()))
	for i, r := range parsetest.OrderRows() {
		rows[i] = r[:]
	}
	b.load(t, db, table, rows)
}

// runElementCases selects each case's orders on db: through the list SQL
// orders compiles on a SQL engine, as the provider's lists evaluate the
// filter on the Memory engine. The fixture's documents take shapes an
// Order does not decode, so the cases read ids alone.
func runElementCases(t *testing.T, b sqlBackend, orders *parse.Resource[parsetest.Order], db *sql.DB) {
	t.Helper()
	for _, tc := range slices.Concat(parsetest.ElementCases(), elementRelationCases()) {
		t.Run(tc.Name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.Filter}}
			if b.engine == Memory {
				f, err := orders.ParseRequestFilter(q)
				require.NoError(t, err)
				got, err := b.filterIDs(db, orders.FilterSchema())(t, f)
				require.NoError(t, err)
				assert.Equal(t, tc.Want, sortedIDs(got, func(id string) string { return id }), tc.Filter)
				return
			}
			req, err := orders.ParseList(q)
			require.NoError(t, err)
			plan, err := orders.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			got := rowIDs(t, dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
			slices.Sort(got)
			assert.Equal(t, tc.Want, got, plan.Page.SQL)
		})
	}

	for _, bad := range []string{
		`{"items":{"$some":{"colour":"red"}}}`,
		`{"items":{"$some":"a"}}`,
		`{"items":{"$is":{}}}`,
	} {
		_, err := orders.ParseList(url.Values{filter.ParamFilter: {bad}})
		assert.Error(t, err, bad)
	}
}

// Every operator × type case on each backend: SQLite holds documents and
// arrays as JSON text, timestamps as their canonical text, booleans as
// integers and decimals in a NUMERIC column; PostgreSQL, with PostGIS for
// the points, the storage model's native types: jsonb, TEXT[],
// timestamptz, uuid, numeric.
func filterMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db, schema := loadRecords(t, b)
		filtertest.Run(t, b.filterIDs(db, schema))
	}, onPostGIS())
}

// `$in` / `$nin` sets of every size, each bound as one parameter: a JSON
// array on SQLite, an array on PostgreSQL.
func setMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db, schema := loadRecords(t, b)
		filtertest.RunSets(t, b.filterIDs(db, schema))
	})
}

// Membership constraints, as subqueries and resolved, over lookup columns
// of each backend's types: TEXT, INTEGER and decimal TEXT on SQLite; UUID,
// BIGINT and NUMERIC on PostgreSQL.
func membershipMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, runMembershipMatrix)
}

// WhereIn lists of integers, UUIDs and text, each one set parameter, one
// past PostgreSQL's 65,535 bind parameters.
func lookupSets(t *testing.T, engine Engine) {
	runOn(t, engine, runLookupSets)
}

// Policy trees, each listed page by page.
func policyMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, runPolicyMatrix)
}

// loadRecords creates filtertest.Records' table on a fresh database of b.
// SQLite holds the amounts in a NUMERIC column, which converts each to
// INTEGER or REAL as it stores it, the comparisons these cases expect of
// it; the decimal matrix holds them exactly, as TEXT.
func loadRecords(t *testing.T, b sqlBackend) (*sql.DB, *filter.ResourceSchema) {
	t.Helper()
	_, db := b.open(t)
	schema, err := filtertest.Schema()
	require.NoError(t, err)
	table := schemaTable(schema).holding("amount", SQLite, "NUMERIC")
	b.load(t, db, table, recordRows(t, table, filtertest.Records()))
	return db, schema
}

// runMembershipMatrix runs the membership cases twice: compiled to their
// subqueries, and resolved through filtertest.Lookups first, as a memory
// backend resolves them. Both must select the same rows.
func runMembershipMatrix(t *testing.T, b sqlBackend) {
	t.Helper()
	db, schema := loadRecords(t, b)
	loadLookupTables(t, b, db)
	selected := b.filterIDs(db, schema)
	t.Run("subquery", func(t *testing.T) {
		filtertest.RunMembership(t, selected)
	})
	t.Run("resolved", func(t *testing.T) {
		filtertest.RunMembership(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			resolved, err := filter.ResolveMemberships(t.Context(), f, filtertest.Lookups())
			if err != nil {
				return nil, err
			}
			return selected(t, resolved)
		})
	})
}

// runLookupSets runs WhereIn lists of each type a lookup column holds, on
// b's column types: each list binds as one set, one of them past
// PostgreSQL's 65,535 bind parameters.
func runLookupSets(t *testing.T, b sqlBackend) {
	t.Helper()
	_, db := b.open(t)
	ua, ub := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), uuid.MustParse("b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12")
	owners := sqlTable{name: "set_owners", columns: []sqlColumn{column[string]("id")}, key: []string{"id"}}
	links := sqlTable{name: "set_links", columns: []sqlColumn{column[string]("owner_id").required(), column[int64]("n"), column[uuid.UUID]("u"), column[string]("s")}}
	b.load(t, db, owners, [][]any{{"a"}, {"b"}, {"c"}})
	b.load(t, db, links, [][]any{{"a", 1, ua, "x"}, {"b", 70000, ub, "y"}})
	many := make([]any, 70_000)
	for i := range many {
		many[i] = int64(i + 2) // 2…70001: b's 70000, not a's 1
	}
	schema := &filter.ResourceSchema{Name: "set_owners", PrimaryKey: []string{"id"}, Fields: map[string]*filter.FieldSchema{
		"id": {ExternalName: "id", Column: "id", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}},
	}}
	for _, tc := range []struct {
		name  string
		where *filter.LookupCond
		want  []string
	}{
		{"integers past the bind limit", filter.WhereIn("n", many...), []string{"b"}},
		{"integers of several Go kinds", filter.WhereIn("n", 1, int32(70000)), []string{"a", "b"}},
		{"uuids", filter.WhereIn("u", ua), []string{"a"}},
		{"text", filter.WhereIn("s", "x", "y"), []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := filter.ApplyPolicyConstraints(schema, nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{
				{Field: "id", Member: &filter.Lookup{Table: "set_links", Project: "owner_id", Where: tc.where}},
			}})
			require.NoError(t, err)
			if b.engine != Memory {
				res, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
				require.NoError(t, err)
				assert.Equal(t, 1, res.ParamCount, "one set parameter")
			}
			got, err := b.filterIDs(db, schema)(t, filter.NewParsedFilter(ast))
			require.NoError(t, err)
			assert.Equal(t, tc.want, sortedIDs(got, func(id string) string { return id }))
		})
	}
}

// runPolicyMatrix runs the policy cases as lists: each walks its pages two
// rows at a time, so every case's WHERE also carries a cursor's seek, and
// its count must agree with the rows walked. The Memory engine evaluates
// each case's policy over the table, as its lists do, except a raw SQL
// fragment and a search (filtertest.PolicyCase.SQLOnly), which it refuses.
func runPolicyMatrix(t *testing.T, b sqlBackend) {
	t.Helper()
	db, schema := loadRecords(t, b)
	loadLookupTables(t, b, db)
	schema.PrimaryKey = []string{"id"}
	if b.engine == Memory {
		selected := b.filterIDs(db, schema)
		filtertest.RunPolicy(t, false, func(t *testing.T, q filtertest.PolicyQuery) ([]string, error) {
			ast, err := filter.ApplyPolicyConstraints(schema, q.Filter, q.Policy)
			if err != nil {
				return nil, err
			}
			return selected(t, filter.NewParsedFilter(ast))
		})
		return
	}
	lq, err := filter.NewListQuery(schema, nil, b.dialect)
	require.NoError(t, err)
	lq = lq.WithSearchSchema(filtertest.SearchSchema())
	filtertest.RunPolicy(t, true, func(t *testing.T, q filtertest.PolicyQuery) ([]string, error) {
		var seen []string
		cursor := ""
		for range len(filtertest.Records()) {
			opts := []filter.ListOption{filter.ListLimit(2), filter.ListPolicy(q.Policy), filter.ListSearch(q.Search), filter.ListWithCount(true)}
			if q.Filter != nil {
				opts = append(opts, filter.ListFilterAST(q.Filter))
			}
			if cursor != "" {
				opts = append(opts, filter.ListCursor(cursor))
			}
			res, err := lq.Build(opts...)
			if err != nil {
				return nil, err
			}
			var page []policyRecord
			for _, id := range dbtest.QueryRows[string](t, db, "SELECT id FROM records "+res.Fragment, res.Args...) {
				page = append(page, policyRecord{ID: id})
			}
			p, err := filter.NewPage(page, res.Limit, res.Cursor, func(r policyRecord, col string) (any, error) {
				return filter.CursorValueByTag(r, "db", col)
			})
			require.NoError(t, err)
			for _, r := range p.Items {
				seen = append(seen, r.ID)
			}
			var total int64
			require.NoError(t, db.QueryRowContext(t.Context(), res.CountSQL, res.CountArgs...).Scan(&total), res.CountSQL)
			if !p.PageInfo.HasNextPage {
				require.EqualValues(t, len(seen), total, "count of %s", res.CountSQL)
				return seen, nil
			}
			cursor = string(p.PageInfo.EndCursor)
		}
		t.Fatalf("walk did not end within %d pages; seen %v", len(filtertest.Records()), seen)
		return nil, nil
	})
}

type policyRecord struct {
	ID string `db:"id"`
}

// loadLookupTables creates and fills filtertest.LookupTables beside
// records.
func loadLookupTables(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	for _, lt := range filtertest.LookupTables() {
		table := sqlTable{name: lt.Name}
		for _, c := range lt.Columns {
			table.columns = append(table.columns, sqlColumn{name: c.Name, kind: core.TypeInfo{Type: c.Type}})
		}
		b.load(t, db, table, lt.Rows)
	}
}

// JSON integers at the edge of the portable domain select exactly the
// matching document on SQLite and PostgreSQL — no neighbour rounds onto it.
func jsonIntegerEdgesSameRowsOnEveryBackend(t *testing.T, engine Engine) {
	schema := &filter.ResourceSchema{Name: "docs", Fields: map[string]*filter.FieldSchema{
		"doc": {ExternalName: "doc", Table: "docs", Column: "doc", DataType: core.FieldTypeJSON, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpContains}},
	}}
	stored := []string{`{"n":9223372036854775807}`, `{"n":9223372036854775806}`, `{"n":18446744073709551615}`, `{"n":-9223372036854775808}`}
	queries := map[string][]int{
		`{"doc":{"n":9223372036854775807}}`:                {1},
		`{"doc":{"$contains":{"n":-9223372036854775808}}}`: {4},
	}
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		rows := make([][]any, len(stored))
		for i, doc := range stored {
			rows[i] = []any{i + 1, doc}
		}
		b.load(t, db, sqlTable{name: "docs", columns: []sqlColumn{column[int]("id"), column[json.RawMessage]("doc")}}, rows)
		selected := b.filterIDs(db, schema)
		for q, want := range queries {
			ast, err := filter.NewFilterParser(schema, nil).Parse(q)
			require.NoError(t, err, q)
			got, err := selected(t, filter.NewParsedFilter(ast))
			require.NoError(t, err, q)
			ids := make([]string, len(want))
			for i, id := range want {
				ids[i] = strconv.Itoa(id)
			}
			assert.Equal(t, ids, got, q)
		}
	})
}

// `$ilike` case on each backend. SQLite lower-cases ASCII letters only. On
// PostgreSQL a pg_c_utf8 column lower-cases every letter, as the reference
// evaluator, and so the memory provider, does, and a C column ASCII letters
// only.
func foldMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		switch b.engine {
		case SQLite:
			runFoldMatrix(t, b, false)
			return
		case Memory:
			runFoldMatrix(t, b, true)
			return
		}
		t.Run("pg_c_utf8", func(t *testing.T) { runFoldMatrix(t, b.with(collation(filter.CollationPgCUTF8)), true) })
		t.Run("C", func(t *testing.T) { runFoldMatrix(t, b.with(collation(filter.CollationC)), false) })
	})
}

// runFoldMatrix loads filtertest.FoldRecords on b and runs the `$ilike`
// case cases on them; unicode adds those whose answer lower-cases a
// non-ASCII letter.
func runFoldMatrix(t *testing.T, b sqlBackend, unicode bool) {
	t.Helper()
	_, db := b.open(t)
	schema, err := filtertest.FoldSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.FoldRecords()))
	filtertest.RunFold(t, unicode, b.filterIDs(db, schema))
}

// Stored points on each backend: JSON text read through parsesql's
// functions on SQLite, jsonb read as PostgreSQL casts its members on
// PostGIS.
func geoMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		schema, err := filtertest.GeoSchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.GeoRecords()))
		filtertest.RunGeo(t, b.filterIDs(db, schema))
	}, onPostGIS())
}

type hasKeyCase struct {
	doc          any
	color, other *bool
}

// hasKeyDocs are the shapes `$has_key` distinguishes, with jsonb `?`'s
// answers for the keys "color" and "x" as PostgreSQL 18 gives them; a NULL
// column (nil) is NULL.
func hasKeyDocs() []hasKeyCase {
	docs := []hasKeyCase{
		{`{"color":1}`, new(true), new(false)},
		{`{"color":null}`, new(true), new(false)},
		{`["color",1]`, new(true), new(false)},
		{`[["color"]]`, new(false), new(false)},
		{`"color"`, new(true), new(false)},
		{`"x"`, new(false), new(true)},
		{`3`, new(false), new(false)},
		{`null`, new(false), new(false)},
		{`{"a":{"color":1}}`, new(false), new(false)},
		{`{}`, new(false), new(false)},
		{`[]`, new(false), new(false)},
		{nil, nil, nil},
	}
	return docs
}

type hasKeyDoc struct {
	ID  string          `json:"id"  db:"id"  parse:"filter"`
	Doc json.RawMessage `json:"doc" db:"doc" parse:"filter"`
}

// `$has_key` on each backend against every shape, on SQLite's JSON text as
// on PostgreSQL's jsonb, and the reference JSONHasKey against the same
// documents.
func hasKey(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		table := sqlTable{name: "has_key_docs", columns: []sqlColumn{column[string]("id"), column[json.RawMessage]("doc")}, key: []string{"id"}}
		docs := hasKeyDocs()
		rows := make([][]any, len(docs))
		for i, c := range docs {
			rows[i] = []any{fmt.Sprintf("%02d", i), c.doc}
		}
		b.load(t, db, table, rows)
		schema, err := filter.GenerateFilterSchema[hasKeyDoc](core.NewSchemaGenerator())
		require.NoError(t, err)
		for _, key := range []string{"color", "x"} {
			ast, err := filter.NewFilterParser(schema, nil).Parse(`{"doc":{"$has_key":"` + key + `"}}`)
			require.NoError(t, err)
			got := hasKeyAnswers(t, b, db, schema, ast, rows)
			require.Len(t, got, len(docs))
			for i, c := range docs {
				want := c.color
				if key == "x" {
					want = c.other
				}
				if want == nil {
					assert.False(t, got[i].Valid, "doc %v key %s: a NULL document is NULL", c.doc, key)
					continue
				}
				assert.True(t, got[i].Valid && got[i].Bool == *want, "doc %v key %s: SQL %v, want %v", c.doc, key, got[i], *want)
				var decoded any
				doc, ok := c.doc.(string)
				require.True(t, ok)
				require.NoError(t, json.Unmarshal([]byte(doc), &decoded))
				assert.Equal(t, *want, filter.JSONHasKey(decoded, key), "reference: doc %v key %s", c.doc, key)
			}
		}
	})
}

// hasKeyAnswers is ast's answer for each of rows, in order: the
// statement's value on a SQL engine; on the Memory engine, true for the
// rows its filter selects, false for those its negation selects, and NULL
// for the rest.
func hasKeyAnswers(t *testing.T, b sqlBackend, db *sql.DB, schema *filter.ResourceSchema, ast *filter.FilterAST, rows [][]any) []sql.NullBool {
	t.Helper()
	if b.engine != Memory {
		res, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
		require.NoError(t, err)
		return dbtest.QueryRows[sql.NullBool](t, db, "SELECT ("+res.Where+") FROM has_key_docs ORDER BY id", res.Args...)
	}
	selected := b.filterIDs(db, schema)
	holds, err := selected(t, filter.NewParsedFilter(ast))
	require.NoError(t, err)
	fails, err := selected(t, filter.NewParsedFilter(&filter.FilterAST{Root: &filter.FilterNode{Op: filter.OpNot, Children: []*filter.FilterNode{ast.Root}}}))
	require.NoError(t, err)
	answers := make([]sql.NullBool, len(rows))
	for i, row := range rows {
		id, ok := row[0].(string)
		require.True(t, ok)
		switch {
		case slices.Contains(holds, id):
			answers[i] = sql.NullBool{Bool: true, Valid: true}
		case slices.Contains(fails, id):
			answers[i] = sql.NullBool{Valid: true}
		}
	}
	return answers
}

// Typed JSON paths on each backend: SQLite's documents are JSON text, and
// PostgreSQL's are each storage a document column can declare.
func pathMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine != Postgres {
			runPathMatrix(t, b, "")
			return
		}
		for _, storage := range []filter.JSONStorage{filter.StorageJSONB, filter.StorageJSON, filter.StorageText} {
			t.Run(string(storage), func(t *testing.T) { runPathMatrix(t, b, storage) })
		}
	})
}

// loadPathRecords writes filtertest.PathRecords into path_records on a
// fresh database of b, its documents held as schema declares them, and
// returns the provider over it and the test's own handle on it.
func loadPathRecords(t *testing.T, b sqlBackend, schema *filter.ResourceSchema) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.PathRecords()))
	return p, db
}

// pathSchema is filtertest.PathSchema with its documents held as storage,
// or as the schema declares them for none.
func pathSchema(t *testing.T, storage filter.JSONStorage) *filter.ResourceSchema {
	t.Helper()
	schema, err := filtertest.PathSchema()
	require.NoError(t, err)
	if storage != "" {
		schema = withStorage(t, schema, storage)
	}
	return schema
}

// runPathMatrix runs the path cases on b, its documents held as storage,
// then checks that every declared path reads, row by row, the value
// filter.PathValue gives the same document: the SQL rendering and the
// reference evaluator are one rule.
func runPathMatrix(t *testing.T, b sqlBackend, storage filter.JSONStorage) {
	t.Helper()
	schema := pathSchema(t, storage)
	_, db := loadPathRecords(t, b, schema)
	query := b.filterIDs(db, schema)
	t.Run("cases", func(t *testing.T) { filtertest.RunPaths(t, schema, query) })
	t.Run("sets", func(t *testing.T) { filtertest.RunPathSets(t, schema, query) })
	t.Run("values match PathValue", func(t *testing.T) {
		b.sqlOnly(t, "reads each path through its SQL expression (filter.FieldExpr)")
		names := slices.Sorted(func(yield func(string) bool) {
			for n, f := range schema.Fields {
				if f.Path != nil && !yield(n) {
					return
				}
			}
		})
		require.Len(t, names, 14, "the fixture's declared paths")
		for _, name := range names {
			f := schema.Fields[name]
			for _, row := range dbtest.QueryRows[[]any](t, db, "SELECT id, "+f.Path.Column+", "+filter.FieldExpr(b.dialect, f)+" FROM path_records ORDER BY id") {
				id, doc, got := row[0], row[1], row[2]
				want, present, err := filter.PathValue(textOf(doc), f)
				require.NoError(t, err, "%s row %v", name, id)
				if !present {
					want = nil
				}
				same := sameValue(got, want)
				if f.DataType == core.FieldTypeDecimal {
					same = sameDecimal(got, want)
				}
				assert.True(t, same, "%s row %v: SQL %v (%T), PathValue %v (%T)", name, id, got, got, want, want)
			}
		}
	})
}

// withStorage declares the fixture's documents held as storage.
func withStorage(t *testing.T, s *filter.ResourceSchema, storage filter.JSONStorage) *filter.ResourceSchema {
	t.Helper()
	out := *s
	out.Fields = map[string]*filter.FieldSchema{}
	for n, f := range s.Fields {
		c := *f
		if c.Path != nil {
			p := *c.Path
			p.Storage = storage
			c.Path = &p
		} else if core.IsDocument(c.DataType) {
			c.Storage = storage
		}
		out.Fields[n] = &c
	}
	return &out
}

func textOf(v any) any {
	b, ok := v.([]byte)
	if ok {
		return string(b)
	}
	return v
}

// sameValue compares a scanned SQL value with PathValue's: numbers by value
// (drivers return int64, float64 or numeric text), booleans across 0/1. A
// number member, which PathValue reads exactly, is PostgreSQL's numeric
// text and SQLite's INTEGER exactly, and SQLite's REAL as the double
// json_extract rounds it to.
func sameValue(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	n, ok := want.(json.Number)
	if ok {
		g, isFloat := got.(float64)
		if isFloat {
			wf, _ := strconv.ParseFloat(string(n), 64) // ±Inf beyond float64's range, as SQLite reads it
			return g == wf
		}
		return sameDecimal(got, string(n))
	}
	switch w := want.(type) {
	case bool:
		switch g := got.(type) {
		case bool:
			return g == w
		case int64:
			return (g == 1) == w
		}
		return false
	case int64, float64:
		g, ok := numberOf(got)
		wf, _ := numberOf(w)
		wi, wInt := w.(int64)
		gi, gInt := got.(int64)
		if gInt && wInt {
			return gi == wi
		}
		s, gText := got.(string)
		if gText && wInt {
			return s == fmt.Sprint(wi)
		}
		return ok && g == wf
	}
	return reflect.DeepEqual(textOf(got), want)
}

// sameDecimal compares a decimal path's SQL value — a number, or numeric
// text keeping its scale — with PathValue's canonical text by value.
func sameDecimal(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	c, ok := core.CompareDecimals(textOf(got), want)
	return ok && c == 0
}

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case []byte:
		var f float64
		_, err := fmt.Sscan(string(x), &f)
		return f, err == nil
	case string:
		var f float64
		_, err := fmt.Sscan(x, &f)
		return f, err == nil
	}
	return 0, false
}

// Quoted integers on each backend: decimal-text operands over INTEGER
// storage on SQLite and bigint on PostgreSQL.
func quotedMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		schema, err := filtertest.QuotedSchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.QuotedRecords()))
		filtertest.RunQuoted(t, b.filterIDs(db, schema))
	})
}

// A NULL operand or set member means on every engine what it means in SQL
// and in filter.Evaluate (runNullOperands).
func nullOperands(t *testing.T, engine Engine) {
	runOn(t, engine, runNullOperands)
}

// nullColumn is a column the NULL operand cases compare: its field type,
// the value its one row holds, and another value of its type.
type nullColumn struct {
	field       core.FieldType
	held, other any
}

// runNullOperands holds a database and filter.Evaluate to SQL's answer for
// a NULL operand or set member, whether nil, a nil pointer or a NULL
// Valuer: a comparison with it is Unknown, and `IN` a set holding it is
// True on an equal member and otherwise Unknown, so `NOT IN` is False or
// Unknown. Each case runs against a string and an integer column.
func runNullOperands(t *testing.T, b sqlBackend) {
	db := b.db(t)
	columns := map[string]nullColumn{
		"string": {core.FieldTypeString, "a", "b"},
		"int":    {core.FieldTypeInt, int64(7), int64(8)},
	}
	for name, c := range columns {
		t.Run(name, func(t *testing.T) {
			table := "null_operands_" + name
			b.load(t, db, sqlTable{name: table, key: []string{"id"}, columns: []sqlColumn{
				column[int64]("id"), {name: "v", kind: core.TypeInfo{Type: c.field}},
			}}, [][]any{{int64(1), c.held}})
			field := &filter.FieldSchema{ExternalName: "v", Table: table, Column: "v", DataType: c.field}
			schema := &filter.ResourceSchema{Name: table, Fields: map[string]*filter.FieldSchema{"v": field}}
			row := filter.ColumnRow(func(column filter.ColumnRef) (any, bool) {
				return c.held, column == filter.ColumnRef{Table: table, Column: "v"}
			})

			for _, tc := range nullOperandCases(field, c) {
				n := tc.n
				answer := nullOperandAnswer(t, b, db, schema, n)
				assert.Equal(t, tc.want.String(), answer, "the database: $%s %#v", n.Op, n.Value)

				got, err := filter.Evaluate(n, row)
				require.NoError(t, err, "$%s %#v", n.Op, n.Value)
				assert.Equal(t, tc.want, got, "Evaluate: $%s %#v", n.Op, n.Value)
			}
		})
	}
}

// nullOperandAnswer is n's answer for the one row of schema's table: the
// statement's on a SQL engine; on the Memory engine true when its filter
// selects the row, false when its negation does, and unknown otherwise.
func nullOperandAnswer(t *testing.T, b sqlBackend, db *sql.DB, schema *filter.ResourceSchema, n *filter.FilterNode) string {
	t.Helper()
	if b.engine != Memory {
		res, err := filter.NewSQLBuilder(schema, b.dialect).Build(&filter.FilterAST{Root: n})
		require.NoError(t, err, "$%s %#v", n.Op, n.Value)
		answer := dbtest.QueryRows[string](t, db, "SELECT CASE WHEN ("+res.Where+") THEN 'true' WHEN NOT ("+res.Where+") THEN 'false' ELSE 'unknown' END FROM "+schema.Name, res.Args...)
		require.Len(t, answer, 1)
		return answer[0]
	}
	selected := b.filterIDs(db, schema)
	holds, err := selected(t, filter.NewParsedFilter(&filter.FilterAST{Root: n}))
	require.NoError(t, err)
	fails, err := selected(t, filter.NewParsedFilter(&filter.FilterAST{Root: &filter.FilterNode{Op: filter.OpNot, Children: []*filter.FilterNode{n}}}))
	require.NoError(t, err)
	switch {
	case len(holds) > 0:
		return filter.True.String()
	case len(fails) > 0:
		return filter.False.String()
	}
	return filter.Unknown.String()
}

type nullOperandCase struct {
	n    *filter.FilterNode
	want filter.Truth
}

// nullOperandCases are each NULL spelling of c's type as a comparison's
// operand and as a set member beside c's held and other values.
func nullOperandCases(field *filter.FieldSchema, c nullColumn) []nullOperandCase {
	nulls := []any{nil, (*string)(nil), sql.NullString{}}
	if c.field == core.FieldTypeInt {
		nulls = []any{nil, (*int64)(nil), sql.NullInt64{}}
	}
	var cases []nullOperandCase
	add := func(op filter.FilterOperator, value any, want filter.Truth) {
		cases = append(cases, nullOperandCase{&filter.FilterNode{Field: field, Op: op, Value: value}, want})
	}
	for _, null := range nulls {
		for _, op := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpLt, filter.OpLte, filter.OpGt, filter.OpGte} {
			add(op, null, filter.Unknown)
		}
		add(filter.OpIn, []any{null}, filter.Unknown)
		add(filter.OpIn, []any{c.other, null}, filter.Unknown)
		add(filter.OpIn, []any{null, c.held}, filter.True)
		add(filter.OpNin, []any{null}, filter.Unknown)
		add(filter.OpNin, []any{c.other, null}, filter.Unknown)
		add(filter.OpNin, []any{null, c.held}, filter.False)
	}
	return cases
}
