//go:build unit || integration || concurrency

package dbtest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

// statementHookKey is the context key OnStatement keeps its hook under.
type statementHookKey struct{}

// OnStatement returns ctx carrying hook. A database SQLiteHooked opened
// calls hook with each statement it is about to run under that context, or
// under one derived from it (context.WithoutCancel keeps it), then refuses
// the statement with the context's error if the context has ended by then,
// as database/sql refuses a statement whose context ended before the call.
// A test ends or stalls an executor's context at a statement of its choice
// this way, inside a transaction the executor holds as it would any other.
func OnStatement(ctx context.Context, hook func(ctx context.Context, query string)) context.Context {
	return context.WithValue(ctx, statementHookKey{}, hook)
}

// SQLiteHooked is SQLite, opened as SQLite opens it, through a driver that
// calls the hook OnStatement put in a statement's context. Statements run
// through ExecContext and QueryContext reach the hook; prepared statements
// do not.
func SQLiteHooked(t testing.TB) *sql.DB {
	t.Helper()
	// The handle only names the registered driver; it never connects.
	registered := openSQLite(t)
	connector := hookedConnector{driver: registered.Driver(), dsn: sqliteDSN}
	err := registered.Close()
	if err != nil {
		t.Fatalf("dbtest: close sqlite handle: %v", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// hookedConnector opens connections of driver, the driver parse's SQLite
// functions are registered on, each wrapped in a hookedConn.
type hookedConnector struct {
	driver driver.Driver
	dsn    string
}

func (c hookedConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	conn := hookedConn{Conn: inner}
	return conn, nil
}

func (c hookedConnector) Driver() driver.Driver { return c.driver }

// hookedConn is a driver connection that reports each statement to its
// context's hook before running it, and otherwise is the connection it
// wraps: every optional interface database/sql looks for is forwarded.
type hookedConn struct{ driver.Conn }

// errNoBeginTx is a wrapped connection whose driver cannot begin a
// transaction with options.
var errNoBeginTx = errors.New("dbtest: the driver's connection has no BeginTx")

func (c hookedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	err := Announce(ctx, query)
	if err != nil {
		return nil, err
	}
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c hookedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	err := Announce(ctx, query)
	if err != nil {
		return nil, err
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c hookedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	return preparer.PrepareContext(ctx, query)
}

func (c hookedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errNoBeginTx
	}
	return beginner.BeginTx(ctx, opts)
}

func (c hookedConn) Ping(ctx context.Context) error {
	pinger, ok := c.Conn.(driver.Pinger)
	if !ok {
		return nil
	}
	return pinger.Ping(ctx)
}

func (c hookedConn) ResetSession(ctx context.Context) error {
	resetter, ok := c.Conn.(driver.SessionResetter)
	if !ok {
		return nil
	}
	return resetter.ResetSession(ctx)
}

func (c hookedConn) IsValid() bool {
	validator, ok := c.Conn.(driver.Validator)
	return !ok || validator.IsValid()
}

// Announce calls the hook OnStatement put in ctx, if it has one, with query,
// and returns ctx's error if the hook left ctx ended: a statement about to
// be sent reports itself so. SQLiteHooked's driver announces every
// statement; a test's own wrapper of another driver's connection does the
// same.
func Announce(ctx context.Context, query string) error {
	hook, ok := ctx.Value(statementHookKey{}).(func(context.Context, string))
	if !ok {
		return nil
	}
	hook(ctx, query)
	return ctx.Err()
}
