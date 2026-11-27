// Package sqlexec is the executor parse's SQL providers share (parsesql,
// parsepgx): it compiles each composed request (internal/backend) for the
// provider's dialect through internal/sqlplan, sends the statements over
// the provider's connection in the order and round trips execute.go
// describes, and hands their rows to the request's sink. A provider package
// supplies only how statements are sent (Conn), how their rows are read
// (backend.Rows) and how a transaction is held (DB, Tx), and wraps them
// with NewDB, NewTx or Provider.
package sqlexec

import (
	"context"
	"errors"
	"time"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// Provider is the backend.Provider of a SQL provider named name, whose
// statements compile for d and run on db, each statement that settles an
// update bounded by settle (Settle), positive. A SQL provider supports
// every capability.
func Provider(name string, d filter.Dialect, db DB, settle time.Duration) backend.Provider {
	p := backend.Provider{Name: name, Dialect: d, DB: NewDB(db, d, settle), Capabilities: backend.AllCapabilities}
	return p
}

// NewDB is db as a backend.DB: its requests compile for d and run on db,
// and each statement that settles an update is bounded by settle.
func NewDB(db DB, d filter.Dialect, settle time.Duration) backend.DB {
	wrapped := database{executor: executor{conn: db, dialect: d, settle: settle}, db: db}
	return wrapped
}

// NewTx is tx, a transaction of the application's own a provider package
// wraps (parsesql.Tx, parsepgx.Tx), as a backend.Tx: its requests compile
// for d, and each statement that settles an update in it is bounded by
// settle. A resource runs in it through its provider's database
// (backend.DB.Join), as that database is configured.
func NewTx(tx Tx, d filter.Dialect, settle time.Duration) backend.Tx {
	wrapped := transaction{executor: executor{conn: tx, dialect: d, settle: settle}, tx: tx}
	return wrapped
}

// executor runs composed requests on conn, compiled for dialect, each
// update settling within settle (execute.go).
type executor struct {
	conn    Conn
	dialect filter.Dialect
	settle  time.Duration
}

// database is a SQL provider's database as a backend.DB.
type database struct {
	executor
	db DB
}

func (d database) Begin(ctx context.Context) (backend.Tx, error) {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	begun := transaction{executor: executor{conn: tx, dialect: d.dialect, settle: d.settle}, tx: tx}
	return begun, nil
}

// Join runs requests in tx, a transaction of a SQL provider (Begin, NewTx),
// under d's dialect and settle timeout: a transaction the application
// wrapped names no provider's configuration, so the resource's provider
// supplies it.
func (d database) Join(tx backend.Tx) (backend.Executor, error) {
	t, ok := tx.(transaction)
	if !ok {
		return nil, errNotJoinable
	}
	joined := executor{conn: t.tx, dialect: d.dialect, settle: d.settle}
	return joined, nil
}

// errNotJoinable is a transaction to join that no SQL provider began or
// wrapped.
var errNotJoinable = errors.New("not a SQL provider's transaction")

// transaction is a SQL provider's transaction as a backend.Tx.
type transaction struct {
	executor
	tx Tx
}

func (t transaction) Commit(ctx context.Context) error { return t.tx.Commit(ctx) }

func (t transaction) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
