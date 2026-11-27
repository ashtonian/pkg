// Package parsesql is parse's SQLite provider. Open opens a SQLite
// database from Config through modernc.org/sqlite, with the SQL functions
// parse's SQLite plans call registered, and returns the provider on it
// (Database). New opens a parse.Provider over a *sql.DB the application
// opened itself, on any SQLite driver that has the functions (Register,
// RegisterOn, Functions). Every resource built on the provider
// (parse.NewResource) compiles for filter.SQLiteDialect and runs its lists,
// facets, queries and updates on that database; Tx wraps a transaction of
// the application's own for a resource to run in (parse.Resource.In). Rows
// scan through the resource's row contract (parse.RowScanner), which
// decodes the JSON text SQLite stores documents and arrays in and parses
// its timestamp text. New refuses a database whose SQLite is older than
// MinSQLiteVersion, the oldest release the plans run on.
//
// SQLite has no exact decimal type: a NUMERIC column or a json_extract
// result holds a double, so filter.SQLiteDialect compares, orders and
// facets decimals, and bounds an int or float path's number members,
// through deterministic functions instead. It has no geometry either, so
// geo filters on a JSONB point call functions that decide them as PostGIS
// does.
//
//   - parse_decimal(x) is the canonical decimal text (core.DecimalText) of
//     x, or NULL: equality, sets and facet keys compare it.
//   - parse_decimal_key(x) is a BLOB that orders bytewise as the decimal x,
//     or NULL: ranges, ORDER BY and keyset seeks compare it.
//   - parse_decimal_float32(x) is the canonical decimal text of x rounded
//     to float32, or NULL: the facet key of a float32 field's REAL column.
//
// x is a decimal column's value (an INTEGER, a REAL, or TEXT holding a JSON
// number literal) or a document member as SQLite's `->` operator returns
// it: a JSON number literal as written, or a JSON string literal holding a
// decimal in plain notation, as filter.PathValue reads a decimal member.
// An int or float path reads a number member through parse_decimal when
// its literal has an exponent or runs past core.MaxDecimalDigits
// characters, so a plan that filters, sorts or facets such a path needs
// the functions too. Anything else is NULL, so a filter treats it as
// missing.
//
//   - parse_geo_near(point, latKey, lonKey, lat, lon, meters) is 1 when the
//     stored point lies within meters of (lat, lon) by the WGS 84 geodesic
//     distance, as PostGIS measures geography, else 0: $near.
//   - parse_geo_in_box(point, latKey, lonKey, south, north, west, east) is
//     1 when the stored point lies in the box or on its edge, else 0:
//     $within and $intersects a box.
//   - parse_geo_covered_by(point, latKey, lonKey, polygon) is 1 when the
//     stored point lies in the polygon (a WKB blob) or on its boundary, else
//     0: $within and $intersects a polygon.
//
// point is the stored point's JSON text; its latKey and lonKey members are
// read as PostgreSQL reads (doc->>'lat')::float8, a duplicated member by its
// last value. Each function is NULL when the point is: NULL, not an object,
// or a member missing, null or no number. An operand of the wrong type is
// an error on every row, one whose point is NULL included.
//
// SQLite stores what another writer wrote as it was written, where
// PostgreSQL's typed columns hold one text per value, so an update that
// skips unchanged rows compares a column whose text can spell one value
// more than one way by value too:
//
//   - parse_canonical(x, kind) is the form two stored values of one value
//     share — a document as jsonb's text, an array element by element by
//     each element's value, a decimal, UUID, timestamp or value type by its
//     canonical text — NULL for NULL, x itself where it is a scalar that
//     holds no value of kind, and an error where it is a document or array
//     that holds no JSON value. patch.ComposedPatch.Apply compares the same
//     form.
//
// A statement on a connection without the functions fails with SQLite's
// "no such function: parse_decimal" rather than comparing a rounded double.
//
// SQLite has no row lock: an update that reads before it writes (a
// SkipUnchanged plan's Current, a batch's preimages) holds its targets only
// in a write transaction. Open begins every transaction IMMEDIATE and sets
// the busy timeout, so a second writer waits rather than failing with
// SQLITE_BUSY. database/sql begins transactions in the mode the connection
// string names, so a database the application opens for New takes
// _txlock=immediate and _busy_timeout (modernc.org/sqlite and
// mattn/go-sqlite3 both read them), and a *sql.Tx passed to Tx is begun on
// such a connection.
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
	"github.com/ashtonian/pkg/parse/internal/sqlexec"
)

// Name is the provider's name in configuration (parse.Provider.Name).
const Name = "sqlite"

// savepoint names the savepoint an update takes in a caller's transaction.
const savepoint = "parsesql_update"

// Option configures New.
type Option func(*options)

type options struct {
	settle time.Duration
}

// SettleTimeout bounds each statement that settles an update in a
// transaction the resource is scoped to (parse.Resource.In): RELEASE
// SAVEPOINT and ROLLBACK TO SAVEPOINT. It must be positive; the default is
// parse.DefaultSettleTimeout. database/sql's Commit and Rollback take no
// context, so the COMMIT or ROLLBACK of a transaction an update begins
// itself is unbounded; SQLite runs it in process.
func SettleTimeout(d time.Duration) Option {
	return func(o *options) { o.settle = d }
}

// New opens the SQLite provider over db: it reads sqlite_version() once
// under ctx and refuses a release older than MinSQLiteVersion with a
// *VersionError, so no statement meets a library that misreads it. db is
// the application's, and stays open and owned by it; the provider runs
// every resource's statements on it.
func New(ctx context.Context, db *sql.DB, opts ...Option) (*parse.Provider, error) {
	return newProvider(ctx, db, MinSQLiteVersion, opts)
}

// newProvider is New requiring SQLite need.
func newProvider(ctx context.Context, db *sql.DB, need string, opts []Option) (*parse.Provider, error) {
	if db == nil {
		return nil, errors.New("parsesql: nil database")
	}
	o := options{settle: parse.DefaultSettleTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if o.settle <= 0 {
		return nil, fmt.Errorf("parsesql: settle timeout %s: must be positive", o.settle)
	}
	version, err := libraryVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	err = checkVersion(version, need)
	if err != nil {
		return nil, err
	}
	p := parse.NewProvider(sqlexec.Provider(Name, filter.SQLiteDialect{}, database{db: db}, o.settle))
	return p, nil
}

// Tx is tx, a transaction the application began on the provider's
// database, as one a resource runs in (parse.Resource.In), so that the
// application's statements and parse's commit together. An update in it
// runs under a savepoint. Commit or roll back tx itself, or through the
// returned Tx.
func Tx(tx *sql.Tx) *parse.Tx {
	joinable := sqlexec.NewTx(transaction{tx: tx}, filter.SQLiteDialect{}, parse.DefaultSettleTimeout)
	wrapped := parse.NewTx(backend.Transaction{Provider: Name, Tx: joinable})
	return wrapped
}

// database is the provider's *sql.DB.
type database struct {
	db *sql.DB
}

func (d database) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return send(ctx, table, d.db, steps)
}

// Transact begins a transaction on a connection taken under ctx, so the
// wait for one keeps ctx's deadline.
func (d database) Transact(ctx context.Context, table string, _ time.Duration, apply func(sqlexec.Conn) error) error {
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
func (d database) Begin(ctx context.Context) (sqlexec.Tx, error) {
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

func (t transaction) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return send(ctx, table, t.tx, steps)
}

func (t transaction) Transact(ctx context.Context, table string, settle time.Duration, apply func(sqlexec.Conn) error) error {
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
func inTransaction(ctx context.Context, table string, conn *sql.Conn, apply func(sqlexec.Conn) error) error {
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
func underSavepoint(ctx context.Context, table string, settle time.Duration, tx *sql.Tx, apply func(sqlexec.Conn) error) error {
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
// settle context of its own (sqlexec.Settle).
func settleExec(ctx context.Context, settle time.Duration, tx *sql.Tx, statement string) error {
	return sqlexec.Settle(ctx, settle, func(ctx context.Context) error {
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
func send(ctx context.Context, table string, q querier, steps []sqlexec.Step) error {
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
func run(ctx context.Context, q querier, step sqlexec.Step) error {
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
func query(ctx context.Context, q querier, step sqlexec.Step) (err error) {
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
