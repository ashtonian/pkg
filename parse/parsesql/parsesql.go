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
	err := r.CheckPlan(plan, filter.SQLiteDialect{})
	if err != nil {
		return nil, err
	}
	page, err := queryPage(ctx, db, r, plan)
	if err != nil {
		return nil, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return page, nil
}

func queryPage[T any](ctx context.Context, db Queryer, r *parse.Resource[T], plan *parse.ListPlan) (*filter.Page[T], error) {
	var (
		read parse.ListRows[T]
		err  error
	)
	read.Items, err = query(ctx, db, r, plan.Page)
	if err != nil {
		return nil, parse.StatementFailed("page", err)
	}
	if plan.Count != nil {
		err = scanOne(ctx, db, *plan.Count, &read.Total)
		if err != nil {
			return nil, parse.StatementFailed("count", err)
		}
	}
	if plan.Facets != nil {
		err := each(ctx, db, plan.Facets.SQL, plan.Facets.Args, func(rows *sql.Rows) error {
			var err error
			read.Facets, err = filter.ScanFacetUnion(rows)
			return err
		})
		if err != nil {
			return nil, parse.StatementFailed("facets", err)
		}
	}
	page, queries, err := r.ListPage(plan, read)
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
			return nil, parse.StatementFailed("include "+q.Field, err)
		}
	}
	return page, nil
}

// Update applies plan and reports its target rows as the update left them,
// split into the rows whose columns it wrote and the rows it left, and how
// many junction rows its membership changes touched (parse.Resource.FollowUp);
// neither split holding a row means the target is not found. The update
// runs in one transaction — a savepoint when db is a *sql.Tx (transact) —
// so its rows and memberships change together and an error means nothing
// it wrote is kept, a row that fails to scan included:
// Current (SkipUnchanged or a patch of document members), then Conflict,
// whose row fails the update before anything is written
// (UpdatePlan.ConflictError), the owner statement, the membership changes
// for the keys of the rows it returned (changed, then unchanged), never the
// requested keys, then, for a resource with computed columns
// (parse.ReadExpr), those rows again (Resource.Reread).
func Update[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdatePlan) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.SQLiteDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, db, func(db DB) error {
		var err error
		res, err = update(ctx, db, r, plan)
		return err
	})
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
			return parse.UpdateResult[T]{}, parse.StatementFailed("current", err)
		}
		current = rows
	}
	if plan.Conflict != nil {
		var op int64
		switch err := scanOne(ctx, db, *plan.Conflict, &op); {
		case err == nil:
			return parse.UpdateResult[T]{}, plan.ConflictError(op)
		case !errors.Is(err, sql.ErrNoRows):
			return parse.UpdateResult[T]{}, parse.StatementFailed("conflict", err)
		}
	}
	owner, err := query(ctx, db, r, plan.Owner)
	if err != nil {
		return parse.UpdateResult[T]{}, parse.StatementFailed("owner", err)
	}
	follow, err := r.FollowUp(plan, owner, current)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	res := follow.Result
	for _, s := range follow.Relations {
		result, err := db.ExecContext(ctx, s.SQL, s.Args...)
		if err != nil {
			return parse.UpdateResult[T]{}, parse.StatementFailed("relations", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return parse.UpdateResult[T]{}, parse.StatementFailed("relations", err)
		}
		res.RelationRows += affected
	}
	res.Changed, err = reread(ctx, db, r, follow.RereadChanged, res.Changed)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	res.Unchanged, err = reread(ctx, db, r, follow.RereadUnchanged, res.Unchanged)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	return res, nil
}

// reread is rows as stmt, one of the update's rereads, reads them again
// once the relation statements have run; rows as they are when stmt is nil.
func reread[T any](ctx context.Context, db Queryer, r *parse.Resource[T], stmt *parse.Statement, rows []T) ([]T, error) {
	if stmt == nil {
		return rows, nil
	}
	again, err := query(ctx, db, r, *stmt)
	if err != nil {
		return nil, parse.StatementFailed("reread", err)
	}
	return again, nil
}

// UpdateBatch applies plan in one transaction — a savepoint when db is a
// *sql.Tx. Every chunk's Current runs first, then each chunk's rows must be
// exactly its keys (parse.Resource.CheckBatchCurrent:
// parse.ErrUpdateBatchTargets otherwise, and nothing is written); every
// Owner then runs, in chunk order. The result (parse.Resource.BatchResult)
// holds as Changed the rows the owners wrote, chunk by chunk, and as
// Unchanged the read rows a SkipUnchanged owner left as they were.
func UpdateBatch[T any](ctx context.Context, db DB, r *parse.Resource[T], plan *parse.UpdateBatchPlan) (parse.UpdateResult[T], error) {
	err := r.CheckPlan(plan, filter.SQLiteDialect{})
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	if len(plan.Chunks) == 0 {
		return parse.UpdateResult[T]{}, nil
	}
	var res parse.UpdateResult[T]
	err = transact(ctx, db, func(db DB) error {
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
			return parse.UpdateResult[T]{}, parse.StatementFailed("current", err)
		}
		current[i] = rows
	}
	err := r.CheckBatchCurrent(plan, current)
	if err != nil {
		return parse.UpdateResult[T]{}, err
	}
	owner := make([][]T, len(plan.Chunks))
	for i, chunk := range plan.Chunks {
		if chunk.Owner == nil {
			continue
		}
		rows, err := query(ctx, db, r, *chunk.Owner)
		if err != nil {
			return parse.UpdateResult[T]{}, parse.StatementFailed("owner", err)
		}
		owner[i] = rows
	}
	return r.BatchResult(plan, current, owner)
}

// transact runs apply in a transaction of its own when db opens them, and
// under a savepoint of the caller's transaction otherwise, and settles it
// only once apply has returned, every row read and scanned. Its contract is
// parsepgx's: an error means nothing apply wrote is kept and the caller's
// transaction is still usable; once apply succeeds the update is finished
// whatever ctx does since, so a reported error never hides an applied
// update. Settling — the commit, the release or the undo — therefore runs
// without ctx's cancellation: the transaction is begun under it too, so
// database/sql does not roll back on cancellation what is already done.
func transact(ctx context.Context, db DB, apply func(DB) error) error {
	settle := context.WithoutCancel(ctx)
	b, opens := db.(beginner)
	if !opens {
		return underSavepoint(ctx, settle, db, apply)
	}
	tx, err := b.BeginTx(settle, nil)
	if err != nil {
		return parse.StatementFailed("begin", err)
	}
	err = apply(tx)
	if err != nil {
		// A transaction database/sql already ended (a bad connection) kept
		// nothing to roll back.
		rollback := tx.Rollback()
		if !errors.Is(rollback, sql.ErrTxDone) {
			err = errors.Join(err, parse.StatementFailed("rollback", rollback))
		}
		return err
	}
	return parse.StatementFailed("commit", tx.Commit())
}

// underSavepoint is transact in the caller's transaction db: apply runs
// after a savepoint, released once apply succeeds and rolled back to
// otherwise, settling under settle.
func underSavepoint(ctx, settle context.Context, db DB, apply func(DB) error) error {
	_, err := db.ExecContext(ctx, "SAVEPOINT "+savepoint)
	if err != nil {
		return parse.StatementFailed("savepoint", err)
	}
	err = apply(db)
	if err != nil {
		return errors.Join(err, undoSavepoint(settle, db))
	}
	_, err = db.ExecContext(settle, "RELEASE SAVEPOINT "+savepoint)
	if err != nil {
		return errors.Join(parse.StatementFailed("release savepoint", err), undoSavepoint(settle, db))
	}
	return nil
}

// undoSavepoint rolls the caller's transaction back to the update's
// savepoint and releases it, so the transaction keeps nothing the update
// wrote and stays usable.
func undoSavepoint(ctx context.Context, db DB) error {
	_, rollback := db.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
	_, release := db.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
	return parse.StatementFailed("rollback to savepoint", errors.Join(rollback, release))
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
