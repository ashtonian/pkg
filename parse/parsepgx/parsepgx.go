// Package parsepgx runs parse plans on PostgreSQL through pgx.
// QueryPage answers a list plan — the page, its total, its facets and its
// includes — and Update applies an update plan with its relation changes.
// Both take the caller's context, connection and transaction; the core
// parse packages stay free of I/O.
package parsepgx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Querier sends statement batches: *pgxpool.Pool, *pgx.Conn and pgx.Tx have
// it.
type Querier interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// DB sends batches and opens transactions: *pgxpool.Pool and *pgx.Conn
// begin a transaction, pgx.Tx a savepoint.
type DB interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ErrDialect is a plan compiled for a dialect other than PostgreSQL.
var ErrDialect = errors.New("parsepgx: plan is not compiled for PostgreSQL")

// QueryPage reads the page plan selects, scanned into T by its db tags
// (pgx.RowToStructByNameLax: fields of columns the resource does not read
// stay zero), and returns it as r.Page builds it: with its
// total when the plan counts, its facet counts when the plan facets, and the
// related IDs of the page's rows for each relation the plan includes.
//
// The page, count and facet statements go to db in one batch, closed before
// the includes — which need the page's keys — go in a second. The statements
// run in their own snapshots unless db is a transaction at REPEATABLE READ
// or stricter; pass one when the total and facets must describe exactly the
// page's rows under concurrent writes.
func QueryPage[T any](ctx context.Context, db Querier, r *parse.Resource[T], plan *parse.ListPlan) (*filter.Page[T], error) {
	if plan == nil {
		return nil, fmt.Errorf("parsepgx: %s: nil list plan", r.Table())
	}
	if err := postgres(r.Table(), plan.Dialect()); err != nil {
		return nil, err
	}

	var (
		items  []T
		total  int64
		facets []filter.FacetResult
	)
	b := &pgx.Batch{}
	b.Queue(plan.Page.SQL, plan.Page.Args...).Query(func(rows pgx.Rows) error {
		var err error
		items, err = pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
		return wrap("page", err)
	})
	if plan.Count != nil {
		b.Queue(plan.Count.SQL, plan.Count.Args...).QueryRow(func(row pgx.Row) error {
			return wrap("count", row.Scan(&total))
		})
	}
	if plan.Facets != nil {
		b.Queue(plan.Facets.SQL, plan.Facets.Args...).Query(func(rows pgx.Rows) error {
			var err error
			facets, err = filter.ScanFacetUnion(rows)
			return wrap("facets", err)
		})
	}
	if err := db.SendBatch(ctx, b).Close(); err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}

	page, err := r.Page(items, plan)
	if err != nil {
		return nil, err
	}
	if plan.Count != nil {
		page.PageInfo.TotalCount = &total
	}
	page.Facets = filter.FacetMap(facets)
	if err := queryIncludes(ctx, db, r, plan, page); err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return page, nil
}

// queryIncludes reads the related IDs of the page's rows, one statement per
// included relation, in one batch.
func queryIncludes[T any](ctx context.Context, db Querier, r *parse.Resource[T], plan *parse.ListPlan, page *filter.Page[T]) error {
	if len(plan.Include) == 0 || len(page.Items) == 0 {
		return nil
	}
	keys, err := ownerKeys(r, page.Items)
	if err != nil {
		return err
	}
	queries, err := r.IncludeQueries(plan, keys)
	if err != nil || len(queries) == 0 {
		return err
	}
	b := &pgx.Batch{}
	for _, q := range queries {
		b.Queue(q.SQL, q.Args...).Query(func(rows pgx.Rows) error {
			res, err := q.Scan(rows)
			if err != nil {
				return fmt.Errorf("include %s: %w", q.Field, err)
			}
			page.SetInclude(q.Field, res)
			return nil
		})
	}
	return db.SendBatch(ctx, b).Close()
}

// Update applies plan and returns the rows its owner statement matched, as
// the update left them; none when no row in the plan's scope is a target.
// A plan that changes relation memberships runs in a transaction — a
// savepoint when db is one — so its rows and memberships change together:
// the owner statement first, then the membership changes for the keys it
// returned, never the requested ones. A plan that writes nothing
// (plan.Writes false: an empty patch) reads its targets; a caller that
// audits or notifies per updated row checks plan.Writes first.
func Update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan) ([]T, error) {
	if plan == nil {
		return nil, fmt.Errorf("parsepgx: %s: nil update plan", r.Table())
	}
	if err := postgres(r.Table(), plan.Dialect()); err != nil {
		return nil, err
	}
	if !plan.HasRelations() {
		rows, err := queryOwner[T](ctx, db, plan)
		if err != nil {
			return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
		}
		return rows, nil
	}

	var rows []T
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		if rows, err = queryOwner[T](ctx, tx, plan); err != nil {
			return err
		}
		keys, err := ownerKeys(r, rows)
		if err != nil {
			return err
		}
		stmts, err := r.RelationStatements(plan, keys)
		if err != nil || len(stmts) == 0 {
			return err
		}
		b := &pgx.Batch{}
		for _, s := range stmts {
			b.Queue(s.SQL, s.Args...)
		}
		return wrap("relations", tx.SendBatch(ctx, b).Close())
	})
	if err != nil {
		return nil, fmt.Errorf("parsepgx: %s: %w", r.Table(), err)
	}
	return rows, nil
}

func queryOwner[T any](ctx context.Context, db Querier, plan *parse.UpdatePlan) ([]T, error) {
	var rows []T
	b := &pgx.Batch{}
	b.Queue(plan.Owner.SQL, plan.Owner.Args...).Query(func(r pgx.Rows) error {
		var err error
		rows, err = pgx.CollectRows(r, pgx.RowToStructByNameLax[T])
		return wrap("owner", err)
	})
	if err := db.SendBatch(ctx, b).Close(); err != nil {
		return nil, err
	}
	return rows, nil
}

func ownerKeys[T any](r *parse.Resource[T], items []T) ([]any, error) {
	keys := make([]any, len(items))
	for i, item := range items {
		k, err := r.Key(item)
		if err != nil {
			return nil, err
		}
		keys[i] = k
	}
	return keys, nil
}

func postgres(table string, d filter.Dialect) error {
	if d == nil || d.Name() != (filter.PostgresDialect{}).Name() {
		return fmt.Errorf("parsepgx: %s: %w", table, ErrDialect)
	}
	return nil
}

func wrap(stmt string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", stmt, err)
}
