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
