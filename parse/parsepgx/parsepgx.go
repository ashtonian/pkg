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
	if err := db.SendBatch(ctx, b).Close(); err != nil {
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
	if err := db.SendBatch(ctx, b).Close(); err != nil {
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
	return db.SendBatch(ctx, b).Close()
}

// Update applies plan and reports its target rows as the update left them,
// split into the rows whose columns it wrote and the rows it left, and how
// many junction rows its membership changes touched (parse.Resource.FollowUp);
// neither split holding a row means the target is not found. The update
// runs in one transaction — a savepoint when db is a pgx.Tx (transact) —
// settled only once every row has scanned, so an error
// means nothing it wrote is kept. A guarded plan (SkipUnchanged:
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
func Update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.PostgresDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, db, func(tx pgx.Tx) error {
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
		return parse.StatementFailed("relations", tx.SendBatch(ctx, b).Close())
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
		err := db.SendBatch(ctx, b).Close()
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
	if err := db.SendBatch(ctx, b).Close(); err != nil {
		return nil, err
	}
	return resource.FollowUp(plan, owner, current)
}

// UpdateBatch applies plan in one transaction — a savepoint when db is one.
// Every chunk's Current goes first, in one batch, locking its targets in key
// order; each chunk's rows must be exactly its keys
// (parse.Resource.CheckBatchCurrent: parse.ErrUpdateBatchTargets otherwise,
// and nothing is written). Every Owner then goes in a second batch. The
// result (parse.Resource.BatchResult) holds as Changed the rows the owners
// wrote, chunk by chunk, and as Unchanged the locked rows a SkipUnchanged
// owner left as they were.
func UpdateBatch[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdateBatchPlan) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.PostgresDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	if len(plan.Chunks) == 0 {
		return parse.UpdateResult[T]{}, nil
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, db, func(tx pgx.Tx) error {
		current := make([][]T, len(plan.Chunks))
		b := &pgx.Batch{}
		for i, chunk := range plan.Chunks {
			b.Queue(chunk.Current.SQL, chunk.Current.Args...).Query(func(rs pgx.Rows) error {
				var err error
				current[i], err = collect(rs, r)
				return parse.StatementFailed("current", err)
			})
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		err := r.CheckBatchCurrent(plan, current)
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
			if err := tx.SendBatch(ctx, b).Close(); err != nil {
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
// caller's transaction is still usable; once apply succeeds the update is
// finished whatever ctx does since, so a reported error never hides an
// applied update. Settling — the commit, the release or the undo —
// therefore runs without ctx's cancellation: pgx sends nothing on a done
// context, which would leave a savepoint's writes in the caller's
// transaction. The savepoint is taken by hand rather than through pgx's
// nested Begin, whose failed release can no longer be rolled back.
func transact(ctx context.Context, db DB, apply func(pgx.Tx) error) error {
	settle := context.WithoutCancel(ctx)
	caller, inTx := db.(pgx.Tx)
	if inTx {
		return underSavepoint(ctx, settle, caller, apply)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return parse.StatementFailed("begin", err)
	}
	err = apply(tx)
	if err != nil {
		// A transaction pgx already closed (its connection lost) kept
		// nothing to roll back.
		rollback := tx.Rollback(settle)
		if !errors.Is(rollback, pgx.ErrTxClosed) {
			err = errors.Join(err, parse.StatementFailed("rollback", rollback))
		}
		return err
	}
	return parse.StatementFailed("commit", tx.Commit(settle))
}

// underSavepoint is transact in the caller's transaction tx: apply runs
// after a savepoint, released once apply succeeds and rolled back to
// otherwise, settling under settle.
func underSavepoint(ctx, settle context.Context, tx pgx.Tx, apply func(pgx.Tx) error) error {
	_, err := tx.Exec(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed("savepoint", err)
	}
	err = apply(tx)
	if err != nil {
		return errors.Join(err, undoSavepoint(settle, tx))
	}
	_, err = tx.Exec(settle, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed("release savepoint", err), undoSavepoint(settle, tx))
	}
	return nil
}

// undoSavepoint rolls the caller's transaction back to the update's
// savepoint and releases it, so the transaction keeps nothing the update
// wrote and stays usable, a failed statement's abort included.
func undoSavepoint(ctx context.Context, tx pgx.Tx) error {
	_, rollback := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
	_, release := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed("rollback to savepoint", errors.Join(rollback, release))
}
