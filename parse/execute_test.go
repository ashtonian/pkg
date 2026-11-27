//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// A statement's failure names its resource's table once, and a
// transaction's own statement names none.
func TestStatementFailed(t *testing.T) {
	cause := assert.AnError
	for _, tc := range []struct {
		name, table, statement, want string
	}{
		{"a resource's statement", "widgets", "include tags", "parse: widgets: include tags: " + cause.Error()},
		{"a transaction's own statement", "", "commit", "commit: " + cause.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, parse.StatementFailed(tc.table, tc.statement, nil))
			err := parse.StatementFailed(tc.table, tc.statement, cause)
			require.ErrorIs(t, err, cause)
			require.EqualError(t, err, tc.want)
			failed, ok := errors.AsType[*parse.StatementError](err)
			require.True(t, ok)
			assert.Equal(t, tc.table, failed.Table)
			assert.Equal(t, tc.statement, failed.Statement)
		})
	}
}

type countedRow struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Counts map[string]int `json:"counts" db:"counts"`
}

// A row that fails to scan fails the statement that read it, whose error
// names the table once, then the statement, then the column.
func TestList_NamesAScanFailureOnce(t *testing.T) {
	p, db := providertest.SQLite(t)
	dbtest.Exec(t, db,
		`CREATE TABLE "counted" ("id" TEXT PRIMARY KEY, "counts" TEXT)`,
		`INSERT INTO "counted" VALUES ('a', 'null')`)
	r := parse.MustResource[countedRow](p, "counted")
	_, err := r.List(t.Context(), nil, parse.Scope{})
	require.EqualError(t, err, "parse: counted: page: column counts: JSON document decodes to nil map[string]int; use json.RawMessage to preserve JSON null")
}

// A resource scoped to a transaction is on its provider and is a view; a
// transaction another provider began, a wrapped one of another kind of
// provider, or none, is refused before it is used.
func TestIn(t *testing.T) {
	p, _ := providertest.SQLite(t)
	r := parsetest.ExecItems(p)
	assert.Same(t, p, r.Provider())
	tx, err := p.Begin(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(t.Context())) }()

	scoped, err := r.In(tx)
	require.NoError(t, err)
	assert.Same(t, p, scoped.Provider())
	err = scoped.Relate("again", parse.HasMany(r, "id"))
	require.ErrorContains(t, err, `parse: exec_items: relation "again": a view (WithPaths, In) declares no relations`)

	other, _ := providertest.SQLite(t)
	otherTx, err := other.Begin(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, otherTx.Rollback(t.Context())) }()
	_, err = r.In(otherTx)
	require.ErrorIs(t, err, parse.ErrProviderMismatch)
	require.ErrorContains(t, err, "parse: exec_items: transaction another sqlite provider began")

	_, err = r.In(parsepgx.Tx(nil))
	require.ErrorIs(t, err, parse.ErrProviderMismatch)
	require.ErrorContains(t, err, "parse: exec_items: postgres transaction for a resource on sqlite")
	_, err = r.In(nil)
	require.ErrorContains(t, err, "parse: exec_items: nil transaction")
}
