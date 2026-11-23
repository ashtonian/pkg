// Package parsesql runs parse plans on SQLite through database/sql, with
// any SQLite driver. QueryPage answers a list plan — the page, its total,
// its facets and its includes —, Update applies an update plan with its
// relation changes, UpdateBatch applies a batch plan, and Query reads one
// statement's rows. Rows scan through the resource's row contract
// (parse.RowScanner), which decodes the JSON text SQLite stores documents
// and arrays in and parses its timestamp text. The SQL functions SQLite
// plans call are registered by parsesqlite. PostgreSQL runs through
// parsepgx, whose codecs scan PostgreSQL's native arrays by type.
//
// SQLite has no row lock: an update that reads before it writes (a
// SkipUnchanged plan's Current, a batch's preimages) holds its targets only
// in a write transaction. database/sql begins transactions in the mode the
// connection string names, so open the database with _txlock=immediate, and
// with _busy_timeout so a second writer waits rather than failing with
// SQLITE_BUSY (modernc.org/sqlite and mattn/go-sqlite3 both read them); a
// *sql.Tx passed in is begun on such a connection.
package parsesql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Queryer runs a query: *sql.DB, *sql.Conn and *sql.Tx have it.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DB runs queries and statements: *sql.DB and *sql.Conn, on which Update
// and UpdateBatch begin their own transaction, and *sql.Tx, in which they
// run under a savepoint the caller's rollback undoes.
type DB interface {
	Queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// beginner is a DB that opens transactions.
type beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// ErrDialect is a plan compiled for a dialect other than SQLite.
var ErrDialect = errors.New("parsesql: plan is not compiled for SQLite")

// savepoint names the savepoint Update and UpdateBatch take in a caller's
// transaction.
const savepoint = "parsesql_update"

// Query runs st, a statement over r compiled for SQLite that reads
// r.Columns (a repository's own read of the table, or a plan's page,
// Current or Reread), and returns its rows scanned through r's row
// contract. A row that fails to scan yields no rows at all.
func Query[T any](ctx context.Context, db Queryer, r *parse.Resource[T], st parse.Statement) ([]T, error) {
	rows, err := query(ctx, db, r, st)
	if err != nil {
		return nil, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return rows, nil
}

// QueryPage reads the page plan selects, scanned through r's row contract,
// and returns it as r.Page builds it: with its total when the plan counts,
// its facet counts when the plan facets, and the related IDs of the page's
// rows for each relation the plan includes. The statements run one after
// another, each in its own snapshot unless db is a transaction; pass one
// when the total and facets must describe exactly the page's rows under
// concurrent writes.
func QueryPage[T any](ctx context.Context, db Queryer, r *parse.Resource[T], plan *parse.ListPlan) (*filter.Page[T], error) {
	if plan == nil {
		return nil, fmt.Errorf("parsesql: %s: nil list plan", r.Table())
	}
	if err := sqlite(r.Table(), plan.Dialect()); err != nil {
		return nil, err
	}
	page, err := queryPage(ctx, db, r, plan)
	if err != nil {
		return nil, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return page, nil
}

func queryPage[T any](ctx context.Context, db Queryer, r *parse.Resource[T], plan *parse.ListPlan) (*filter.Page[T], error) {
	items, err := query(ctx, db, r, plan.Page)
	if err != nil {
		return nil, wrap("page", err)
	}
	page, err := r.Page(items, plan)
	if err != nil {
		return nil, err
	}
	if plan.Count != nil {
		var total int64
		if err := scanOne(ctx, db, *plan.Count, &total); err != nil {
			return nil, wrap("count", err)
		}
		page.PageInfo.TotalCount = &total
	}
	var facets []filter.FacetResult
	if plan.Facets != nil {
		err := each(ctx, db, plan.Facets.SQL, plan.Facets.Args, func(rows *sql.Rows) error {
			var err error
			facets, err = filter.ScanFacetUnion(rows)
			return err
		})
		if err != nil {
			return nil, wrap("facets", err)
		}
	}
	page.Facets = filter.FacetMap(facets)
	if len(plan.Include) == 0 || len(page.Items) == 0 {
		return page, nil
	}
	keys, err := ownerKeys(r, page.Items)
	if err != nil {
		return nil, err
	}
	queries, err := r.IncludeQueries(plan, keys)
	if err != nil {
		return nil, err
	}
	for _, q := range queries {
		err := each(ctx, db, q.SQL, q.Args, func(rows *sql.Rows) error {
			res, err := q.Scan(rows)
			if err != nil {
				return err
			}
			page.SetInclude(q.Field, res)
			return nil
		})
		if err != nil {
			return nil, wrap("include "+q.Field, err)
		}
	}
	return page, nil
}

// Update applies plan and reports its target rows as the update left them,
// split into the rows whose columns it wrote and the rows it left
// (parse.Resource.UpdatedRows), and how many junction rows its membership
// changes touched; neither split holding a row means the target is not
// found. A plan that reads before it writes (SkipUnchanged or a patch of
// document members: plan.Current set) or changes relation memberships runs
// in one transaction — a savepoint when db is a *sql.Tx — so its rows and
// memberships change together: Current, then Conflict, whose row fails the
// update before anything is written (UpdatePlan.ConflictError), the owner
// statement, the membership changes for the
// keys of the rows it returned (changed, then unchanged), never the
// requested keys, then, for a resource with computed columns
// (parse.ReadExpr), those rows again (Resource.Reread).
func Update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan) (parse.UpdateResult[T], error) {
	if plan == nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsesql: %s: nil update plan", r.Table())
	}
	if err := sqlite(r.Table(), plan.Dialect()); err != nil {
		return parse.UpdateResult[T]{}, err
	}
	var res parse.UpdateResult[T]
	apply := func(db DB) error {
		var err error
		res, err = update(ctx, db, r, plan)
		return err
	}
	var err error
	if plan.Current == nil && !plan.HasRelations() {
		err = apply(db)
	} else {
		err = transact(ctx, db, apply)
	}
	if err != nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return res, nil
}

func update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan) (parse.UpdateResult[T], error) {
	var current []T
	if plan.Current != nil {
		rows, err := query(ctx, db, r, *plan.Current)
		if err != nil {
			return parse.UpdateResult[T]{}, wrap("current", err)
		}
		current = rows
	}
	if plan.Conflict != nil {
		var op int64
		switch err := scanOne(ctx, db, *plan.Conflict, &op); {
		case err == nil:
			return parse.UpdateResult[T]{}, plan.ConflictError(op)
		case !errors.Is(err, sql.ErrNoRows):
			return parse.UpdateResult[T]{}, wrap("conflict", err)
		}
	}
	owner, err := query(ctx, db, r, plan.Owner)
	if err != nil {
		return parse.UpdateResult[T]{}, wrap("owner", err)
	}
	changed, unchanged, err := r.UpdatedRows(plan, owner, current)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	res := parse.UpdateResult[T]{Changed: changed, Unchanged: unchanged}
	if !plan.HasRelations() {
		return res, nil
	}
	keys, err := ownerKeys(r, slices.Concat(changed, unchanged))
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	stmts, err := r.RelationStatements(plan, keys)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	for _, s := range stmts {
		result, err := db.ExecContext(ctx, s.SQL, s.Args...)
		if err != nil {
			return parse.UpdateResult[T]{}, wrap("relations", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return parse.UpdateResult[T]{}, wrap("relations", err)
		}
		res.RelationRows += affected
	}
	// The changed and the unchanged rows are read again apart, so each keeps
	// its side of the split.
	for i, part := range [2][]parse.Key{keys[:len(changed)], keys[len(changed):]} {
		stmt, err := r.Reread(plan, part)
		if err != nil {
			return parse.UpdateResult[T]{}, err
		}
		if stmt == nil {
			continue
		}
		rows, err := query(ctx, db, r, *stmt)
		if err != nil {
			return parse.UpdateResult[T]{}, wrap("reread", err)
		}
		if i == 0 {
			res.Changed = rows
		} else {
			res.Unchanged = rows
		}
	}
	return res, nil
}

// UpdateBatch applies plan in one transaction — a savepoint when db is a
// *sql.Tx. Every chunk's Current runs first, and each chunk's rows must be
// exactly its keys (UpdateBatchChunk.CheckCurrent: parse.ErrUpdateBatchTargets
// otherwise, and nothing is written); every Owner then runs, in chunk order.
// The result's Changed are the rows the owners wrote, chunk by chunk; its
// Unchanged the read rows a SkipUnchanged owner left as they were.
func UpdateBatch[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdateBatchPlan) (parse.UpdateResult[T], error) {
	if plan == nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsesql: %s: nil update batch plan", r.Table())
	}
	if err := sqlite(r.Table(), plan.Dialect()); err != nil {
		return parse.UpdateResult[T]{}, err
	}
	if len(plan.Chunks) == 0 {
		return parse.UpdateResult[T]{}, nil
	}
	var res parse.UpdateResult[T]
	err := transact(ctx, db, func(db DB) error {
		var err error
		res, err = updateBatch(ctx, db, r, plan)
		return err
	})
	if err != nil {
		return parse.UpdateResult[T]{}, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return res, nil
}

func updateBatch[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdateBatchPlan) (parse.UpdateResult[T], error) {
	current := make([][]T, len(plan.Chunks))
	for i, chunk := range plan.Chunks {
		rows, err := query(ctx, db, r, chunk.Current)
		if err != nil {
			return parse.UpdateResult[T]{}, wrap("current", err)
		}
		keys, err := ownerKeys(r, rows)
		if err != nil {
			return parse.UpdateResult[T]{}, err
		}
		if err := chunk.CheckCurrent(keys); err != nil {
			return parse.UpdateResult[T]{}, err
		}
		current[i] = rows
	}
	var res parse.UpdateResult[T]
	for i, chunk := range plan.Chunks {
		var owner []T
		if chunk.Owner != nil {
			rows, err := query(ctx, db, r, *chunk.Owner)
			if err != nil {
				return parse.UpdateResult[T]{}, wrap("owner", err)
			}
			owner = rows
		}
		unchanged, err := r.Unchanged(owner, current[i])
		if err != nil {
			return parse.UpdateResult[T]{}, err
		}
		res.Changed = append(res.Changed, owner...)
		res.Unchanged = append(res.Unchanged, unchanged...)
	}
	return res, nil
}

// transact runs apply in a transaction of its own when db opens them, and
// under a savepoint of the caller's transaction otherwise, so a failure
// undoes everything apply wrote and leaves the caller's transaction usable.
func transact(ctx context.Context, db DB, apply func(DB) error) error {
	if b, ok := db.(beginner); ok {
		tx, err := b.BeginTx(ctx, nil)
		if err != nil {
			return wrap("begin", err)
		}
		if err := apply(tx); err != nil {
			// A cancelled context has already rolled the transaction back.
			if rollback := tx.Rollback(); !errors.Is(rollback, sql.ErrTxDone) {
				err = errors.Join(err, wrap("rollback", rollback))
			}
			return err
		}
		return wrap("commit", tx.Commit())
	}
	if _, err := db.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return wrap("savepoint", err)
	}
	if err := apply(db); err != nil {
		// The undo runs even when ctx is what failed: the caller's
		// transaction outlives this call and must not keep a partial update.
		undo := context.WithoutCancel(ctx)
		_, rollback := db.ExecContext(undo, "ROLLBACK TO SAVEPOINT "+savepoint)
		_, release := db.ExecContext(undo, "RELEASE SAVEPOINT "+savepoint)
		return errors.Join(err, wrap("rollback to savepoint", errors.Join(rollback, release)))
	}
	_, err := db.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
	return wrap("release savepoint", err)
}

func query[T any](ctx context.Context, db Queryer, r *parse.Resource[T], st parse.Statement) ([]T, error) {
	scanner, err := r.RowScanner(filter.SQLiteDialect{})
	if err != nil {
		return nil, err
	}
	var out []T
	err = each(ctx, db, st.SQL, st.Args, func(rows *sql.Rows) error {
		for rows.Next() {
			item, err := scanner.Scan(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// each runs a query and hands its rows to read, closing them after.
func each(ctx context.Context, db Queryer, q string, args []any, read func(*sql.Rows) error) (err error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if err := read(rows); err != nil {
		return err
	}
	return rows.Err()
}

// scanOne reads a statement's single row into dest.
func scanOne(ctx context.Context, db Queryer, st parse.Statement, dest ...any) error {
	return each(ctx, db, st.SQL, st.Args, func(rows *sql.Rows) error {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		return rows.Scan(dest...)
	})
}

func ownerKeys[T any](r *parse.Resource[T], items []T) ([]parse.Key, error) {
	keys := make([]parse.Key, len(items))
	for i, item := range items {
		k, err := r.Key(item)
		if err != nil {
			return nil, err
		}
		keys[i] = k
	}
	return keys, nil
}

func sqlite(table string, d filter.Dialect) error {
	if d == nil || d.Name() != (filter.SQLiteDialect{}).Name() {
		return fmt.Errorf("parsesql: %s: %w", table, ErrDialect)
	}
	return nil
}

func wrap(stmt string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", stmt, err)
}
