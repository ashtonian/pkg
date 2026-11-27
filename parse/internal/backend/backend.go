// Package backend is the contract between parse's root package, which runs
// a resource's statements, and the provider packages (parsesql, parsepgx),
// which send them to a database: how statements are sent, how their rows
// are read, and how a transaction is held. The root package composes and
// compiles every statement, scans every row into the resource's type and
// holds the executor sequence once for every provider; a provider package
// supplies the driver underneath. The package is internal so that only
// parse's own packages open a provider.
package backend

import (
	"context"
	"time"

	"github.com/ashtonian/pkg/parse/filter"
)

// Provider is what a provider package hands the root package
// (parse.NewProvider): the provider's name in configuration, the dialect
// its statements compile for, its database, and the bound on each
// statement that settles an update's transaction (Settle), positive.
type Provider struct {
	Name          string
	Dialect       filter.Dialect
	DB            DB
	SettleTimeout time.Duration
}

// Transaction is a transaction a provider package wraps for the root
// package (parse.NewTx): the name of the provider it belongs to and the
// transaction.
type Transaction struct {
	Provider string
	Tx       Tx
}

// Rows are one statement's rows as a provider reads them.
type Rows interface {
	filter.Rows
	// Documents names the result's columns the database reports as JSON
	// documents (PostgreSQL's json and jsonb), which a row scan decodes as
	// documents whatever the resource declares. Nil from a database that
	// reports no column types (SQLite).
	Documents() []string
}

// Step is one statement a provider sends.
type Step struct {
	// Name names the statement in a failure: page, count, facets, include
	// <field>, current, conflict, lock, owner, relations or reread. The
	// provider reports a failed step as parse.StatementFailed(table, Name,
	// err), table the one Send was given.
	Name string
	SQL  string
	Args []any
	// Read reads the statement's rows; the provider closes them after. A
	// statement without Read is run for the rows it changes (Changed).
	Read func(Rows) error
	// Changed, when set, is given the number of rows a statement without
	// Read changed.
	Changed func(int64)
}

// Conn sends statements: a provider's database, or a transaction on it.
// table is the resource's whose statements they are, which every failure
// the provider reports names (parse.StatementFailed).
type Conn interface {
	// Send runs steps in order and stops at the first that fails. A
	// provider may send them in one round trip (a pgx batch); each runs in
	// its own snapshot unless the Conn is a transaction that holds one.
	Send(ctx context.Context, table string, steps ...Step) error
	// Transact runs apply in one transaction, settled only once apply has
	// returned. On a database it begins one of its own and commits or
	// rolls it back; in a transaction it takes a savepoint, released or
	// rolled back to, so the caller's transaction keeps nothing apply wrote
	// when it fails and stays usable. Every settling statement runs under
	// Settle with the settle timeout. An error means nothing apply wrote is
	// kept, unless errors.Is(err, parse.ErrCommitUnknown). apply's error is
	// returned as it is, joined with the failure of the statements that
	// undo its writes, if they fail too. A panic in apply settles as an
	// error does on its way out.
	Transact(ctx context.Context, table string, settle time.Duration, apply func(Conn) error) error
}

// DB is a provider's database.
type DB interface {
	Conn
	// Begin begins a transaction the caller settles (parse.Provider.Begin).
	// The transaction is not bound to ctx's cancellation: it lasts until
	// Commit or Rollback.
	Begin(ctx context.Context) (Tx, error)
}

// Tx is a transaction a caller holds and settles. Rollback of a
// transaction already settled does nothing and returns nil.
type Tx interface {
	Conn
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Settle runs call, one statement that settles a transaction (a COMMIT, a
// ROLLBACK or a savepoint's release or rollback), under ctx's values
// without its cancellation, bounded by timeout. The request may be gone by
// the time an update settles, and a settle its cancellation cut short would
// leave the update half reported; the timeout holds a stalled database to
// that long. Each settling statement takes its own, so one that ran out of
// time leaves the next its full bound.
func Settle(ctx context.Context, timeout time.Duration, call func(context.Context) error) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return call(settle)
}
