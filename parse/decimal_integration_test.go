//go:build integration

package parse_test

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// Decimal filters select by value and a decimal patch writes back exactly,
// over every column type a decimal is held in: PostgreSQL numeric, with and
// without a declared scale, and SQLite NUMERIC and REAL.
func TestDecimalSQL(t *testing.T) {
	columnTypes := map[sqlEngine][]string{sqliteEngine: {"NUMERIC", "REAL"}, postgresEngine: {"NUMERIC", "NUMERIC(12,4)"}}
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		for _, columnType := range columnTypes[b.engine] {
			t.Run(columnType, func(t *testing.T) { runDecimalSQL(t, b.with(storing(core.FieldTypeDecimal, columnType))) })
		}
	})
}

func ledgerTable() sqlTable {
	table := sqlTable{name: "ledger_lines", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[decimal.Decimal]("amount").required(), column[decimal.Decimal]("fee"),
	}}
	return table
}

func runDecimalSQL(t *testing.T, b sqlBackend) {
	db := b.open(t)
	b.create(t, db, ledgerTable())
	_, err := db.ExecContext(t.Context(), `INSERT INTO ledger_lines VALUES
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
		got := listLedger(t, db, b.dialect, resource, url.Values{"filter": {filterDoc}})
		assert.Equal(t, want, got, filterDoc)
	}

	p, err := resource.ParseMergePatch([]byte(`{"amount":"10.50","fee":"0.10"}`))
	require.NoError(t, err)
	update, err := resource.BuildUpdate(b.dialect, p, parse.ByID("c"), parse.Scope{})
	require.NoError(t, err)
	var row ledgerLine
	require.NoError(t, db.QueryRowContext(t.Context(), update.Owner.SQL, update.Owner.Args...).Scan(&row.ID, &row.Amount, &row.Fee))
	assert.True(t, decimal.RequireFromString("10.5").Equal(row.Amount), row.Amount.String())
	require.NotNil(t, row.Fee)
	assert.True(t, decimal.RequireFromString("0.1").Equal(*row.Fee), row.Fee.String())
	assert.Equal(t, "c", listLedger(t, db, b.dialect, resource, url.Values{"amount": {"10.5"}}))
}

// PostgreSQL holds and compares decimals beyond float64's precision.
func TestDecimalSQL_PostgresIsExact(t *testing.T) {
	b := sqlBackendOf(postgresEngine)
	db := b.open(t)
	b.create(t, db, ledgerTable())
	_, err := db.ExecContext(t.Context(), `INSERT INTO ledger_lines VALUES ('a','0.1',NULL), ('b','0.10000000000000001',NULL),
		('c','123456789012345678901234567890.5',NULL), ('d','123456789012345678901234567890.25',NULL)`)
	require.NoError(t, err)
	resource := parse.MustResource[ledgerLine]("ledger_lines")
	for filterDoc, want := range map[string]string{
		`{"amount":"0.1"}`:                                       "a",
		`{"amount":"0.10000000000000001"}`:                       "b",
		`{"amount":{"$gt":0.1,"$lt":1}}`:                         "b",
		`{"amount":{"$gt":"123456789012345678901234567890.25"}}`: "c",
	} {
		assert.Equal(t, want, listLedger(t, db, b.dialect, resource, url.Values{"filter": {filterDoc}}), filterDoc)
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
		plan, err := resource.BuildList(b.dialect, req, parse.Scope{})
		require.NoError(t, err)
		page := listPage(t, db, resource, b.dialect, plan)
		walked = append(walked, itemIDs(page.Items, ledgerLineID)...)
		if !page.PageInfo.HasNextPage {
			break
		}
		cursor = string(page.PageInfo.EndCursor)
	}
	assert.Equal(t, []string{"a", "b", "d", "c"}, walked)
}

func ledgerLineID(l ledgerLine) string { return l.ID }

// listLedger runs one list request and returns the matched ids, sorted.
func listLedger(t *testing.T, db *sql.DB, d parse.Dialect, resource *parse.Resource[ledgerLine], q url.Values) string {
	t.Helper()
	req, err := resource.ParseList(q)
	require.NoError(t, err)
	plan, err := resource.BuildList(d, req, parse.Scope{})
	require.NoError(t, err)
	return strings.Join(sortedIDs(listPage(t, db, resource, d, plan).Items, ledgerLineID), "")
}
