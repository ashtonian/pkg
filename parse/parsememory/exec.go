package parsememory

import (
	"context"
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// executor runs composed requests (backend.Executor) on db's tables: in
// txn when it is set, its reads seeing its writes and its updates under a
// savepoint of it; otherwise each read on the published snapshot and each
// update in a write of its own.
type executor struct {
	db  *Database
	txn *Txn
}

// hold runs the executor's request alone in its transaction, after the
// operation in flight there and before the next (Txn.mu), until the
// returned release; outside a transaction it holds nothing.
func (e executor) hold() (release func()) {
	if e.txn == nil {
		return func() {}
	}
	e.txn.mu.Lock()
	return e.txn.mu.Unlock
}

// view is what the executor's reads read: the transaction's write, or the
// snapshot published when the read begins. A read in a transaction holds
// it (hold) while it reads.
func (e executor) view() (view, error) {
	err := e.db.open()
	if err != nil {
		return nil, err
	}
	if e.txn == nil {
		return e.db.store.read(), nil
	}
	err = e.txn.live()
	if err != nil {
		return nil, err
	}
	return e.txn.w, nil
}

// write runs apply, one update of table's resource, in a write: under a
// savepoint of the transaction, restored when apply fails, or in a write
// of its own, which waits for the writer under ctx and is published only
// when apply succeeds. ErrRowLimit ends the transaction it happens in. A
// panic in apply, such as a sink's scanner panicking, settles the write on
// its way out as an error does: the savepoint is restored, or the write
// aborted and the writer released. A ctx already done runs nothing, and
// fails the part the SQL providers would run first: the savepoint in a
// transaction, the begin otherwise.
func (e executor) write(ctx context.Context, table string, apply func(*write) error) error {
	err := e.db.open()
	if err != nil {
		return err
	}
	if e.txn != nil {
		err = ctx.Err()
		if err != nil {
			return parse.StatementFailed(table, "savepoint", err)
		}
		defer e.hold()()
		return e.underSavepoint(apply)
	}
	w, err := e.db.store.begin(ctx)
	if err != nil {
		return parse.StatementFailed(table, "begin", err)
	}
	// After a commit, abort is a no-op.
	defer w.abort()
	err = apply(w)
	if err != nil {
		return err
	}
	return parse.StatementFailed(table, "commit", w.commit())
}

// underSavepoint runs apply in the executor's transaction, which it holds
// (hold), under a savepoint, restored unless apply returns nil.
func (e executor) underSavepoint(apply func(*write) error) error {
	err := e.txn.live()
	if err != nil {
		return err
	}
	w := e.txn.w
	saved := w.savepoint()
	returned := false
	defer func() {
		if !returned {
			w.restore(saved)
		}
	}()
	err = apply(w)
	returned = true
	switch {
	case errors.Is(err, ErrRowLimit):
		w.abort()
		e.txn.settled()
	case err != nil:
		w.restore(saved)
	}
	return err
}

func (e executor) Query(_ context.Context, req backend.QueryRequest, _ backend.QuerySink) error {
	unsupported := &parse.UnsupportedError{Provider: Name, Capability: backend.Query.String()}
	return fmt.Errorf("parse: %s: %w", req.Resource.Table, unsupported)
}

// database is the provider's database as a backend.DB, which declares the
// tables its resources read (backend.Declarer).
type database struct {
	executor
}

func newDatabase(db *Database) database {
	d := database{executor: executor{db: db}}
	return d
}

func (d database) Declare(res *backend.Resource) error {
	return d.db.catalog.declare(res)
}

// Begin begins a transaction that lasts until Commit or Rollback, waiting
// for the writer under ctx: parse.Provider.Begin's contract, which the SQL
// providers keep too, is a transaction not bound to ctx's cancellation.
func (d database) Begin(ctx context.Context) (backend.Tx, error) {
	txn, err := d.db.begin(ctx)
	if err != nil {
		return nil, parse.StatementFailed("", "begin", err)
	}
	begun := transaction{executor: executor{db: d.db, txn: txn}}
	return begun, nil
}

// Join runs requests in tx, a transaction of this database (Begin, Tx).
func (d database) Join(tx backend.Tx) (backend.Executor, error) {
	t, ok := tx.(transaction)
	if !ok || t.db != d.db {
		return nil, errNotJoinable
	}
	return t.executor, nil
}

// errNotJoinable is a transaction to join that this database did not
// begin.
var errNotJoinable = errors.New("parsememory: not a transaction of this database")

// transaction is a transaction of the database as a backend.Tx.
type transaction struct {
	executor
}

func (t transaction) Commit(ctx context.Context) error {
	return parse.StatementFailed("", "commit", t.txn.Commit(ctx))
}

func (t transaction) Rollback(ctx context.Context) error {
	return t.txn.Rollback(ctx)
}
