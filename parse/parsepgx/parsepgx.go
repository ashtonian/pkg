// Package parsepgx is parse's PostgreSQL provider. New opens a
// parse.Provider over a pgx pool, and every resource built on it
// (parse.NewResource) compiles for filter.PostgresDialect and runs its
// lists, facets, queries and updates on that pool; Tx wraps a transaction
// of the application's own for a resource to run in (parse.Resource.In).
// The statements of one step go to the database in one pgx batch, one
// round trip. Rows scan through the resource's row contract
// (parse.RowScanner), and a column PostgreSQL reports as json or jsonb is
// decoded as a stored document whether or not the resource declares its
// type.
package parsepgx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/sqlexec"
)

// Name is the provider's name in configuration (parse.Provider.Name).
const Name = "postgres"

// savepoint names the savepoint an update takes in a caller's transaction.
const savepoint = "parsepgx_update"

// Option configures New.
type Option func(*config)

type config struct {
	dialect filter.PostgresDialect
	settle  time.Duration
}

// Collation is the code-point collation string sorts, keyset seeks and
// string ranges compile under (filter.PostgresDialect.Collation): the
// default, filter.CollationPgCUTF8, needs PostgreSQL 17; filter.CollationC
// serves PostgreSQL 14 to 16. New refuses a collation the server lacks.
func Collation(c filter.PostgresCollation) Option {
	return func(cfg *config) { cfg.dialect.Collation = c }
}

// ContainmentMaxValues bounds the `$in` sets a jsonb path compares by
// containment (filter.PostgresDialect.ContainmentMaxValues): 0, the
// default, is filter.DefaultContainmentMaxValues, and a negative value
// compares no set by containment.
func ContainmentMaxValues(n int) Option {
	return func(cfg *config) { cfg.dialect.ContainmentMaxValues = n }
}

// SettleTimeout bounds each statement that settles an update's
// transaction: COMMIT, ROLLBACK, RELEASE SAVEPOINT and ROLLBACK TO
// SAVEPOINT. It must be positive; the default is
// parse.DefaultSettleTimeout.
func SettleTimeout(d time.Duration) Option {
	return func(cfg *config) { cfg.settle = d }
}

// New opens the PostgreSQL provider over pool: it checks under ctx that the
// server has the provider's collation, so no string comparison meets one it
// lacks. pool is the application's, and stays open and owned by it; the
// provider runs every resource's statements on it.
func New(ctx context.Context, pool *pgxpool.Pool, opts ...Option) (*parse.Provider, error) {
	if pool == nil {
		return nil, errors.New("parsepgx: nil pool")
	}
	cfg := config{settle: parse.DefaultSettleTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.settle <= 0 {
		return nil, fmt.Errorf("parsepgx: settle timeout %s: must be positive", cfg.settle)
	}
	err := checkCollation(ctx, pool, cfg.dialect.Collation)
	if err != nil {
		return nil, err
	}
	p := parse.NewProvider(sqlexec.Provider(Name, cfg.dialect, database{pool: pool}, cfg.settle))
	return p, nil
}

// checkCollation refuses a collation the database cannot compare its
// strings under, by comparing two strings under it as the plans' SQL does:
// pg_c_utf8 is missing before PostgreSQL 17 and in a database not encoded
// UTF8 (undefined_object, 42704).
func checkCollation(ctx context.Context, pool *pgxpool.Pool, c filter.PostgresCollation) error {
	switch c {
	case filter.CollationPgCUTF8, filter.CollationC:
	default:
		return fmt.Errorf("parsepgx: collation %d: no such collation", c)
	}
	const undefinedObject = "42704"
	var ok bool
	err := pool.QueryRow(ctx, "SELECT 'a' COLLATE "+c.String()+" < 'b'").Scan(&ok)
	pgErr, answered := errors.AsType[*pgconn.PgError](err)
	if answered && pgErr.Code == undefinedObject {
		return fmt.Errorf("parsepgx: collation %s: %s; on PostgreSQL 14 to 16 open the provider with Collation(filter.CollationC)", c, pgErr.Message)
	}
	if err != nil {
		return fmt.Errorf("parsepgx: checking collation %s: %w", c, err)
	}
	return nil
}

// Tx is tx, a transaction the application began on the provider's pool, as
// one a resource runs in (parse.Resource.In), so that the application's
// statements and parse's commit together. An update in it runs under a
// savepoint. Commit or roll back tx itself, or through the returned Tx.
func Tx(tx pgx.Tx) *parse.Tx {
	joinable := sqlexec.NewTx(transaction{tx: tx}, filter.PostgresDialect{}, parse.DefaultSettleTimeout)
	wrapped := parse.NewTx(backend.Transaction{Provider: Name, Tx: joinable})
	return wrapped
}

// database is the provider's pool.
type database struct {
	pool *pgxpool.Pool
}

func (d database) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return send(ctx, table, d.pool, steps)
}

// Transact runs apply in a transaction of its own, settled only once apply
// has returned, every row read and scanned: an error means nothing apply
// wrote is kept, but for parse.ErrCommitUnknown (commitFailed). Once apply
// succeeds the update is finished whatever ctx does since, so a reported
// error never hides an applied update; each settling statement therefore
// runs under its own settle context (sqlexec.Settle), as pgx sends nothing
// on a done context. A panic in apply settles as an error does on its way
// out, so no transaction stays open on the connection.
func (d database) Transact(ctx context.Context, table string, settle time.Duration, apply func(sqlexec.Conn) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return parse.StatementFailed(table, "begin", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller, so the rollback's error has
			// nowhere to go; a rollback that fails closes the connection,
			// and the server rolls back with it.
			_ = sqlexec.Settle(ctx, settle, tx.Rollback)
		}
	}()
	err = apply(transaction{tx: tx})
	returned = true
	if err != nil {
		// A transaction pgx already closed (its connection lost) kept
		// nothing to roll back.
		rollback := sqlexec.Settle(ctx, settle, tx.Rollback)
		if !errors.Is(rollback, pgx.ErrTxClosed) {
			err = errors.Join(err, parse.StatementFailed(table, "rollback", rollback))
		}
		return err
	}
	return commitFailed(table, sqlexec.Settle(ctx, settle, tx.Commit))
}

// Begin begins a transaction that lasts until Commit or Rollback: pgx reads
// ctx for the BEGIN alone.
func (d database) Begin(ctx context.Context) (sqlexec.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, parse.StatementFailed("", "begin", err)
	}
	begun := transaction{tx: tx}
	return begun, nil
}

// transaction is a transaction on the provider's pool.
type transaction struct {
	tx pgx.Tx
}

func (t transaction) Send(ctx context.Context, table string, steps ...sqlexec.Step) error {
	return send(ctx, table, t.tx, steps)
}

// Transact runs apply in the transaction after a savepoint, released once
// apply succeeds and rolled back to otherwise, or as a panic in apply
// unwinds. The savepoint is taken by hand rather than through pgx's nested
// Begin, whose failed release can no longer be rolled back.
func (t transaction) Transact(ctx context.Context, table string, settle time.Duration, apply func(sqlexec.Conn) error) error {
	_, err := t.tx.Exec(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed(table, "savepoint", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the undo's error has nowhere
			// to go.
			_ = t.undoSavepoint(ctx, table, settle)
		}
	}()
	err = apply(t)
	returned = true
	if err != nil {
		return errors.Join(err, t.undoSavepoint(ctx, table, settle))
	}
	err = t.settleExec(ctx, settle, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed(table, "release savepoint", err), t.undoSavepoint(ctx, table, settle))
	}
	return nil
}

func (t transaction) Commit(ctx context.Context) error {
	return commitFailed("", t.tx.Commit(ctx))
}

func (t transaction) Rollback(ctx context.Context) error {
	err := t.tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return parse.StatementFailed("", "rollback", err)
}

// undoSavepoint rolls the transaction back to the update's savepoint and
// releases it, so the transaction keeps nothing the update wrote and stays
// usable, a failed statement's abort included.
func (t transaction) undoSavepoint(ctx context.Context, table string, settle time.Duration) error {
	rollback := t.settleExec(ctx, settle, "ROLLBACK TO SAVEPOINT "+savepoint)
	release := t.settleExec(ctx, settle, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed(table, "rollback to savepoint", errors.Join(rollback, release))
}

// settleExec runs statement, one that settles the update, on the
// transaction under a settle context of its own.
func (t transaction) settleExec(ctx context.Context, settle time.Duration, statement string) error {
	return sqlexec.Settle(ctx, settle, func(ctx context.Context) error {
		_, err := t.tx.Exec(ctx, statement)
		return err
	})
}

// commitFailed is a failed COMMIT's error, saying whether it may have taken.
// PostgreSQL answers a COMMIT it refuses with an ERROR (a deferred
// constraint, a serialization failure) and rolls the transaction back, as it
// does for the COMMIT of an aborted transaction (pgx.ErrTxCommitRollback);
// pgx reports a COMMIT it never sent as safe to retry. Any other failure —
// the connection lost or the settle timeout run out while the COMMIT was in
// flight, or a FATAL that ends the session — leaves the outcome unknown.
// table names the update whose transaction it commits ("" for a Tx).
func commitFailed(table string, err error) error {
	if err == nil {
		return nil
	}
	pgErr, answered := errors.AsType[*pgconn.PgError](err)
	refused := answered && pgErr.SeverityUnlocalized == "ERROR"
	if refused || errors.Is(err, pgx.ErrTxCommitRollback) || pgconn.SafeToRetry(err) {
		return parse.StatementFailed(table, "commit", err)
	}
	return parse.StatementFailed(table, "commit", fmt.Errorf("%w: %w", parse.ErrCommitUnknown, err))
}

// batcher sends statement batches: *pgxpool.Pool and pgx.Tx.
type batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// send sends steps in one batch and reads every result through its step,
// stopping at the first that fails, its failure named as the step. A step
// that panics leaves the rest of the batch unread and the connection busy
// (and a pool's connection unreleased); reading the rest as the panic
// unwinds frees it for the settle that follows.
func send(ctx context.Context, table string, db batcher, steps []sqlexec.Step) error {
	b := &pgx.Batch{}
	// done counts the steps read through, so a failure the batch reports on
	// Close, such as an Exec's, names the step it stopped at.
	done := 0
	for _, step := range steps {
		queued := b.Queue(step.SQL, step.Args...)
		if step.Read == nil {
			queued.Exec(func(tag pgconn.CommandTag) error {
				if step.Changed != nil {
					step.Changed(tag.RowsAffected())
				}
				done++
				return nil
			})
			continue
		}
		queued.Query(func(rs pgx.Rows) error {
			err := step.Read(rows{rs})
			rs.Close()
			err = errors.Join(err, rs.Err())
			if err != nil {
				return parse.StatementFailed(table, step.Name, err)
			}
			done++
			return nil
		})
	}
	results := db.SendBatch(ctx, b)
	closed := false
	defer func() {
		if !closed {
			_ = results.Close()
		}
	}()
	err := results.Close()
	closed = true
	if err == nil {
		return nil
	}
	_, named := errors.AsType[*parse.StatementError](err)
	if named || done >= len(steps) {
		return err
	}
	return parse.StatementFailed(table, steps[done].Name, err)
}

// rows are a statement's rows, which report the columns PostgreSQL types
// json or jsonb once the first row is read.
type rows struct{ pgx.Rows }

func (r rows) Documents() []string {
	var names []string
	for _, f := range r.FieldDescriptions() {
		if f.DataTypeOID == pgtype.JSONOID || f.DataTypeOID == pgtype.JSONBOID {
			names = append(names, f.Name)
		}
	}
	return names
}
