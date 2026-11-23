//go:build unit || integration

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matrixDB loads filtertest.Records into a table built by schema and
// evaluates each case's WHERE clause against it.
type matrixDB struct {
	db      *sql.DB
	dialect filter.Dialect
	schema  string
	// values maps a record to its insert arguments, in matrixColumns order.
	values func(r filtertest.Record) []any
	// casts wraps each insert placeholder by column (e.g. "%s::jsonb").
	casts map[string]string
}

var matrixColumns = []string{"id", "name", "count", "score", "amount", "active", "at", "ref", "doc", "tags", "loc"}

func runFilterMatrix(t *testing.T, mdb matrixDB) {
	t.Helper()
	loadRecords(t, mdb)
	schema, err := filtertest.Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	filtertest.Run(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		return queryRecordIDs(t, mdb, schema, f)
	})
}

// runSetMatrix runs the set cases: every scalar column's `$in` and `$nin`
// at each of filtertest.SetSizes.
func runSetMatrix(t *testing.T, mdb matrixDB) {
	t.Helper()
	loadRecords(t, mdb)
	schema, err := filtertest.Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	filtertest.RunSets(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		return queryRecordIDs(t, mdb, schema, f)
	})
}

// runMembershipMatrix runs the membership cases twice: compiled to their
// subqueries, and resolved through filtertest.Lookups first, as a memory
// backend resolves them. Both must select the same rows.
func runMembershipMatrix(t *testing.T, mdb matrixDB, columnTypes map[core.FieldType]string) {
	t.Helper()
	loadRecords(t, mdb)
	loadLookupTables(t, mdb, columnTypes)
	schema, err := filtertest.Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Run("subquery", func(t *testing.T) {
		filtertest.RunMembership(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			return queryRecordIDs(t, mdb, schema, f)
		})
	})
	t.Run("resolved", func(t *testing.T) {
		filtertest.RunMembership(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			resolved, err := filter.ResolveMemberships(context.Background(), f, filtertest.Lookups())
			if err != nil {
				return nil, err
			}
			return queryRecordIDs(t, mdb, schema, resolved)
		})
	})
}

// runLookupSets runs WhereIn lists of each type a lookup column holds, on
// the column types columnTypes names: each list binds as one set, one of
// them past PostgreSQL's 65,535 bind parameters.
func runLookupSets(t *testing.T, mdb matrixDB, columnTypes map[core.FieldType]string) {
	t.Helper()
	ua, ub := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), uuid.MustParse("b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12")
	ph := mdb.dialect.Placeholder
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"CREATE TABLE set_owners (id TEXT PRIMARY KEY)", nil},
		{"CREATE TABLE set_links (owner_id TEXT NOT NULL, n " + columnTypes[core.FieldTypeInt] + ", u " + columnTypes[core.FieldTypeUUID] + ", s " + columnTypes[core.FieldTypeString] + ")", nil},
		{"INSERT INTO set_owners VALUES ('a'), ('b'), ('c')", nil},
		{"INSERT INTO set_links VALUES ('a', 1, " + ph(1) + ", 'x'), ('b', 70000, " + ph(2) + ", 'y')", []any{ua.String(), ub.String()}},
	} {
		_, err := mdb.db.Exec(stmt.sql, stmt.args...)
		require.NoError(t, err, stmt.sql)
	}
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
			res, err := filter.NewSQLBuilder(schema, mdb.dialect).Build(ast)
			require.NoError(t, err)
			assert.Equal(t, 1, res.ParamCount, "one set parameter")
			rows, err := mdb.db.Query("SELECT id FROM set_owners WHERE "+res.Where+" ORDER BY id", res.Args...)
			require.NoError(t, err)
			var got []string
			for rows.Next() {
				var id string
				require.NoError(t, rows.Scan(&id))
				got = append(got, id)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			assert.Equal(t, tc.want, got)
		})
	}
}

// runPolicyMatrix runs the policy cases as lists: each walks its pages two
// rows at a time, so every case's WHERE also carries a cursor's seek, and
// its count must agree with the rows walked.
func runPolicyMatrix(t *testing.T, mdb matrixDB, columnTypes map[core.FieldType]string) {
	t.Helper()
	loadRecords(t, mdb)
	loadLookupTables(t, mdb, columnTypes)
	schema, err := filtertest.Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	schema.PrimaryKey = []string{"id"}
	lq, err := filter.NewListQuery(schema, nil, mdb.dialect)
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
			for _, id := range queryIDs(t, mdb.db, "SELECT id FROM records "+res.Fragment, res.Args) {
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
			require.NoError(t, mdb.db.QueryRow(res.CountSQL, res.CountArgs...).Scan(&total), res.CountSQL)
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

// loadLookupTables creates and fills filtertest.LookupTables beside records.
func loadLookupTables(t *testing.T, mdb matrixDB, columnTypes map[core.FieldType]string) {
	t.Helper()
	for _, lt := range filtertest.LookupTables() {
		cols := make([]string, len(lt.Columns))
		names := make([]string, len(lt.Columns))
		phs := make([]string, len(lt.Columns))
		for i, c := range lt.Columns {
			cols[i] = c.Name + " " + columnTypes[c.Type]
			names[i] = c.Name
			phs[i] = mdb.dialect.Placeholder(i + 1)
		}
		if _, err := mdb.db.Exec("CREATE TABLE " + lt.Name + " (" + strings.Join(cols, ", ") + ")"); err != nil {
			t.Fatalf("create %s: %v", lt.Name, err)
		}
		insert := "INSERT INTO " + lt.Name + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
		for _, row := range lt.Rows {
			if _, err := mdb.db.Exec(insert, row...); err != nil {
				t.Fatalf("insert %s %v: %v", lt.Name, row, err)
			}
		}
	}
}

func loadRecords(t *testing.T, mdb matrixDB) {
	t.Helper()
	if _, err := mdb.db.Exec(mdb.schema); err != nil {
		t.Fatalf("create records: %v", err)
	}
	phs := make([]string, len(matrixColumns))
	for i, col := range matrixColumns {
		phs[i] = mdb.dialect.Placeholder(i + 1)
		if c, ok := mdb.casts[col]; ok {
			phs[i] = fmt.Sprintf(c, phs[i])
		}
	}
	insert := "INSERT INTO records (" + strings.Join(matrixColumns, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	for _, r := range filtertest.Records() {
		if _, err := mdb.db.Exec(insert, mdb.values(r)...); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}
}

func queryRecordIDs(t *testing.T, mdb matrixDB, schema *filter.ResourceSchema, f *filter.ParsedFilter) ([]string, error) {
	t.Helper()
	res, err := filter.NewSQLBuilder(schema, mdb.dialect).Build(f.AST())
	if err != nil {
		return nil, err
	}
	q := "SELECT id FROM records WHERE " + res.Where
	rows, err := mdb.db.Query(q, res.Args...)
	if err != nil {
		t.Fatalf("%s %v: %v", q, res.Args, err)
	}
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids, nil
}

// jsonText renders v as JSON text, or NULL for a nil map/slice/document.
func jsonText(v any) any {
	switch x := v.(type) {
	case json.RawMessage:
		if x == nil {
			return nil
		}
		return string(x)
	case []string:
		if x == nil {
			return nil
		}
	case []*string:
		if x == nil {
			return nil
		}
	case map[string]float64:
		if x == nil {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func uuidText(r filtertest.Record) any {
	if r.Ref == nil {
		return nil
	}
	return r.Ref.String()
}
