//go:build integration

package corpus

import (
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// planPrice is a SQLite row whose decimal column holds its canonical text.
type planPrice struct {
	ID     int64           `json:"id"     db:"id"     parse:"filter,sort"`
	Amount decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort"`
}

// SQLite compares a decimal by its key for equality and sets, as it does for
// ranges, sorts and keyset seeks, so the one expression index the operations
// guide recommends, the key and then the keyset's tiebreaker, serves each.
func setPlansSQLiteDecimalKeyIndex(t *testing.T, engine Engine) {
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
