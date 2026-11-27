//go:build integration

package parsesql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

// An update takes its connection under the caller's context: a
// request that ends while every connection is busy fails then, instead of
// waiting for one to come free.
func TestUpdate_WaitsForItsConnectionUnderTheCallersContext(t *testing.T) {
	items, db := taggedItems(t) // one connection
	held, err := db.Conn(t.Context())
	require.NoError(t, err)
	p := rename(t, items, "x")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := items.Update(ctx, p, parse.ByID("a"), parse.Scope{})
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		// Freeing the connection lets an update that ignored its context
		// return, so the failure says so rather than hanging the test.
		require.NoError(t, held.Close())
		t.Fatalf("the update waited for a connection past its context: %v", <-done)
	}
	require.ErrorIs(t, err, context.Canceled)
	failed, ok := errors.AsType[*parse.StatementError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "begin", failed.Statement)
	require.NoError(t, held.Close())
	assert.Equal(t, "one", storedName(t, items))
}

// A transaction the provider begins takes its connection under the
// caller's context too, and once begun is bound to it no further.
func TestBegin_WaitsForItsConnectionUnderTheCallersContext(t *testing.T) {
	items, db := taggedItems(t) // one connection
	held, err := db.Conn(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := items.Provider().Begin(ctx)
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		require.NoError(t, held.Close())
		t.Fatalf("the transaction waited for a connection past its context: %v", <-done)
	}
	require.ErrorIs(t, err, context.Canceled)
	failed, ok := errors.AsType[*parse.StatementError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "begin", failed.Statement)
	require.NoError(t, held.Close())

	ctx, cancel = context.WithCancel(t.Context())
	tx, err := items.Provider().Begin(ctx)
	require.NoError(t, err)
	cancel()
	scoped, err := items.In(tx)
	require.NoError(t, err)
	_, err = scoped.Update(t.Context(), rename(t, scoped, "renamed"), parse.ByID("a"), parse.Scope{})
	require.NoError(t, err, "the transaction outlives the context it began under")
	require.NoError(t, tx.Commit(t.Context()))
	assert.Equal(t, "renamed", storedName(t, items))
}

// A statement that ends the whole transaction — a trigger's
// RAISE(ROLLBACK) — fails the update with its own error and nothing more:
// the transaction it ended keeps nothing and has nothing left to roll
// back, and the connection goes back to the pool outside any transaction.
// It is not discarded, so the in-memory database on it keeps its tables.
func TestUpdate_AStatementThatEndsTheTransactionLeavesTheConnectionUsable(t *testing.T) {
	items, db := taggedItems(t) // one connection, an in-memory database
	_, err := db.ExecContext(t.Context(), `CREATE TRIGGER tagged_refused BEFORE UPDATE OF name ON tagged
		WHEN NEW.name = 'refused' BEGIN SELECT RAISE(ROLLBACK, 'the name is refused'); END`)
	require.NoError(t, err)

	t.Run("in a transaction of the update's own", func(t *testing.T) {
		_, err := items.Update(t.Context(), rename(t, items, "refused"), parse.ByID("a"), parse.Scope{})
		require.ErrorContains(t, err, "parse: tagged: owner: constraint failed: the name is refused")
		assert.NotContains(t, err.Error(), "no transaction is active")
		_, err = items.Update(t.Context(), rename(t, items, "kept"), parse.ByID("a"), parse.Scope{})
		require.NoError(t, err)
		assert.Equal(t, "kept", storedName(t, items))
	})

	t.Run("in a transaction the provider began", func(t *testing.T) {
		tx, err := items.Provider().Begin(t.Context())
		require.NoError(t, err)
		scoped, err := items.In(tx)
		require.NoError(t, err)
		_, err = scoped.Update(t.Context(), rename(t, scoped, "refused"), parse.ByID("a"), parse.Scope{})
		require.ErrorContains(t, err, "parse: tagged: owner: constraint failed: the name is refused")
		require.ErrorContains(t, err, "no such savepoint", "the savepoint ended with the transaction")
		require.NoError(t, tx.Rollback(t.Context()), "nothing is left to roll back")
		_, err = items.Update(t.Context(), rename(t, items, "after"), parse.ByID("a"), parse.Scope{})
		require.NoError(t, err)
		assert.Equal(t, "after", storedName(t, items))
	})
}
