//go:build integration

package parse_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// planDoc is a document store's row: a GIN-indexed jsonb document with
// declared paths.
type planDoc struct {
	ID  int64           `json:"id"  db:"id"  parse:"filter" filter_ops:"eq,in"`
	Doc json.RawMessage `json:"doc" db:"doc" parse:"filter" paths:"k:string;n:int;tags:[]string"`
}

// The plans PostgreSQL gives the renderings the builder chooses, custom and
// generic (pgx caches prepared statements, and a cached statement may run a
// generic plan): a GIN index serves a jsonb path's equality and small sets
// under $and and $or, and an array path's `$some` of an element equal to a
// value or in a small set, and its `$contains`; a large path set is a hash
// join, and a large column set is served by the column's index.
func TestSetPlans_PostgreSQL(t *testing.T) {
	ctx := t.Context()
	conn, err := pgx.ConnectConfig(ctx, dbtest.PostgresConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	schema, err := filter.GenerateFilterSchema[planDoc](core.NewSchemaGenerator())
	require.NoError(t, err)
	table := schema.Name
	for _, stmt := range []string{
		"CREATE TABLE " + table + " (id bigint PRIMARY KEY, doc jsonb NOT NULL)",
		"INSERT INTO " + table + " SELECT g, jsonb_build_object('k', 'v' || g, 'n', g % 100, 'tags', jsonb_build_array('t' || g, 'x')) FROM generate_series(1, 50000) g",
		"CREATE INDEX " + table + "_gin ON " + table + " USING gin (doc jsonb_path_ops)",
		"ANALYZE " + table,
		"SET max_parallel_workers_per_gather = 0",
	} {
		_, err := conn.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	parser := filter.NewFilterParser(schema, filtertest.SetConfig())

	plans := func(t *testing.T, body string) (custom, generic string) {
		t.Helper()
		ast, err := parser.Parse(body)
		require.NoError(t, err)
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
		require.NoError(t, err)
		query := "SELECT id FROM " + table + " WHERE " + res.Where

		rows, err := conn.Query(ctx, "EXPLAIN (COSTS OFF) "+query, res.Args...)
		require.NoError(t, err)
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		custom = strings.Join(lines, "\n")

		// GENERIC_PLAN plans the statement with its parameters unbound, as
		// the plan cache's generic plan does; it takes no parameter values,
		// so it goes over the simple protocol unchanged.
		results, err := conn.PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN, COSTS OFF) "+query).ReadAll()
		require.NoError(t, err)
		lines = lines[:0]
		for _, r := range results {
			for _, row := range r.Rows {
				lines = append(lines, string(row[0]))
			}
		}
		return custom, strings.Join(lines, "\n")
	}
	keys := func(n int) string {
		vs := make([]string, n)
		for i := range vs {
			vs[i] = fmt.Sprintf(`"v%d"`, i*3)
		}
		return "[" + strings.Join(vs, ",") + "]"
	}
	ids := func(n int) string {
		vs := make([]string, n)
		for i := range vs {
			vs[i] = fmt.Sprint(i * 3)
		}
		return "[" + strings.Join(vs, ",") + "]"
	}
	gin := "Bitmap Index Scan on " + table + "_gin"

	t.Run("path equality uses the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"doc.k":"v777"}`)
		assert.Contains(t, custom, gin)
		assert.Contains(t, generic, gin)
	})
	t.Run("small sets under or use the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"$or":[{"doc.k":{"$in":`+keys(16)+`}},{"doc.n":5}]}`)
		for _, p := range []string{custom, generic} {
			assert.Contains(t, p, "BitmapOr")
			assert.Equal(t, 2, strings.Count(p, gin), p)
		}
	})
	t.Run("small sets under and use the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"$and":[{"doc.k":{"$in":`+keys(16)+`}},{"doc.n":5}]}`)
		assert.Contains(t, custom, gin)
		assert.Contains(t, generic, gin)
	})
	t.Run("an array element equal to a value uses the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"doc.tags":{"$some":{"$eq":"t777"}}}`)
		assert.Contains(t, custom, gin)
		assert.Contains(t, generic, gin)
	})
	t.Run("an array element in a small set uses the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"doc.tags":{"$some":{"$in":["t1","t2","t3"]}}}`)
		assert.Contains(t, custom, gin)
		assert.Contains(t, generic, gin)
	})
	t.Run("array containment uses the GIN index", func(t *testing.T) {
		custom, generic := plans(t, `{"doc.tags":{"$contains":["t777","x"]}}`)
		assert.Contains(t, custom, gin)
		assert.Contains(t, generic, gin)
	})
	t.Run("a large path set is a hash join in a generic plan", func(t *testing.T) {
		_, generic := plans(t, `{"doc.k":{"$in":`+keys(100_000)+`}}`)
		assert.Regexp(t, regexp.MustCompile(`Hash (Right )?(Semi )?Join`), generic)
		assert.NotContains(t, generic, "= ANY", "the set is not probed row by row")
	})
	t.Run("a large path set under or is a hashed subplan", func(t *testing.T) {
		_, generic := plans(t, `{"$or":[{"doc.k":{"$in":`+keys(100_000)+`}},{"id":7}]}`)
		assert.Contains(t, generic, "hashed SubPlan")
	})
	t.Run("a large array containment hashes its list once", func(t *testing.T) {
		// Under $not, where containment would not be exact: each row's
		// elements probe one hash of the list, built once and reused as the
		// per-row subplan is rescanned, and the list's distinct count is an
		// InitPlan.
		body := `{"$not":{"doc.tags":{"$contains":` + keys(100_000) + `}}}`
		_, generic := plans(t, body)
		assert.Regexp(t, regexp.MustCompile(`Hash (Semi )?Join`), generic)
		assert.Contains(t, generic, "InitPlan")

		ast, err := parser.Parse(body)
		require.NoError(t, err)
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
		require.NoError(t, err)
		rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) SELECT id FROM "+table+" WHERE "+res.Where, res.Args...)
		require.NoError(t, err)
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		analyzed := strings.Join(lines, "\n")
		assert.Regexp(t, regexp.MustCompile(`Seq Scan on `+table+` \(actual rows=50000(\.00)? loops=1\)`), analyzed, "every row qualifies")
		assert.Regexp(t, regexp.MustCompile(`Hash \(actual rows=100000(\.00)? loops=1\)`), analyzed, "the list is hashed once")
	})
	t.Run("a large column set uses the column's index in a generic plan", func(t *testing.T) {
		_, generic := plans(t, `{"id":{"$in":`+ids(100_000)+`}}`)
		assert.Contains(t, generic, table+"_pkey")
		assert.Contains(t, generic, "= ANY ($1)")
	})
}

// planPrice is a SQLite row whose decimal column holds its canonical text.
type planPrice struct {
	ID     int64           `json:"id"     db:"id"     parse:"filter,sort"`
	Amount decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort"`
}

// SQLite compares a decimal by its key for equality and sets, as it does for
// ranges, sorts and keyset seeks, so the one expression index the operations
// guide recommends, the key and then the keyset's tiebreaker, serves each.
func TestSetPlans_SQLiteDecimalKeyIndex(t *testing.T) {
	db := dbtest.SQLite(t)
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[planPrice](g)
	require.NoError(t, err)
	schema.PrimaryKey = []string{"id"}
	sorts, err := filter.GenerateSortSchema[planPrice](g)
	require.NoError(t, err)
	table := schema.Name
	index := table + "_amount_key"
	for _, stmt := range []string{
		"CREATE TABLE " + table + " (id INTEGER PRIMARY KEY, amount TEXT NOT NULL)",
		"INSERT INTO " + table + " WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 20000) SELECT n, (n % 1000) || '.' || (n % 7) FROM g",
		"CREATE INDEX " + index + " ON " + table + " (parse_decimal_key(amount), id)",
		"ANALYZE",
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	lq, err := filter.NewListQuery(schema, sorts, filter.SQLiteDialect{})
	require.NoError(t, err)

	plan := func(t *testing.T, opts ...filter.ListOption) string {
		t.Helper()
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM "+table+" "+res.Fragment, res.Args...)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		var lines []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			lines = append(lines, detail)
		}
		require.NoError(t, rows.Err())
		return strings.Join(lines, "\n")
	}
	// The query reads only the key and id, so the index covers it.
	search := regexp.MustCompile(`SEARCH ` + table + ` USING COVERING INDEX ` + index + ` \(<expr>[=<>]`)
	sorted := func(t *testing.T, p string) {
		t.Helper()
		assert.Contains(t, p, " USING COVERING INDEX "+index, p)
		assert.NotContains(t, p, "TEMP B-TREE", "the index orders the page")
	}

	t.Run("equality", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":"500.3"}`)))
	})
	t.Run("a set", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":{"$in":["500.3","7.0","12"]}}`)))
	})
	t.Run("a range", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":{"$gt":"500","$lte":"510.5"}}`)))
	})
	t.Run("a sort", func(t *testing.T) {
		sorted(t, plan(t, filter.ListSort("amount")))
		sorted(t, plan(t, filter.ListSort("-amount")))
	})
	t.Run("a keyset seek", func(t *testing.T) {
		first, err := lq.Build(filter.ListSort("amount"))
		require.NoError(t, err)
		token, err := first.Cursor.Token(false, func(column string) (any, error) {
			return map[string]any{"amount": "500.3", "id": int64(1500)}[column], nil
		})
		require.NoError(t, err)
		cursor, err := filter.EncodeCursor(token)
		require.NoError(t, err)
		p := plan(t, filter.ListSort("amount"), filter.ListCursor(cursor))
		assert.Regexp(t, search, p)
		sorted(t, p)
	})
}
