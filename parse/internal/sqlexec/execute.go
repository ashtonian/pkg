package sqlexec

import (
	"context"
	"errors"
	"slices"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/sqlplan"
)

// The sequence below is every SQL provider's: a request is compiled for
// the provider's dialect (internal/sqlplan), its statements are sent in
// the steps below, and their rows go to the request's sink, which scans
// them and builds the result.

// List sends the page, count and facet statements together, then, once the
// page's keys are known (backend.ListSink.Keys), the includes. Each
// statement runs in its own snapshot unless the executor runs in a
// transaction that holds one (on PostgreSQL at REPEATABLE READ or
// stricter).
func (e executor) List(ctx context.Context, req backend.ListRequest, sink backend.ListSink) error {
	plan, err := sqlplan.CompileList(e.dialect, req)
	if err != nil {
		return err
	}
	table := req.Resource.Table
	steps := []Step{rowsStep("page", plan.Page, sink.Page)}
	if plan.Count != nil {
		steps = append(steps, Step{Name: "count", SQL: plan.Count.SQL, Args: plan.Count.Args, Read: func(rows backend.Rows) error {
			var total int64
			err := readOne(rows, &total)
			if err != nil {
				return err
			}
			sink.Count(total)
			return nil
		}})
	}
	if plan.Facets != nil {
		steps = append(steps, facetStep(*plan.Facets, sink.Facets))
	}
	err = e.conn.Send(ctx, table, steps...)
	if err != nil {
		return err
	}
	if len(req.Include) == 0 {
		return nil
	}
	owners, err := sink.Keys()
	if err != nil {
		return err
	}
	includes, err := sqlplan.Includes(e.dialect, req, owners)
	if err != nil || len(includes) == 0 {
		return err
	}
	steps = make([]Step, len(includes))
	for i, q := range includes {
		steps[i] = Step{Name: "include " + q.Field, SQL: q.SQL, Args: q.Args, Read: func(rows backend.Rows) error {
			res, err := q.Scan(rows)
			if err != nil {
				return err
			}
			sink.Include(q.Field, res)
			return nil
		}}
	}
	return e.conn.Send(ctx, table, steps...)
}

// Facets sends the facet statement the list compiles to; a request naming
// no facets compiles to none and sends nothing.
func (e executor) Facets(ctx context.Context, req backend.ListRequest) ([]filter.FacetResult, error) {
	plan, err := sqlplan.CompileList(e.dialect, req)
	if err != nil || plan.Facets == nil {
		return nil, err
	}
	var results []filter.FacetResult
	err = e.conn.Send(ctx, req.Resource.Table, facetStep(*plan.Facets, func(read []filter.FacetResult) { results = read }))
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (e executor) Query(ctx context.Context, req backend.QueryRequest, sink backend.QuerySink) error {
	return e.conn.Send(ctx, req.Resource.Table, rowsStep("query", sqlplan.Statement{SQL: req.SQL, Args: req.Args}, sink.Rows))
}

// Update runs the update in one transaction, or under a savepoint of the
// one the executor runs in. Its statements run in order. Current goes
// first when the plan has it (SkipUnchanged, or a patch of document
// members), locking every target; then Conflict, whose row fails the update
// before anything is written (E2077); then Lock, which takes a multi-row
// target's rows in key order; then Owner. A patch that changes relation
// memberships changes them for the keys of the rows Owner returned
// (changed, then unchanged), never the requested keys, then, for a
// resource with computed columns, reads those rows again, so a column over
// a relation's pairs reads them as the update left them.
func (e executor) Update(ctx context.Context, req backend.UpdateRequest, sink backend.UpdateSink) error {
	plan, err := sqlplan.CompileUpdate(e.dialect, req)
	if err != nil {
		return err
	}
	table := req.Resource.Table
	return e.conn.Transact(ctx, table, e.settle, func(tx Conn) error {
		err := updateTargets(ctx, tx, table, plan, sink)
		if err != nil {
			return err
		}
		err = sink.Split()
		if err != nil || !plan.HasRelations() {
			return err
		}
		steps, err := e.relationSteps(req, plan, sink)
		if err != nil || len(steps) == 0 {
			return err
		}
		return tx.Send(ctx, table, steps...)
	})
}

// updateTargets runs plan's statements up to its owner statement in tx.
// Current goes first, in the same send as Owner unless a Conflict comes
// between: its lock holds every target while the guarded update compares
// them, so the rows it read and the update did not return are exactly the
// unchanged ones. A Conflict row fails the update before Owner is sent.
func updateTargets(ctx context.Context, tx Conn, table string, plan *sqlplan.Update, sink backend.UpdateSink) error {
	var steps []Step
	if plan.Current != nil {
		steps = append(steps, rowsStep("current", *plan.Current, sink.Targets))
	}
	if plan.Conflict != nil {
		var unmet *int64
		steps = append(steps, Step{Name: "conflict", SQL: plan.Conflict.SQL, Args: plan.Conflict.Args, Read: func(rows backend.Rows) error {
			if rows.Next() {
				var op int64
				err := rows.Scan(&op)
				if err != nil {
					return err
				}
				unmet = &op
			}
			return rows.Err()
		}})
		err := tx.Send(ctx, table, steps...)
		if err != nil {
			return err
		}
		if unmet != nil {
			return plan.ConflictError(*unmet)
		}
		steps = nil
	}
	if plan.Lock != nil {
		steps = append(steps, Step{Name: "lock", SQL: plan.Lock.SQL, Args: plan.Lock.Args, Read: func(rows backend.Rows) error {
			return rows.Err()
		}})
	}
	// The rows an UPDATE returns are the ones it wrote; a SELECT's are the
	// targets it left.
	owner := sink.Unchanged
	if plan.Assigns {
		owner = sink.Changed
	}
	steps = append(steps, rowsStep("owner", plan.Owner, owner))
	return tx.Send(ctx, table, steps...)
}

// relationSteps are what an update that changes relations sends once its
// targets are split: the membership changes for the keys of the rows the
// owner statement returned, then the rereads of the changed and the
// unchanged rows apart, so each row keeps its side of the split. No
// membership change sends nothing, rereads included.
func (e executor) relationSteps(req backend.UpdateRequest, plan *sqlplan.Update, sink backend.UpdateSink) ([]Step, error) {
	changed, unchanged, err := sink.Keys()
	if err != nil {
		return nil, err
	}
	relations, err := sqlplan.Relations(e.dialect, req, plan, slices.Concat(changed, unchanged))
	if err != nil {
		return nil, err
	}
	rereadChanged, err := sqlplan.Reread(e.dialect, req, plan, changed)
	if err != nil {
		return nil, err
	}
	rereadUnchanged, err := sqlplan.Reread(e.dialect, req, plan, unchanged)
	if err != nil {
		return nil, err
	}
	if len(relations) == 0 {
		return nil, nil
	}
	steps := make([]Step, 0, len(relations)+2)
	for _, stmt := range relations {
		steps = append(steps, Step{Name: "relations", SQL: stmt.SQL, Args: stmt.Args, Changed: sink.RelationRows})
	}
	if rereadChanged != nil {
		steps = append(steps, rowsStep("reread", *rereadChanged, sink.Changed))
	}
	if rereadUnchanged != nil {
		steps = append(steps, rowsStep("reread", *rereadUnchanged, sink.Unchanged))
	}
	return steps, nil
}

// UpdateBatch runs the batch in one transaction, held as Update holds its
// own. Every chunk's Current goes first, locking its targets in key order,
// and each chunk's rows must be exactly its keys (backend.BatchSink.Check),
// or nothing is written; every Owner then goes. A batch of no rows sends
// nothing.
func (e executor) UpdateBatch(ctx context.Context, req backend.UpdateBatchRequest, sink backend.BatchSink) error {
	plan, err := sqlplan.CompileBatch(e.dialect, req)
	if err != nil || len(plan.Chunks) == 0 {
		return err
	}
	table := req.Resource.Table
	return e.conn.Transact(ctx, table, e.settle, func(tx Conn) error {
		steps := make([]Step, len(plan.Chunks))
		for i, chunk := range plan.Chunks {
			keys := chunk.Keys
			steps[i] = rowsStep("current", chunk.Current, func(rows backend.Rows) error { return sink.Targets(keys, rows) })
		}
		err := tx.Send(ctx, table, steps...)
		if err != nil {
			return err
		}
		err = sink.Check()
		if err != nil {
			return err
		}
		steps = make([]Step, 0, len(plan.Chunks))
		for i, chunk := range plan.Chunks {
			if chunk.Owner != nil {
				steps = append(steps, rowsStep("owner", *chunk.Owner, func(rows backend.Rows) error { return sink.Changed(i, rows) }))
			}
		}
		if len(steps) > 0 {
			err := tx.Send(ctx, table, steps...)
			if err != nil {
				return err
			}
		}
		return sink.Split()
	})
}

// errNoRow is a statement that returns one row returning none.
var errNoRow = errors.New("no row")

// readOne reads a statement's single row into dest.
func readOne(rows backend.Rows, dest ...any) error {
	if !rows.Next() {
		err := rows.Err()
		if err != nil {
			return err
		}
		return errNoRow
	}
	return rows.Scan(dest...)
}

// rowsStep runs stmt, a statement over the resource's Columns, and hands
// its rows to read, a sink's.
func rowsStep(name string, stmt sqlplan.Statement, read func(backend.Rows) error) Step {
	step := Step{Name: name, SQL: stmt.SQL, Args: stmt.Args, Read: read}
	return step
}

// facetStep runs q and hands its counts to keep (filter.ScanFacetUnion).
func facetStep(q filter.FacetQuery, keep func([]filter.FacetResult)) Step {
	step := Step{Name: "facets", SQL: q.SQL, Args: q.Args, Read: func(rows backend.Rows) error {
		results, err := filter.ScanFacetUnion(rows)
		if err != nil {
			return err
		}
		keep(results)
		return nil
	}}
	return step
}
