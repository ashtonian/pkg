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
//     share — a document as jsonb's text compares it, each number exactly;
//     an array element by element by each element's value; a decimal,
//     UUID, timestamp or value type by its canonical text — NULL for NULL,
//     x itself where it is a scalar that holds no value of kind, and an
//     error where it is a document or array that holds no JSON value.
//     patch.ComposedPatch.Apply compares the same form.
//
// A statement on a connection without the functions fails with SQLite's
// "no such function: parse_decimal" rather than comparing a rounded double.
//
// SQLite has no row lock: an update that reads before it writes (a
// SkipUnchanged plan's Current, a batch's preimages) holds its targets only
// while it holds the write lock from its read to its write. The provider
// begins the transactions it settles itself, an update's own and
// Provider.Begin's, with BEGIN IMMEDIATE, which takes the lock as the
// transaction begins, waiting for it up to the connection's busy timeout,
// whatever mode the connection string names. database/sql begins an
// application's *sql.Tx in that mode, so one passed to Tx is begun on a
// connection with _txlock=immediate, as Open's are. A database the
// application opens for New also takes _busy_timeout, which
// modernc.org/sqlite leaves at 0, failing at once. The README's SQLite
// configuration covers the journal mode, the busy timeout, the transaction
// mode and backups.
package parsesql

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
// parse.DefaultSettleTimeout. The COMMIT or ROLLBACK of a transaction the
// provider begins itself takes no deadline, since an interrupted COMMIT
// could have taken effect; SQLite runs it in process, and its wait for a
// lock is the busy timeout's.
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
// returned Tx. database/sql begins tx in the mode the connection string
// names: an update that reads before it writes holds its targets only if
// that mode is IMMEDIATE (_txlock=immediate), as it is on Open's database.
func Tx(tx *sql.Tx) *parse.Tx {
	joinable := sqlexec.NewTx(transaction{openTx: openTx{q: tx}, tx: tx}, filter.SQLiteDialect{}, parse.DefaultSettleTimeout)
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

// Transact begins a write transaction (beginWrite) on a connection taken
// under ctx, so the wait for one keeps ctx's deadline.
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

// Begin begins a write transaction (beginWrite) on a connection taken
// under ctx, so the wait for one keeps ctx's deadline. The transaction is
// not bound to ctx's cancellation: it holds the connection until Commit or
// Rollback.
func (d database) Begin(ctx context.Context) (sqlexec.Tx, error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, parse.StatementFailed("", "begin", err)
	}
	err = beginWrite(ctx, conn)
	if err != nil {
		// A BEGIN that failed began nothing, so conn goes back as it came.
		return nil, errors.Join(parse.StatementFailed("", "begin", err), conn.Close())
	}
	begun := &ownTransaction{conn: conn}
	return begun, nil
}

// ownTransaction is a transaction the provider began on a connection of
// its database (database.Begin), which it holds until settled and then
// returns to the pool.
type ownTransaction struct {
	// conn is nil once the transaction is settled.
	conn *sql.Conn
}

// statements is what the transaction's statements run on: its connection,
// or, once it is settled, a querier that refuses them.
func (t *ownTransaction) statements() querier {
	if t.conn == nil {
		return settled{}
	}
	return t.conn
}

func (t *ownTransaction) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return openTx{q: t.statements()}.Send(ctx, table, steps...)
}

func (t *ownTransaction) Transact(ctx context.Context, table string, settle time.Duration, apply func(sqlexec.Conn) error) error {
	return openTx{q: t.statements()}.Transact(ctx, table, settle, apply)
}

func (t *ownTransaction) Commit(ctx context.Context) error {
	if t.conn == nil {
		return parse.StatementFailed("", "commit", sql.ErrTxDone)
	}
	conn := t.conn
	t.conn = nil
	// Close fails only on a second close: commit discarded conn.
	defer func() { _ = conn.Close() }()
	return parse.StatementFailed("", "commit", commit(ctx, conn))
}

func (t *ownTransaction) Rollback(ctx context.Context) error {
	if t.conn == nil {
		return nil
	}
	conn := t.conn
	t.conn = nil
	// Close fails only on a second close: rollback discarded conn.
	defer func() { _ = conn.Close() }()
	return parse.StatementFailed("", "rollback", rollback(ctx, conn))
}

// settled is the querier of a settled transaction: it refuses every
// statement with sql.ErrTxDone, as a settled *sql.Tx does.
type settled struct{}

func (settled) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, sql.ErrTxDone
}

func (settled) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, sql.ErrTxDone
}

// transaction is the application's transaction on the provider's database
// (Tx), settled through database/sql.
type transaction struct {
	openTx
	tx *sql.Tx
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

// openTx sends statements in a transaction already open on q: a
// connection the provider began one on, or the application's *sql.Tx.
// Transact takes a savepoint of it.
type openTx struct {
	q querier
}

func (o openTx) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return send(ctx, table, o.q, steps)
}

func (o openTx) Transact(ctx context.Context, table string, settle time.Duration, apply func(sqlexec.Conn) error) error {
	return underSavepoint(ctx, table, settle, o.q, apply)
}

// inTransaction runs apply in a write transaction begun on conn
// (beginWrite), committed once apply succeeds and rolled back otherwise, or
// as a panic in apply unwinds. The statements in it take ctx; the ones that
// begin and settle it take ctx's values without its cancellation. SQLite's
// commit never leaves its outcome unknown, so parse.ErrCommitUnknown is not
// one of its errors.
func inTransaction(ctx context.Context, table string, conn *sql.Conn, apply func(sqlexec.Conn) error) error {
	err := beginWrite(ctx, conn)
	if err != nil {
		return parse.StatementFailed(table, "begin", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the rollback's error has
			// nowhere to go.
			_ = rollback(ctx, conn)
		}
	}()
	err = apply(openTx{q: conn})
	returned = true
	if err != nil {
		return errors.Join(err, parse.StatementFailed(table, "rollback", rollback(ctx, conn)))
	}
	return parse.StatementFailed(table, "commit", commit(ctx, conn))
}

// beginWrite begins a write transaction on conn. BEGIN IMMEDIATE takes
// SQLite's write lock as the transaction begins, waiting for it up to the
// busy timeout, so no other connection writes between an update's read and
// its write. A deferred transaction, the connection string's default,
// holds only a read snapshot until its first write, which fails with
// SQLITE_BUSY whatever the busy timeout once another connection has
// written since the read. The statement runs without ctx's cancellation:
// an interrupt could report a BEGIN that took effect as failed, and the
// connection would go back to the pool inside a transaction.
func beginWrite(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(context.WithoutCancel(ctx), "BEGIN IMMEDIATE")
	return err
}

// commit commits the transaction open on conn, without ctx's cancellation,
// which could report a COMMIT that took effect as failed. A COMMIT that
// fails can leave the transaction open (SQLITE_BUSY while a reader holds
// the database in rollback-journal mode, a deferred foreign key it
// violates); it is rolled back then, so conn leaves no transaction open.
func commit(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT")
	if err != nil {
		// The COMMIT's failure is the outcome; nothing is kept either way.
		_ = rollback(ctx, conn)
	}
	return err
}

// rollback rolls back the transaction open on conn, without ctx's
// cancellation, so conn goes back to the pool outside any transaction. A
// statement that failed can have rolled the whole transaction back already
// (a trigger's RAISE(ROLLBACK), SQLITE_FULL, an interrupt), and ROLLBACK
// then fails with nothing kept. SQLite refuses BEGIN inside a transaction,
// so a BEGIN that succeeds after a failed ROLLBACK shows that none is open,
// and the transaction it began ends at once. A connection still in a
// transaction is discarded, which ends it, rather than returned to the pool
// holding the write lock.
func rollback(ctx context.Context, conn *sql.Conn) error {
	settle := context.WithoutCancel(ctx)
	_, err := conn.ExecContext(settle, "ROLLBACK")
	if err == nil {
		return nil
	}
	_, probe := conn.ExecContext(settle, "BEGIN")
	if probe == nil {
		_, probe = conn.ExecContext(settle, "ROLLBACK")
	}
	if probe == nil {
		return nil
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return err
}

// underSavepoint runs apply in the transaction open on q after a
// savepoint, released once apply succeeds and rolled back to otherwise, or
// as a panic in apply unwinds, so the transaction keeps nothing apply wrote
// when it fails and stays usable.
func underSavepoint(ctx context.Context, table string, settle time.Duration, q querier, apply func(sqlexec.Conn) error) error {
	_, err := q.ExecContext(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed(table, "savepoint", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the undo's error has nowhere
			// to go.
			_ = undoSavepoint(ctx, table, settle, q)
		}
	}()
	err = apply(openTx{q: q})
	returned = true
	if err != nil {
		return errors.Join(err, undoSavepoint(ctx, table, settle, q))
	}
	err = settleExec(ctx, settle, q, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed(table, "release savepoint", err), undoSavepoint(ctx, table, settle, q))
	}
	return nil
}

// undoSavepoint rolls the transaction open on q back to the update's
// savepoint and releases it, so the transaction keeps nothing the update
// wrote and stays usable.
func undoSavepoint(ctx context.Context, table string, settle time.Duration, q querier) error {
	rollback := settleExec(ctx, settle, q, "ROLLBACK TO SAVEPOINT "+savepoint)
	release := settleExec(ctx, settle, q, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed(table, "rollback to savepoint", errors.Join(rollback, release))
}

// settleExec runs statement, one that settles the update, on q under a
// settle context of its own (sqlexec.Settle).
func settleExec(ctx context.Context, settle time.Duration, q querier, statement string) error {
	return sqlexec.Settle(ctx, settle, func(ctx context.Context) error {
		_, err := q.ExecContext(ctx, statement)
		return err
	})
}

// querier runs statements: *sql.DB, *sql.Conn and *sql.Tx.
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
