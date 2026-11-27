// Package parsesql is parse's SQLite provider. New opens a parse.Provider
// over a *sql.DB on any SQLite driver, and every resource built on it
// (parse.NewResource) compiles for filter.SQLiteDialect and runs its lists,
// facets, queries and updates on that database; Tx wraps a transaction of
// the application's own for a resource to run in (parse.Resource.In). Rows
// scan through the resource's row contract (parse.RowScanner), which
// decodes the JSON text SQLite stores documents and arrays in and parses
// its timestamp text. The SQL functions SQLite plans call are registered by
// parsesqlite. New refuses a database whose SQLite is older than
// MinSQLiteVersion, the oldest release the plans run on.
//
// SQLite has no row lock: an update that reads before it writes (a
// SkipUnchanged plan's Current, a batch's preimages) holds its targets only
// in a write transaction. database/sql begins transactions in the mode the
// connection string names, so open the database with _txlock=immediate, and
// with _busy_timeout so a second writer waits rather than failing with
// SQLITE_BUSY (modernc.org/sqlite and mattn/go-sqlite3 both read them); a
// *sql.Tx passed to Tx is begun on such a connection.
package parsesql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// Name is the provider's name in configuration (parse.Provider.Name).
const Name = "sqlite"

// savepoint names the savepoint an update takes in a caller's transaction.
const savepoint = "parsesql_update"

// Option configures New.
type Option func(*config)

type config struct {
	settle time.Duration
}

// SettleTimeout bounds each statement that settles an update in a
// transaction the resource is scoped to (parse.Resource.In): RELEASE
// SAVEPOINT and ROLLBACK TO SAVEPOINT. It must be positive; the default is
// parse.DefaultSettleTimeout. database/sql's Commit and Rollback take no
// context, so the COMMIT or ROLLBACK of a transaction an update begins
// itself is unbounded; SQLite runs it in process.
func SettleTimeout(d time.Duration) Option {
	return func(c *config) { c.settle = d }
}

// New opens the SQLite provider over db: it reads sqlite_version() once
// under ctx and refuses a release older than MinSQLiteVersion with a
// *VersionError, so no statement meets a library that misreads it. db is
// the application's, and stays open and owned by it; the provider runs
// every resource's statements on it.
func New(ctx context.Context, db *sql.DB, opts ...Option) (*parse.Provider, error) {
	return open(ctx, db, MinSQLiteVersion, opts)
}

// open is New requiring SQLite need.
func open(ctx context.Context, db *sql.DB, need string, opts []Option) (*parse.Provider, error) {
	if db == nil {
		return nil, errors.New("parsesql: nil database")
	}
	cfg := config{settle: parse.DefaultSettleTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.settle <= 0 {
		return nil, fmt.Errorf("parsesql: settle timeout %s: must be positive", cfg.settle)
	}
	version, err := libraryVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	err = checkVersion(version, need)
	if err != nil {
		return nil, err
	}
	p := parse.NewProvider(backend.Provider{Name: Name, Dialect: filter.SQLiteDialect{}, DB: database{db: db}, SettleTimeout: cfg.settle})
	return p, nil
}

// Tx is tx, a transaction the application began on the provider's
// database, as one a resource runs in (parse.Resource.In), so that the
// application's statements and parse's commit together. An update in it
// runs under a savepoint. Commit or roll back tx itself, or through the
// returned Tx.
func Tx(tx *sql.Tx) *parse.Tx {
	wrapped := parse.NewTx(backend.Transaction{Provider: Name, Tx: transaction{tx: tx}})
	return wrapped
}

// database is the provider's *sql.DB.
type database struct {
	db *sql.DB
}

func (d database) Send(ctx context.Context, table string, steps ...backend.Step) error {
	return send(ctx, table, d.db, steps)
}

// Transact begins a transaction on a connection taken under ctx, so the
// wait for one keeps ctx's deadline.
func (d database) Transact(ctx context.Context, table string, _ time.Duration, apply func(backend.Conn) error) error {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return parse.StatementFailed(table, "begin", err)
	}
	// Close fails only on a second close. It runs after inTransaction's
	// own deferred settle, which ends the transaction holding conn.
	defer func() { _ = conn.Close() }()
	return inTransaction(ctx, table, conn, apply)
}

// Begin begins the transaction without ctx's cancellation, which
// database/sql would answer by rolling back what is already done; it lasts
// until Commit or Rollback.
func (d database) Begin(ctx context.Context) (backend.Tx, error) {
	tx, err := d.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return nil, parse.StatementFailed("", "begin", err)
	}
	begun := transaction{tx: tx}
	return begun, nil
}

// transaction is a transaction on the provider's database.
type transaction struct {
	tx *sql.Tx
}

func (t transaction) Send(ctx context.Context, table string, steps ...backend.Step) error {
	return send(ctx, table, t.tx, steps)
}

func (t transaction) Transact(ctx context.Context, table string, settle time.Duration, apply func(backend.Conn) error) error {
	return underSavepoint(ctx, table, settle, t.tx, apply)
}

func (t transaction) Commit(context.Context) error {
	return parse.StatementFailed("", "commit", t.tx.Commit())
}

func (t transaction) Rollback(context.Context) error {
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return parse.StatementFailed("", "rollback", err)
}

// inTransaction runs apply in a transaction conn begins, committed once
// apply succeeds and rolled back otherwise, or as a panic in apply unwinds.
// It is begun without ctx's cancellation, which database/sql would answer
// by rolling back what is already done; the statements in it take ctx.
// SQLite's commit never leaves its outcome unknown, so parse.ErrCommitUnknown
// is not one of its errors.
func inTransaction(ctx context.Context, table string, conn *sql.Conn, apply func(backend.Conn) error) error {
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return parse.StatementFailed(table, "begin", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the rollback's error has
			// nowhere to go.
			_ = tx.Rollback()
		}
	}()
	err = apply(transaction{tx: tx})
	returned = true
	if err != nil {
		// A transaction database/sql already ended (a bad connection) kept
		// nothing to roll back.
		rollback := tx.Rollback()
		if !errors.Is(rollback, sql.ErrTxDone) {
			err = errors.Join(err, parse.StatementFailed(table, "rollback", rollback))
		}
		return err
	}
	return parse.StatementFailed(table, "commit", tx.Commit())
}

// underSavepoint runs apply in the caller's transaction tx after a
// savepoint, released once apply succeeds and rolled back to otherwise, or
// as a panic in apply unwinds, so tx keeps nothing apply wrote when it
// fails and stays usable.
func underSavepoint(ctx context.Context, table string, settle time.Duration, tx *sql.Tx, apply func(backend.Conn) error) error {
	_, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed(table, "savepoint", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the undo's error has nowhere
			// to go.
			_ = undoSavepoint(ctx, table, settle, tx)
		}
	}()
	err = apply(transaction{tx: tx})
	returned = true
	if err != nil {
		return errors.Join(err, undoSavepoint(ctx, table, settle, tx))
	}
	err = settleExec(ctx, settle, tx, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed(table, "release savepoint", err), undoSavepoint(ctx, table, settle, tx))
	}
	return nil
}

// undoSavepoint rolls the caller's transaction back to the update's
// savepoint and releases it, so the transaction keeps nothing the update
// wrote and stays usable.
func undoSavepoint(ctx context.Context, table string, settle time.Duration, tx *sql.Tx) error {
	rollback := settleExec(ctx, settle, tx, "ROLLBACK TO SAVEPOINT "+savepoint)
	release := settleExec(ctx, settle, tx, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed(table, "rollback to savepoint", errors.Join(rollback, release))
}

// settleExec runs statement, one that settles the update, on tx under a
// settle context of its own (backend.Settle).
func settleExec(ctx context.Context, settle time.Duration, tx *sql.Tx, statement string) error {
	return backend.Settle(ctx, settle, func(ctx context.Context) error {
		_, err := tx.ExecContext(ctx, statement)
		return err
	})
}

// querier runs statements: *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// send runs steps one after another on q, stopping at the first that
// fails, its failure named as the step.
func send(ctx context.Context, table string, q querier, steps []backend.Step) error {
	for _, step := range steps {
		err := run(ctx, q, step)
		if err != nil {
			return parse.StatementFailed(table, step.Name, err)
		}
	}
	return nil
}

// run runs one step: a query whose rows its Read reads, or a statement
// whose changed rows its Changed is given.
func run(ctx context.Context, q querier, step backend.Step) error {
	if step.Read != nil {
		return query(ctx, q, step)
	}
	res, err := q.ExecContext(ctx, step.SQL, step.Args...)
	if err != nil || step.Changed == nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	step.Changed(n)
	return nil
}

// query runs step's query and hands its rows to step.Read, closing them
// after, as a panic in Read unwinds too.
func query(ctx context.Context, q querier, step backend.Step) (err error) {
	rs, err := q.QueryContext(ctx, step.SQL, step.Args...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rs.Close()) }()
	err = step.Read(rows{rs})
	if err != nil {
		return err
	}
	return rs.Err()
}

// rows are a statement's rows; SQLite reports no column types.
type rows struct{ *sql.Rows }

func (rows) Documents() []string { return nil }
