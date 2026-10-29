//go:build integration

package parse_test

import (
	"database/sql"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Decimal filters select by value and a decimal patch writes back exactly,
// over every column type a decimal is held in: PostgreSQL numeric, with and
// without a declared scale, and SQLite NUMERIC and REAL.
func TestDecimalSQL(t *testing.T) {
	for _, backend := range []struct {
		name       string
		open       func(*testing.T) *sql.DB
		dialect    parse.Dialect
		columnType string
	}{
		{"sqlite NUMERIC", func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{}, "NUMERIC"},
		{"sqlite REAL", func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{}, "REAL"},
		{"postgres numeric", func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{}, "NUMERIC"},
		{"postgres numeric(12,4)", func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{}, "NUMERIC(12,4)"},
	} {
		t.Run(backend.name, func(t *testing.T) {
			db := backend.open(t)
			_, err := db.Exec(`CREATE TABLE ledger_lines (id TEXT PRIMARY KEY, amount ` + backend.columnType +
				` NOT NULL, fee ` + backend.columnType + `)`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO ledger_lines VALUES
				('a','10.25',NULL), ('b','9.5','0.25'), ('c','-1',NULL),
				('d','2.00','1'), ('e','100',NULL), ('g','2','2.0')`)
			require.NoError(t, err)
			resource := parse.MustResource[ledgerLine]("ledger_lines")

			for filterDoc, want := range map[string]string{
				`{"amount":{"$gt":"9.5"}}`:                                  "ae",
				`{"amount":{"$gt":9.5}}`:                                    "ae",
				`{"amount":{"$lt":"10.25"}}`:                                "bcdg",
				`{"amount":"2"}`:                                            "dg",
				`{"amount":{"$in":["0.5e1",100,"-1.0"]}}`:                   "ce",
				`{"amount":{"$nin":["2.000","10.25"]}}`:                     "bce",
				`{"fee":{"$lt":"1.0"}}`:                                     "b",
				`{"fee":{"$gte":"1"}}`:                                      "dg",
				`{"$and":[{"amount":{"$gte":-1}},{"amount":{"$lte":"2"}}]}`: "cdg",
			} {
				got := listLedger(t, db, backend.dialect, resource, url.Values{"filter": {filterDoc}})
				assert.Equal(t, want, got, filterDoc)
			}

			p, err := resource.ParseMergePatch([]byte(`{"amount":"10.50","fee":"0.10"}`))
			require.NoError(t, err)
			update, err := resource.BuildUpdate(backend.dialect, p, parse.ByID("c"), parse.Scope{})
			require.NoError(t, err)
			var row ledgerLine
			require.NoError(t, db.QueryRow(update.Owner.SQL, update.Owner.Args...).Scan(&row.ID, &row.Amount, &row.Fee))
			assert.True(t, decimal.RequireFromString("10.5").Equal(row.Amount), row.Amount.String())
			require.NotNil(t, row.Fee)
			assert.True(t, decimal.RequireFromString("0.1").Equal(*row.Fee), row.Fee.String())
			assert.Equal(t, "c", listLedger(t, db, backend.dialect, resource, url.Values{"amount": {"10.5"}}))
		})
	}
}

// PostgreSQL holds and compares decimals beyond float64's precision.
func TestDecimalSQL_PostgresIsExact(t *testing.T) {
	db := dbtest.Postgres(t)
	_, err := db.Exec(`CREATE TABLE ledger_lines (id TEXT PRIMARY KEY, amount NUMERIC NOT NULL, fee NUMERIC)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ledger_lines VALUES ('a','0.1',NULL), ('b','0.10000000000000001',NULL),
		('c','123456789012345678901234567890.5',NULL), ('d','123456789012345678901234567890.25',NULL)`)
	require.NoError(t, err)
	resource := parse.MustResource[ledgerLine]("ledger_lines")
	for filterDoc, want := range map[string]string{
		`{"amount":"0.1"}`:                                       "a",
		`{"amount":"0.10000000000000001"}`:                       "b",
		`{"amount":{"$gt":0.1,"$lt":1}}`:                         "b",
		`{"amount":{"$gt":"123456789012345678901234567890.25"}}`: "c",
	} {
		assert.Equal(t, want, listLedger(t, db, filter.PostgresDialect{}, resource, url.Values{"filter": {filterDoc}}), filterDoc)
	}

	// Paging resumes from a cursor that holds the exact key.
	var walked []string
	cursor := ""
	for n := 0; ; n++ {
		require.Less(t, n, 5, "walk did not terminate")
		q := url.Values{"sort": {"amount"}, "page_size": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := resource.ParseList(q)
		require.NoError(t, err)
		plan, err := resource.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
		require.NoError(t, err)
		page, err := resource.Page(queryLedger(t, db, plan), plan)
		require.NoError(t, err)
		for _, it := range page.Items {
			walked = append(walked, it.ID)
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		cursor = string(page.PageInfo.EndCursor)
	}
	assert.Equal(t, []string{"a", "b", "d", "c"}, walked)
}

// listLedger runs one list request and returns the matched ids, sorted.
func listLedger(t *testing.T, db *sql.DB, d parse.Dialect, resource *parse.Resource[ledgerLine], q url.Values) string {
	t.Helper()
	req, err := resource.ParseList(q)
	require.NoError(t, err)
	plan, err := resource.BuildList(d, req, parse.Scope{})
	require.NoError(t, err)
	var ids []string
	for _, it := range queryLedger(t, db, plan) {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	return strings.Join(ids, "")
}

func queryLedger(t *testing.T, db *sql.DB, plan *parse.ListPlan) []ledgerLine {
	t.Helper()
	rows, err := db.Query(plan.Page.SQL, plan.Page.Args...)
	require.NoError(t, err, plan.Page.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []ledgerLine
	for rows.Next() {
		var it ledgerLine
		require.NoError(t, rows.Scan(&it.ID, &it.Amount, &it.Fee))
		out = append(out, it)
	}
	require.NoError(t, rows.Err())
	return out
}
