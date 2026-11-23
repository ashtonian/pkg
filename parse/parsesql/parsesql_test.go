//go:build unit

package parsesql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
	db := dbtest.SQLite(t)
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

// cancelOn is a caller's transaction whose context is cancelled as the
// first statement naming trigger is sent: a request that ends between an
// update's owner statement and its relation statements.
type cancelOn struct {
	*sql.Tx
	trigger string
	cancel  context.CancelFunc
}

func (c cancelOn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, c.trigger) {
		c.cancel()
	}
	return c.Tx.ExecContext(ctx, query, args...)
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
	_, err = Update(ctx, cancelOn{Tx: tx, trigger: "tagged_tags", cancel: cancel}, taggedItems, plan)
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

	empty, err := taggedItems.BuildUpdateBatch(filter.SQLiteDialect{}, nil, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	res, err := UpdateBatch(t.Context(), db, taggedItems, empty)
	require.NoError(t, err)
	assert.Zero(t, res, "an empty batch runs nothing")
}
