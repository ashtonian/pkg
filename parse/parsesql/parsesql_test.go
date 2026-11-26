//go:build unit || concurrency

package parsesql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type tagged struct {
	ID   string   `json:"id"   db:"id"   parse:"filter"`
	Name string   `json:"name" db:"name" parse:"patch"`
	Tags []string `json:"tags" db:"-"    parse:"patch" relation:"tagged_tags,item_id,tag_id,id" indexed:"true"`
}

var taggedItems = parse.MustResource[tagged]("tagged")

func taggedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLiteHooked(t)
	for _, stmt := range []string{
		`CREATE TABLE tagged (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE tagged_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id))`,
		`INSERT INTO tagged VALUES ('a', 'one')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func renamePlan(t *testing.T, body string) *parse.UpdatePlan {
	t.Helper()
	p, err := taggedItems.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := taggedItems.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	return plan
}

// storedName reads row a's name through db, which may be any Queryer.
func storedName(t *testing.T, db Queryer) string {
	t.Helper()
	list, err := taggedItems.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	page, err := QueryPage(t.Context(), db, taggedItems, list)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	return page.Items[0].Name
}

// A request cancelled inside the caller's transaction still undoes the
// update's own writes, so the caller's transaction holds no partial update.
func TestUpdate_CancelledInTheCallersTransactionUndoesItsWrites(t *testing.T) {
	db := taggedDB(t)
	p, err := taggedItems.ParseMergePatch([]byte(`{"name":"renamed","tags":["red"]}`))
	require.NoError(t, err)
	plan, err := taggedItems.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("a"), parse.Scope{})
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
	_, err = Update(ctx, tx, taggedItems, plan)
	require.ErrorIs(t, err, context.Canceled)

	var name string
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT name FROM tagged WHERE id = 'a'`).Scan(&name))
	assert.Equal(t, "one", name, "the owner write is rolled back to the savepoint")
	require.NoError(t, tx.Commit(), "the caller's transaction is still usable")
}

func TestRefusesWhatItCannotRun(t *testing.T) {
	db := taggedDB(t)
	_, err := QueryPage(t.Context(), db, taggedItems, nil)
	assert.ErrorContains(t, err, "nil list plan")
	_, err = Update(t.Context(), db, taggedItems, nil)
	assert.ErrorContains(t, err, "nil update plan")
	_, err = UpdateBatch(t.Context(), db, taggedItems, nil)
	assert.ErrorContains(t, err, "nil update batch plan")
	_, err = Update(t.Context(), db, taggedItems, renamePlan(t, `{"name":"x"}`), parse.WithSettleTimeout(0))
	require.ErrorContains(t, err, "settle timeout 0s: must be positive")
	assert.Equal(t, "one", storedName(t, db), "a refused option sends no statement")

	empty, err := taggedItems.BuildUpdateBatch(filter.SQLiteDialect{}, nil, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	res, err := UpdateBatch(t.Context(), db, taggedItems, empty)
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
	db := taggedDB(t)
	renamed := func(t *testing.T, res parse.UpdateResult[tagged], err error) string {
		t.Helper()
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		return res.Changed[0].Name
	}

	res, err := Update(t.Context(), db, taggedItems, renamePlan(t, `{"name":"pool"}`))
	assert.Equal(t, "pool", renamed(t, res, err))

	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	res, err = Update(t.Context(), conn, taggedItems, renamePlan(t, `{"name":"connection"}`))
	assert.Equal(t, "connection", renamed(t, res, err))
	require.NoError(t, conn.Close())

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	res, err = Update(t.Context(), tx, taggedItems, renamePlan(t, `{"name":"transaction"}`))
	assert.Equal(t, "transaction", renamed(t, res, err))
	require.NoError(t, tx.Commit())

	hidden := noTransaction{DB: db}
	_, err = Update(t.Context(), hidden, taggedItems, renamePlan(t, `{"name":"hidden"}`))
	require.ErrorIs(t, err, ErrNoTransaction)
	require.ErrorContains(t, err, "parsesql: tagged: parsesql.noTransaction: an update needs")
	p, err := taggedItems.ParseMergePatch([]byte(`{"name":"hidden"}`))
	require.NoError(t, err)
	batch, err := taggedItems.BuildUpdateBatch(filter.SQLiteDialect{}, []parse.RowUpdate{{Key: parse.Key{"a"}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	_, err = UpdateBatch(t.Context(), hidden, taggedItems, batch)
	require.ErrorIs(t, err, ErrNoTransaction)
	assert.Equal(t, "transaction", storedName(t, hidden), "the refused updates wrote nothing, and a list runs on any DB")
}

// An update on a pool takes its connection under the caller's context: a
// request that ends while every connection is busy fails then, instead of
// waiting for one to come free.
func TestUpdate_WaitsForItsConnectionUnderTheCallersContext(t *testing.T) {
	db := taggedDB(t) // one connection
	held, err := db.Conn(t.Context())
	require.NoError(t, err)
	plan := renamePlan(t, `{"name":"x"}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Update(ctx, db, taggedItems, plan)
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
	assert.Equal(t, "one", storedName(t, db))
}
