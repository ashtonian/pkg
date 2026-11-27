//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Every operator × type case on each backend: SQLite holds documents and
// arrays as JSON text, timestamps as their canonical text, booleans as
// integers and decimals in a NUMERIC column; PostgreSQL, with PostGIS for
// the points, the storage model's native types: jsonb, TEXT[],
// timestamptz, uuid, numeric.
func TestFilterMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db, schema := loadRecords(t, b)
		filtertest.Run(t, b.filterIDs(db, schema))
	}, onPostGIS())
}

// `$in` / `$nin` sets of every size, each bound as one parameter: a JSON
// array on SQLite, an array on PostgreSQL.
func TestSetMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db, schema := loadRecords(t, b)
		filtertest.RunSets(t, b.filterIDs(db, schema))
	})
}

// Membership constraints, as subqueries and resolved, over lookup columns
// of each backend's types: TEXT, INTEGER and decimal TEXT on SQLite; UUID,
// BIGINT and NUMERIC on PostgreSQL.
func TestMembershipMatrix(t *testing.T) {
	runSQLBackends(t, runMembershipMatrix)
}

// WhereIn lists of integers, UUIDs and text, each one set parameter, one
// past PostgreSQL's 65,535 bind parameters.
func TestLookupSets(t *testing.T) {
	runSQLBackends(t, runLookupSets)
}

// Policy trees, each listed page by page.
func TestPolicyMatrix(t *testing.T) {
	runSQLBackends(t, runPolicyMatrix)
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
	table := schemaTable(schema).holding("amount", sqliteEngine, "NUMERIC")
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
			res, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
			require.NoError(t, err)
			assert.Equal(t, 1, res.ParamCount, "one set parameter")
			assert.Equal(t, tc.want, queryRows[string](t, db, "SELECT id FROM set_owners WHERE "+res.Where+" ORDER BY id", res.Args...))
		})
	}
}

// runPolicyMatrix runs the policy cases as lists: each walks its pages two
// rows at a time, so every case's WHERE also carries a cursor's seek, and
// its count must agree with the rows walked.
func runPolicyMatrix(t *testing.T, b sqlBackend) {
	t.Helper()
	db, schema := loadRecords(t, b)
	loadLookupTables(t, b, db)
	schema.PrimaryKey = []string{"id"}
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
			for _, id := range queryRows[string](t, db, "SELECT id FROM records "+res.Fragment, res.Args...) {
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
