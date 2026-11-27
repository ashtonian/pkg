//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Same-element predicates over JSON arrays on each backend: JSON text on
// SQLite, jsonb on PostgreSQL.
func TestElementPredicates(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadOrders(t, b)
		runElementCases(t, orderResource(t, p), db)
	})
}

// loadOrders writes orderRows into orders on a fresh database of b: its id
// and parent_id text, its other columns documents.
func loadOrders(t *testing.T, b sqlBackend) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	table := sqlTable{name: "orders", key: []string{"id"}}
	for _, name := range orderColumns {
		c := column[json.RawMessage](name)
		if name == "id" || name == "parent_id" {
			c = column[string](name)
		}
		table.columns = append(table.columns, c)
	}
	rows := make([][]any, len(orderRows))
	for i, r := range orderRows {
		rows[i] = r[:]
	}
	b.load(t, db, table, rows)
	return p, db
}

// runElementCases runs each case's list SQL, compiled by orders, on db.
func runElementCases(t *testing.T, orders *parse.Resource[order], db *sql.DB) {
	t.Helper()
	for _, tc := range slices.Concat(elementCases, elementRelationCases) {
		t.Run(tc.name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			require.NoError(t, err)
			plan, err := orders.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			got := rowIDs(t, queryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
			slices.Sort(got)
			assert.Equal(t, tc.want, got, plan.Page.SQL)
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
