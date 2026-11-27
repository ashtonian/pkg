//go:build unit

package parsesql

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A request cancelled inside the caller's transaction still undoes the
// update's own writes, so the caller's transaction holds no partial update.
func TestUpdate_CancelledInTheCallersTransactionUndoesItsWrites(t *testing.T) {
	items := taggedItems()
	db := taggedDB(t)
	p, err := items.ParseMergePatch([]byte(`{"name":"renamed","tags":["red"]}`))
	require.NoError(t, err)
	plan, err := items.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The request ends as the first relation statement is sent.
	ctx = dbtest.OnStatement(ctx, func(_ context.Context, query string) {
		if strings.Contains(query, "tagged_tags") {
			cancel()
		}
	})
	_, err = Update(ctx, tx, items, plan)
	require.ErrorIs(t, err, context.Canceled)

	var name string
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT name FROM tagged WHERE id = 'a'`).Scan(&name))
	assert.Equal(t, "one", name, "the owner write is rolled back to the savepoint")
	require.NoError(t, tx.Commit(), "the caller's transaction is still usable")
}

func TestRefusesWhatItCannotRun(t *testing.T) {
	items := taggedItems()
	db := taggedDB(t)
	_, err := QueryPage(t.Context(), db, items, nil)
	assert.ErrorContains(t, err, "nil list plan")
	_, err = Update(t.Context(), db, items, nil)
	assert.ErrorContains(t, err, "nil update plan")
	_, err = UpdateBatch(t.Context(), db, items, nil)
	assert.ErrorContains(t, err, "nil update batch plan")
	_, err = Update(t.Context(), db, items, renamePlan(t, items, `{"name":"x"}`), parse.WithSettleTimeout(0))
	require.ErrorContains(t, err, "settle timeout 0s: must be positive")
	assert.Equal(t, "one", storedName(t, db, items), "a refused option sends no statement")

	empty, err := items.BuildUpdateBatch(filter.SQLiteDialect{}, nil, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	res, err := UpdateBatch(t.Context(), db, items, empty)
	require.NoError(t, err)
	assert.Zero(t, res, "an empty batch runs nothing")
}

// noTransaction runs statements but is no *sql.Tx and begins none: a
// wrapper that hides the *sql.DB it holds.
type noTransaction struct {
	DB
}

// An update runs in a transaction it begins on a pool or a connection, and
// under a savepoint in a caller's *sql.Tx; on any other DB it is refused
// before a statement is sent, as its statements might each take another of
// the pool's connections. Lists run on any.
func TestUpdate_HoldsItsTransactionOnlyWhereItCan(t *testing.T) {
	items := taggedItems()
	db := taggedDB(t)
	renamed := func(t *testing.T, res parse.UpdateResult[tagged], err error) string {
		t.Helper()
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		return res.Changed[0].Name
	}

	res, err := Update(t.Context(), db, items, renamePlan(t, items, `{"name":"pool"}`))
	assert.Equal(t, "pool", renamed(t, res, err))

	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	res, err = Update(t.Context(), conn, items, renamePlan(t, items, `{"name":"connection"}`))
	assert.Equal(t, "connection", renamed(t, res, err))
	require.NoError(t, conn.Close())

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	res, err = Update(t.Context(), tx, items, renamePlan(t, items, `{"name":"transaction"}`))
	assert.Equal(t, "transaction", renamed(t, res, err))
	require.NoError(t, tx.Commit())

	hidden := noTransaction{DB: db}
	_, err = Update(t.Context(), hidden, items, renamePlan(t, items, `{"name":"hidden"}`))
	require.ErrorIs(t, err, ErrNoTransaction)
	require.ErrorContains(t, err, "parsesql: tagged: parsesql.noTransaction: an update needs")
	p, err := items.ParseMergePatch([]byte(`{"name":"hidden"}`))
	require.NoError(t, err)
	batch, err := items.BuildUpdateBatch(filter.SQLiteDialect{}, []parse.RowUpdate{{Key: parse.Key{"a"}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	_, err = UpdateBatch(t.Context(), hidden, items, batch)
	require.ErrorIs(t, err, ErrNoTransaction)
	assert.Equal(t, "transaction", storedName(t, hidden, items), "the refused updates wrote nothing, and a list runs on any DB")
}
