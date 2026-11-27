//go:build unit

package parsesql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A request cancelled inside the caller's transaction still undoes the
// update's own writes, so the caller's transaction holds no partial update.
func TestUpdate_CancelledInTheCallersTransactionUndoesItsWrites(t *testing.T) {
	items, db := taggedItems(t)
	p, err := items.ParseMergePatch([]byte(`{"name":"renamed","tags":["red"]}`))
	require.NoError(t, err)

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	scoped, err := items.In(Tx(tx))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The request ends as the first relation statement is sent.
	ctx = dbtest.OnStatement(ctx, func(_ context.Context, query string) {
		if strings.Contains(query, "tagged_tags") {
			cancel()
		}
	})
	_, err = scoped.Update(ctx, p, parse.ByID("a"), parse.Scope{})
	require.ErrorIs(t, err, context.Canceled)

	var name string
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT name FROM tagged WHERE id = 'a'`).Scan(&name))
	assert.Equal(t, "one", name, "the owner write is rolled back to the savepoint")
	require.NoError(t, tx.Commit(), "the caller's transaction is still usable")
}

// An update runs in a transaction of its own on the provider's database, or
// under a savepoint of the transaction its resource is scoped to, which
// keeps the update only when it commits.
func TestUpdate_RunsInItsOwnOrTheScopedTransaction(t *testing.T) {
	items, db := taggedItems(t)
	renamed := func(t *testing.T, res parse.UpdateResult[tagged], err error) string {
		t.Helper()
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		return res.Changed[0].Name
	}

	res, err := items.Update(t.Context(), rename(t, items, "own"), parse.ByID("a"), parse.Scope{})
	assert.Equal(t, "own", renamed(t, res, err))

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	scoped, err := items.In(Tx(tx))
	require.NoError(t, err)
	res, err = scoped.Update(t.Context(), rename(t, scoped, "rolled back"), parse.ByID("a"), parse.Scope{})
	assert.Equal(t, "rolled back", renamed(t, res, err))
	assert.Equal(t, "rolled back", storedName(t, scoped), "the transaction reads its own write")
	require.NoError(t, tx.Rollback())
	assert.Equal(t, "own", storedName(t, items), "a rolled-back transaction keeps nothing")

	begun, err := items.Provider().Begin(t.Context())
	require.NoError(t, err)
	scoped, err = items.In(begun)
	require.NoError(t, err)
	res, err = scoped.Update(t.Context(), rename(t, scoped, "committed"), parse.ByID("a"), parse.Scope{})
	assert.Equal(t, "committed", renamed(t, res, err))
	require.NoError(t, begun.Commit(t.Context()))
	require.NoError(t, begun.Rollback(t.Context()), "a rollback after the commit does nothing")
	assert.Equal(t, "committed", storedName(t, items))
}

func TestUpdateBatch_EmptyRunsNothing(t *testing.T) {
	items, _ := taggedItems(t)
	ctx := dbtest.OnStatement(t.Context(), func(_ context.Context, query string) {
		t.Errorf("an empty batch sent %s", query)
	})
	res, err := items.UpdateBatch(ctx, nil, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	assert.Zero(t, res)
}

// A statement the database refuses fails as the statement the resource
// named, for a log attribute or a metric label.
func TestQuery_NamesTheFailedStatement(t *testing.T) {
	items, _ := taggedItems(t)
	_, err := items.Query(t.Context(), parse.Statement{SQL: "SELECT id, name FROM missing"})
	failed, ok := errors.AsType[*parse.StatementError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "query", failed.Statement)
	require.ErrorContains(t, err, "parse: tagged: query: ")
}
