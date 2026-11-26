// Package parsepgx runs parse plans on PostgreSQL through pgx.
// QueryPage answers a list plan — the page, its total, its facets and its
// includes —, Update applies an update plan with its relation changes,
// UpdateBatch applies a batch plan, and Query reads one statement's rows.
// Rows scan through the resource's row contract (parse.RowScanner).
// Both take the caller's context, connection and transaction; the core
// parse packages stay free of I/O.
package parsepgx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Querier sends statement batches: *pgxpool.Pool, *pgx.Conn and pgx.Tx have
// it.
type Querier interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// DB sends batches and opens transactions: on *pgxpool.Pool and *pgx.Conn
// an update begins its own transaction, and in a pgx.Tx it takes a
// savepoint the caller's rollback undoes.
type DB interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// savepoint names the savepoint Update and UpdateBatch take in a caller's
// transaction.
const savepoint = "parsepgx_update"

// QueryPage reads the page plan selects, scanned into T through r's row
// contract (parse.RowScanner), and returns it as r.Page builds it: with its
// total when the plan counts, its facet counts when the plan facets, and the
// related IDs of the page's rows for each relation the plan includes.
//
// The page, count and facet statements go to db in one batch, closed before
// the includes — which need the page's keys — go in a second. The statements
// run in their own snapshots unless db is a transaction at REPEATABLE READ
// or stricter; pass one when the total and facets must describe exactly the
// page's rows under concurrent writes.
func QueryPage[T any](ctx context.Context, db Querier, r *parse.Resource[T], plan *parse.ListPlan) (*filter.Page[T], error) {
	err := r.CheckPlan(plan, filter.PostgresDialect{})
	if err != nil {
		return nil, err
	}

	var read parse.ListRows[T]
	b := &pgx.Batch{}
	b.Queue(plan.Page.SQL, plan.Page.Args...).Query(func(rows pgx.Rows) error {
		var err error
		read.Items, err = collect(rows, r)
		return parse.StatementFailed("page", err)
	})
	if plan.Count != nil {
		b.Queue(plan.Count.SQL, plan.Count.Args...).QueryRow(func(row pgx.Row) error {
			return parse.StatementFailed("count", row.Scan(&read.Total))
		})
	}
	if plan.Facets != nil {
		b.Queue(plan.Facets.SQL, plan.Facets.Args...).Query(func(rows pgx.Rows) error {
			var err error
			read.Facets, err = filter.ScanFacetUnion(rows)
			return parse.StatementFailed("facets", err)
		})
	}
	err = sendBatch(ctx, db, b)
	if err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}

	page, includes, err := r.ListPage(plan, read)
	if err != nil {
		return nil, err
	}
	err = queryIncludes(ctx, db, page, includes)
	if err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return page, nil
}

// Query runs st, a statement over r compiled for PostgreSQL that reads
// r.Columns (a repository's own read of the table, or a plan's Current or
// Reread), and returns its rows scanned through r's row contract.
func Query[T any](ctx context.Context, db Querier, r *parse.Resource[T], st parse.Statement) ([]T, error) {
	var rows []T
	b := &pgx.Batch{}
	b.Queue(st.SQL, st.Args...).Query(func(rs pgx.Rows) error {
		var err error
		rows, err = collect(rs, r)
		return err
	})
	err := sendBatch(ctx, db, b)
	if err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return rows, nil
}

// queryIncludes reads the related IDs of the page's rows, one statement per
// included relation, in one batch.
func queryIncludes[T any](ctx context.Context, db Querier, page *filter.Page[T], queries []filter.IncludeQuery) error {
	if len(queries) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, q := range queries {
		b.Queue(q.SQL, q.Args...).Query(func(rows pgx.Rows) error {
			res, err := q.Scan(rows)
			if err != nil {
				return parse.StatementFailed("include "+q.Field, err)
			}
			page.SetInclude(q.Field, res)
			return nil
		})
	}
	return sendBatch(ctx, db, b)
}

// Update applies plan and reports its target rows as the update left them,
// split into the rows whose columns it wrote and the rows it left, and how
// many junction rows its membership changes touched (parse.Resource.FollowUp);
// neither split holding a row means the target is not found. The update
// runs in one transaction — a savepoint when db is a pgx.Tx (transact) —
// settled only once every row has scanned, so an error means nothing it
// wrote is kept, unless errors.Is(err, parse.ErrCommitUnknown); opts bound
// the settle (parse.WithSettleTimeout). A guarded plan (SkipUnchanged:
// plan.Current set) sends Current before Owner in one batch, so Current's
// lock holds every target while Owner compares them. A patch of document
// members (plan.Conflict set) sends Current and Conflict, whose row fails
// the update before Owner is sent (UpdatePlan.ConflictError), then Owner.
//
// A plan that changes relation memberships then changes them for the keys
// of the rows the owner statement returned (changed, then unchanged), never
// the requested keys, then, for a resource with computed columns
// (parse.ReadExpr), reads those rows again (Resource.Reread), so a column
// over a relation's pairs reads them as the update left them.
func Update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan, opts ...parse.ExecOption) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.PostgresDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	cfg, err := parse.NewExecConfig(opts...)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, cfg, db, func(tx pgx.Tx) error {
		follow, err := queryTargets(ctx, tx, r, plan)
		if err != nil {
			return err
		}
		res = follow.Result
		if len(follow.Relations) == 0 {
			return nil
		}
		// The membership changes and the rereads after them go in one batch.
		b := &pgx.Batch{}
		for _, s := range follow.Relations {
			b.Queue(s.SQL, s.Args...).Exec(func(tag pgconn.CommandTag) error {
				res.RelationRows += tag.RowsAffected()
				return nil
			})
		}
		reread := func(stmt *parse.Statement, side *[]T) {
			if stmt == nil {
				return
			}
			b.Queue(stmt.SQL, stmt.Args...).Query(func(rs pgx.Rows) error {
				var err error
				*side, err = collect(rs, r)
				return parse.StatementFailed("reread", err)
			})
		}
		reread(follow.RereadChanged, &res.Changed)
		reread(follow.RereadUnchanged, &res.Unchanged)
		return parse.StatementFailed("relations", sendBatch(ctx, tx, b))
	})
	if err != nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return res, nil
}

// queryTargets runs plan's owner statement and returns what the update
// runs next (parse.Resource.FollowUp), the target rows split as the owner
// statement left them. A plan's Current goes first in the same batch of the
// update's transaction: its lock holds every target while the guarded
// update compares them, so the rows it read and the update did not return
// are exactly the unchanged ones.
func queryTargets[T any](ctx context.Context, db Querier, resource *parse.Resource[T], plan *parse.UpdatePlan) (*parse.UpdateFollowUp[T], error) {
	var current, owner []T
	b := &pgx.Batch{}
	if plan.Current != nil {
		b.Queue(plan.Current.SQL, plan.Current.Args...).Query(func(r pgx.Rows) error {
			var err error
			current, err = collect(r, resource)
			return parse.StatementFailed("current", err)
		})
	}
	if plan.Conflict != nil {
		// Owner goes in a batch of its own, so a conflict sends no write.
		var unmet *int64
		b.Queue(plan.Conflict.SQL, plan.Conflict.Args...).Query(func(r pgx.Rows) error {
			defer r.Close()
			if r.Next() {
				var op int64
				err := r.Scan(&op)
				if err != nil {
					return parse.StatementFailed("conflict", err)
				}
				unmet = &op
			}
			return parse.StatementFailed("conflict", r.Err())
		})
		err := sendBatch(ctx, db, b)
		if err != nil {
			return nil, err
		}
		if unmet != nil {
			return nil, plan.ConflictError(*unmet)
		}
		b = &pgx.Batch{}
	}
	b.Queue(plan.Owner.SQL, plan.Owner.Args...).Query(func(r pgx.Rows) error {
		var err error
		owner, err = collect(r, resource)
		return parse.StatementFailed("owner", err)
	})
	err := sendBatch(ctx, db, b)
	if err != nil {
		return nil, err
	}
	return resource.FollowUp(plan, owner, current)
}

// UpdateBatch applies plan in one transaction — a savepoint when db is one —
// held as Update holds its own, opts included.
// Every chunk's Current goes first, in one batch, locking its targets in key
// order; each chunk's rows must be exactly its keys
// (parse.Resource.CheckBatchCurrent: parse.ErrUpdateBatchTargets otherwise,
// and nothing is written). Every Owner then goes in a second batch. The
// result (parse.Resource.BatchResult) holds as Changed the rows the owners
// wrote, chunk by chunk, and as Unchanged the locked rows a SkipUnchanged
// owner left as they were.
func UpdateBatch[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdateBatchPlan, opts ...parse.ExecOption) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.PostgresDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	cfg, err := parse.NewExecConfig(opts...)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	if len(plan.Chunks) == 0 {
		return parse.UpdateResult[T]{}, nil
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, cfg, db, func(tx pgx.Tx) error {
		current := make([][]T, len(plan.Chunks))
		b := &pgx.Batch{}
		for i, chunk := range plan.Chunks {
			b.Queue(chunk.Current.SQL, chunk.Current.Args...).Query(func(rs pgx.Rows) error {
				var err error
				current[i], err = collect(rs, r)
				return parse.StatementFailed("current", err)
			})
		}
		err := sendBatch(ctx, tx, b)
		if err != nil {
			return err
		}
		err = r.CheckBatchCurrent(plan, current)
		if err != nil {
			return err
		}
		owner := make([][]T, len(plan.Chunks))
		b = &pgx.Batch{}
		for i, chunk := range plan.Chunks {
			if chunk.Owner == nil {
				continue
			}
			b.Queue(chunk.Owner.SQL, chunk.Owner.Args...).Query(func(rs pgx.Rows) error {
				var err error
				owner[i], err = collect(rs, r)
				return parse.StatementFailed("owner", err)
			})
		}
		if b.Len() > 0 {
			err := sendBatch(ctx, tx, b)
			if err != nil {
				return err
			}
		}
		res, err = r.BatchResult(plan, current, owner)
		return err
	})
	if err != nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return res, nil
}

// transact runs apply in a transaction of its own when db is no
// transaction, and under a savepoint of the caller's pgx.Tx otherwise, and
// settles it only once apply has returned, every row read and scanned. Its
// contract is parsesql's: an error means nothing apply wrote is kept and the
// caller's transaction is still usable, but for parse.ErrCommitUnknown
// (commitFailed); once apply succeeds the update is finished whatever ctx
// does since, so a reported error never hides an applied update. Each
// settling statement — the commit, the release or the undo — therefore runs
// under its own settle context (parse.ExecConfig.SettleContext): pgx sends
// nothing on a done context, which would leave a savepoint's writes in the
// caller's transaction. A panic in apply settles as an error does on its way
// out, so no transaction stays open on the connection. The savepoint is
// taken by hand rather than through pgx's nested Begin, whose failed release
// can no longer be rolled back.
func transact(ctx context.Context, cfg parse.ExecConfig, db DB, apply func(pgx.Tx) error) error {
	caller, inTx := db.(pgx.Tx)
	if inTx {
		return underSavepoint(ctx, cfg, caller, apply)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return parse.StatementFailed("begin", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller, so the rollback's error has
			// nowhere to go; a rollback that fails closes the connection,
			// and the server rolls back with it.
			_ = settle(ctx, cfg, tx.Rollback)
		}
	}()
	err = apply(tx)
	returned = true
	if err != nil {
		// A transaction pgx already closed (its connection lost) kept
		// nothing to roll back.
		rollback := settle(ctx, cfg, tx.Rollback)
		if !errors.Is(rollback, pgx.ErrTxClosed) {
			err = errors.Join(err, parse.StatementFailed("rollback", rollback))
		}
		return err
	}
	return commitFailed(settle(ctx, cfg, tx.Commit))
}

// commitFailed is a failed COMMIT's error, saying whether it may have taken.
// PostgreSQL answers a COMMIT it refuses with an ERROR (a deferred
// constraint, a serialization failure) and rolls the transaction back, as it
// does for the COMMIT of an aborted transaction (pgx.ErrTxCommitRollback);
// pgx reports a COMMIT it never sent as safe to retry. Any other failure —
// the connection lost or the settle timeout run out while the COMMIT was in
// flight, or a FATAL that ends the session — leaves the outcome unknown.
func commitFailed(err error) error {
	if err == nil {
		return nil
	}
	failed := parse.StatementFailed("commit", err)
	pgErr, answered := errors.AsType[*pgconn.PgError](err)
	refused := answered && pgErr.SeverityUnlocalized == "ERROR"
	if refused || errors.Is(err, pgx.ErrTxCommitRollback) || pgconn.SafeToRetry(err) {
		return failed
	}
	return fmt.Errorf("%w: %w", parse.ErrCommitUnknown, failed)
}

// underSavepoint is transact in the caller's transaction tx: apply runs
// after a savepoint, released once apply succeeds and rolled back to
// otherwise, or as a panic in apply unwinds.
func underSavepoint(ctx context.Context, cfg parse.ExecConfig, tx pgx.Tx, apply func(pgx.Tx) error) error {
	_, err := tx.Exec(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed("savepoint", err)
	}
	returned := false
	defer func() {
		if !returned {
			// The panic goes on to the caller; the undo's error has nowhere
			// to go.
			_ = undoSavepoint(ctx, cfg, tx)
		}
	}()
	err = apply(tx)
	returned = true
	if err != nil {
		return errors.Join(err, undoSavepoint(ctx, cfg, tx))
	}
	err = settleExec(ctx, cfg, tx, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed("release savepoint", err), undoSavepoint(ctx, cfg, tx))
	}
	return nil
}

// undoSavepoint rolls the caller's transaction back to the update's
// savepoint and releases it, so the transaction keeps nothing the update
// wrote and stays usable, a failed statement's abort included.
func undoSavepoint(ctx context.Context, cfg parse.ExecConfig, tx pgx.Tx) error {
	rollback := settleExec(ctx, cfg, tx, "ROLLBACK TO SAVEPOINT "+savepoint)
	release := settleExec(ctx, cfg, tx, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed("rollback to savepoint", errors.Join(rollback, release))
}

// settleExec runs statement, one that settles the update, on tx.
func settleExec(ctx context.Context, cfg parse.ExecConfig, tx pgx.Tx, statement string) error {
	return settle(ctx, cfg, func(ctx context.Context) error {
		_, err := tx.Exec(ctx, statement)
		return err
	})
}

// settle runs call, one settling statement, under a settle context of its
// own, so a statement that ran out of time leaves the next its full bound.
func settle(ctx context.Context, cfg parse.ExecConfig, call func(context.Context) error) error {
	settleCtx, cancel := cfg.SettleContext(ctx)
	defer cancel()
	return call(settleCtx)
}

// sendBatch sends b and reads every result through b's callbacks. A
// callback that panics leaves the rest of the batch unread and the
// connection busy (and a pool's connection unreleased); reading the rest as
// the panic unwinds frees it for the settle that follows.
func sendBatch(ctx context.Context, db Querier, b *pgx.Batch) error {
	results := db.SendBatch(ctx, b)
	closed := false
	defer func() {
		if !closed {
			_ = results.Close()
		}
	}()
	err := results.Close()
	closed = true
	return err
}
