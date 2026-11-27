package parse

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// A resource runs its statements on its provider: it compiles them for the
// provider's dialect, hands them to the provider's database in the steps
// below, scans their rows into T and builds the result. The sequence is
// every provider's; a provider package supplies only how statements are
// sent, how their rows are read and how a transaction is held.

// Provider is the provider the resource was built on (NewResource).
func (r *Resource[T]) Provider() *Provider { return r.provider }

// In is r scoped to tx: its List, Facets, Query, Update and UpdateBatch run
// in tx, so several operations, on several resources, commit or roll back
// together, with the application's own SQL in the same transaction
// (parsesql.Tx, parsepgx.Tx). An update in tx runs under a savepoint: one
// that fails leaves tx as it was and usable. The scoped resource is a view
// (as WithPaths is), a value for the transaction's life: it parses and
// builds as r does, declares no relation and is no relation's target. A
// transaction another provider began (Provider.Begin), or a wrapped one of
// another kind of provider, is ErrProviderMismatch; a wrapped transaction
// of the right kind must be on r's database, which its driver cannot tell.
func (r *Resource[T]) In(tx *Tx) (*Resource[T], error) {
	if tx == nil {
		return nil, fmt.Errorf("parse: %s: nil transaction", r.table)
	}
	if tx.provider != r.provider.name {
		return nil, fmt.Errorf("parse: %s: %s transaction for a resource on %s: %w", r.table, tx.provider, r.provider.name, ErrProviderMismatch)
	}
	if tx.began != nil && tx.began != r.provider {
		return nil, fmt.Errorf("parse: %s: transaction another %s provider began: %w", r.table, tx.provider, ErrProviderMismatch)
	}
	scoped := *r
	scoped.conn, scoped.view = tx.tx, true
	return &scoped, nil
}

// List compiles req under scope s (BuildList) and reads the page on the
// provider's database: its rows, its total when the request asks for it,
// its facet counts, and the related keys of its rows for each relation it
// includes. The page, count and facet statements are sent together, and
// the includes, which need the page's keys, after them. Each statement runs
// in its own snapshot unless the resource runs in a transaction that holds
// one (In, on PostgreSQL at REPEATABLE READ or stricter); hold one when the
// total and facets must describe exactly the page's rows under concurrent
// writes.
func (r *Resource[T]) List(ctx context.Context, req *ListRequest, s Scope) (*filter.Page[T], error) {
	plan, err := r.BuildList(req, s)
	if err != nil {
		return nil, err
	}
	var read listRows[T]
	steps := []backend.Step{r.rowsStep("page", plan.Page, &read.items)}
	if plan.Count != nil {
		steps = append(steps, backend.Step{Name: "count", SQL: plan.Count.SQL, Args: plan.Count.Args, Read: func(rows backend.Rows) error {
			return readOne(rows, &read.total)
		}})
	}
	if plan.Facets != nil {
		steps = append(steps, facetStep(*plan.Facets, &read.facets))
	}
	err = r.conn.Send(ctx, r.table, steps...)
	if err != nil {
		return nil, err
	}
	page, includes, err := r.listPage(plan, read)
	if err != nil {
		return nil, err
	}
	if len(includes) == 0 {
		return page, nil
	}
	steps = make([]backend.Step, len(includes))
	for i, q := range includes {
		steps[i] = backend.Step{Name: "include " + q.Field, SQL: q.SQL, Args: q.Args, Read: func(rows backend.Rows) error {
			res, err := q.Scan(rows)
			if err != nil {
				return err
			}
			page.SetInclude(q.Field, res)
			return nil
		}}
	}
	err = r.conn.Send(ctx, r.table, steps...)
	if err != nil {
		return nil, err
	}
	return page, nil
}

// Facets compiles a facets request (ParseFacets) under scope s
// (BuildFacets) and counts the rows it matches by each facet it names; nil
// when it names none.
func (r *Resource[T]) Facets(ctx context.Context, req *ListRequest, s Scope) (map[string][]filter.FacetBucket, error) {
	query, err := r.BuildFacets(req, s)
	if err != nil || query == nil {
		return nil, err
	}
	var results []filter.FacetResult
	err = r.conn.Send(ctx, r.table, facetStep(*query, &results))
	if err != nil {
		return nil, err
	}
	return filter.FacetMap(results), nil
}

// Query runs st, a statement of the application's own over the resource's
// table that selects Columns, in the provider's dialect, and returns its
// rows scanned through the row contract (RowScanner). A row that fails to
// scan yields no rows at all.
func (r *Resource[T]) Query(ctx context.Context, st Statement) ([]T, error) {
	var rows []T
	err := r.conn.Send(ctx, r.table, r.rowsStep("query", st, &rows))
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// Update compiles p for target under scope s (BuildUpdate), applies it, and
// reports the target rows as the update left them: split into the rows
// whose columns it wrote and the rows it left, with how many junction rows
// its membership changes touched. Neither split holding a row means the
// target is not found.
//
// The update runs in one transaction, or under a savepoint of the one the
// resource is scoped to (In), settled only once every row has scanned: an
// error means nothing it wrote is kept, unless errors.Is(err,
// ErrCommitUnknown). Its statements run in order. Current goes first when
// the plan has it (SkipUnchanged, or a patch of document members), locking
// every target; then Conflict, whose row fails the update before anything
// is written (E2077); then Lock, which takes a multi-row target's rows in
// key order; then Owner. A patch that changes relation memberships changes
// them for the keys of the rows Owner returned (changed, then unchanged),
// never the requested keys, then, for a resource with computed columns
// (ReadExpr), reads those rows again, so a column over a relation's pairs
// reads them as the update left them.
func (r *Resource[T]) Update(ctx context.Context, p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (UpdateResult[T], error) {
	plan, err := r.BuildUpdate(p, target, s, opts...)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	var res UpdateResult[T]
	err = r.conn.Transact(ctx, r.table, r.provider.settle, func(tx backend.Conn) error {
		follow, err := r.updateTargets(ctx, tx, plan)
		if err != nil {
			return err
		}
		res = follow.result
		if len(follow.relations) == 0 {
			return nil
		}
		// The membership changes and the rereads after them go together.
		steps := make([]backend.Step, 0, len(follow.relations)+2)
		for _, stmt := range follow.relations {
			steps = append(steps, backend.Step{Name: "relations", SQL: stmt.SQL, Args: stmt.Args, Changed: func(n int64) {
				res.RelationRows += n
			}})
		}
		if follow.rereadChanged != nil {
			steps = append(steps, r.rowsStep("reread", *follow.rereadChanged, &res.Changed))
		}
		if follow.rereadUnchanged != nil {
			steps = append(steps, r.rowsStep("reread", *follow.rereadUnchanged, &res.Unchanged))
		}
		return tx.Send(ctx, r.table, steps...)
	})
	if err != nil {
		return UpdateResult[T]{}, err
	}
	return res, nil
}

// UpdateBatch compiles rows under scope s within limits
// (BuildUpdateBatch) and applies them in one transaction, held as Update
// holds its own. Every chunk's Current goes first, locking its targets in
// key order, and each chunk's rows must be exactly its keys
// (ErrUpdateBatchTargets otherwise, and nothing is written); every Owner
// then goes. The result holds as Changed the rows the owners wrote, chunk
// by chunk, and as Unchanged the locked rows a SkipUnchanged owner left as
// they were.
func (r *Resource[T]) UpdateBatch(ctx context.Context, rows []RowUpdate, s Scope, limits UpdateBatchLimits, opts ...UpdateOption) (UpdateResult[T], error) {
	plan, err := r.BuildUpdateBatch(rows, s, limits, opts...)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	if len(plan.Chunks) == 0 {
		return UpdateResult[T]{}, nil
	}
	var res UpdateResult[T]
	err = r.conn.Transact(ctx, r.table, r.provider.settle, func(tx backend.Conn) error {
		current := make([][]T, len(plan.Chunks))
		steps := make([]backend.Step, len(plan.Chunks))
		for i, chunk := range plan.Chunks {
			steps[i] = r.rowsStep("current", chunk.Current, &current[i])
		}
		err := tx.Send(ctx, r.table, steps...)
		if err != nil {
			return err
		}
		err = r.checkBatchCurrent(plan, current)
		if err != nil {
			return err
		}
		owner := make([][]T, len(plan.Chunks))
		steps = make([]backend.Step, 0, len(plan.Chunks))
		for i, chunk := range plan.Chunks {
			if chunk.Owner != nil {
				steps = append(steps, r.rowsStep("owner", *chunk.Owner, &owner[i]))
			}
		}
		if len(steps) > 0 {
			err := tx.Send(ctx, r.table, steps...)
			if err != nil {
				return err
			}
		}
		res, err = r.batchResult(plan, current, owner)
		return err
	})
	if err != nil {
		return UpdateResult[T]{}, err
	}
	return res, nil
}

// StatementError is the failure of one statement a resource ran on its
// provider, as the database or the scan of its rows reported it. Statement
// names it: a list's page, count, facets or include <field>; a query; an
// update's current, conflict, lock, owner, relations or reread; or a step
// of the transaction an update runs in: begin, savepoint, release
// savepoint, rollback to savepoint, rollback or commit. Every provider
// names its statements so, for a log attribute or a metric label.
type StatementError struct {
	// Table is the table of the resource whose statement it is; empty for
	// a transaction's own begin, commit or rollback (Provider.Begin, Tx).
	Table     string
	Statement string
	Err       error
}

func (e *StatementError) Error() string {
	if e.Table == "" {
		return e.Statement + ": " + e.Err.Error()
	}
	return "parse: " + e.Table + ": " + e.Statement + ": " + e.Err.Error()
}

func (e *StatementError) Unwrap() error { return e.Err }

// StatementFailed is err as the failure of statement, one of table's
// resource ("" for a transaction's own), a *StatementError, or nil when err
// is nil, for parse's provider packages, which report each statement's
// failure so.
func StatementFailed(table, statement string, err error) error {
	if err == nil {
		return nil
	}
	failed := &StatementError{Table: table, Statement: statement, Err: err}
	return failed
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

// rowsStep runs stmt, a statement over Columns, and scans its rows into
// *into.
func (r *Resource[T]) rowsStep(name string, stmt Statement, into *[]T) backend.Step {
	step := backend.Step{Name: name, SQL: stmt.SQL, Args: stmt.Args, Read: func(rows backend.Rows) error {
		items, err := r.scanRows(rows)
		*into = items
		return err
	}}
	return step
}

// facetStep runs q and reads its counts into *into (filter.ScanFacetUnion).
func facetStep(q filter.FacetQuery, into *[]filter.FacetResult) backend.Step {
	step := backend.Step{Name: "facets", SQL: q.SQL, Args: q.Args, Read: func(rows backend.Rows) error {
		results, err := filter.ScanFacetUnion(rows)
		*into = results
		return err
	}}
	return step
}

// scanRows scans rows, a statement's over Columns, through the row
// contract. A provider that reports which of the result's columns are
// documents (backend.Rows.Documents) does so once the first row is read;
// they decode as stored documents, so the scanner is built for them then.
func (r *Resource[T]) scanRows(rows backend.Rows) ([]T, error) {
	var (
		out     []T
		scanner *RowScanner[T]
	)
	for rows.Next() {
		if scanner == nil {
			var err error
			scanner, err = r.scannerFor(rows.Documents())
			if err != nil {
				return nil, err
			}
		}
		item, err := scanner.scan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	err := rows.Err()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scannerFor is the scanner for a result whose documents columns the
// database reports as JSON documents: the resource's own when there are
// none. Its errors, as scan's, are a statement's, which name the table.
func (r *Resource[T]) scannerFor(documents []string) (*RowScanner[T], error) {
	if len(documents) == 0 {
		return r.scanner, nil
	}
	return r.rowScanner(ScanJSON(documents...))
}

// listRows is what a list's page, count and facet statements read: the
// page's rows in query order, the count (read only when the plan counts)
// and the facet counts.
type listRows[T any] struct {
	items  []T
	total  int64
	facets []filter.FacetResult
}

// listPage builds the page plan's statements read (Page), with its total
// when the plan counts and its facet counts, and returns the include
// statements for its rows' keys (IncludeQueries): none when the plan
// includes nothing or the page holds no row.
func (r *Resource[T]) listPage(plan *ListPlan, read listRows[T]) (*filter.Page[T], []filter.IncludeQuery, error) {
	page, err := r.Page(read.items, plan)
	if err != nil {
		return nil, nil, err
	}
	if plan.Count != nil {
		total := read.total
		page.PageInfo.TotalCount = &total
	}
	page.Facets = filter.FacetMap(read.facets)
	if len(plan.Include) == 0 || len(page.Items) == 0 {
		return page, nil, nil
	}
	keys, err := r.keys(page.Items)
	if err != nil {
		return nil, nil, err
	}
	queries, err := r.IncludeQueries(plan, keys)
	if err != nil {
		return nil, nil, err
	}
	return page, queries, nil
}

// updateFollowUp is what an update runs once its owner statement has
// returned, in the update's transaction.
type updateFollowUp[T any] struct {
	// result splits the target rows as Owner, and Current when the plan
	// has it, left them (updatedRows). Its RelationRows is zero until the
	// relation statements have run.
	result UpdateResult[T]
	// relations change the memberships of result's rows, the changed then
	// the unchanged ones (relationStatements).
	relations []patch.RenderedRelationStatement
	// rereadChanged and rereadUnchanged, set for a resource with computed
	// columns when the plan changes relations (rereadStatement), read
	// result.Changed and result.Unchanged again once relations have run;
	// the rows each returns replace that side of result. They read the two
	// apart, so each row keeps its side of the split.
	rereadChanged, rereadUnchanged *Statement
}

// updateTargets runs plan's statements up to its owner statement in tx and
// returns what the update runs next (followUp). Current goes first, in the
// same send as Owner unless a Conflict comes between: its lock holds every
// target while the guarded update compares them, so the rows it read and
// the update did not return are exactly the unchanged ones. A Conflict row
// fails the update before Owner is sent.
func (r *Resource[T]) updateTargets(ctx context.Context, tx backend.Conn, plan *UpdatePlan) (*updateFollowUp[T], error) {
	var current, owner []T
	var steps []backend.Step
	if plan.Current != nil {
		steps = append(steps, r.rowsStep("current", *plan.Current, &current))
	}
	if plan.Conflict != nil {
		var unmet *int64
		steps = append(steps, backend.Step{Name: "conflict", SQL: plan.Conflict.SQL, Args: plan.Conflict.Args, Read: func(rows backend.Rows) error {
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
		err := tx.Send(ctx, r.table, steps...)
		if err != nil {
			return nil, err
		}
		if unmet != nil {
			return nil, plan.conflictError(*unmet)
		}
		steps = nil
	}
	if plan.Lock != nil {
		steps = append(steps, backend.Step{Name: "lock", SQL: plan.Lock.SQL, Args: plan.Lock.Args, Read: func(rows backend.Rows) error {
			return rows.Err()
		}})
	}
	steps = append(steps, r.rowsStep("owner", plan.Owner, &owner))
	err := tx.Send(ctx, r.table, steps...)
	if err != nil {
		return nil, err
	}
	return r.followUp(plan, owner, current)
}

// followUp splits the target rows plan's owner statement returned (owner)
// and, for a plan with Current, the rows Current read (current), and
// renders what the update runs next: its relation statements and rereads,
// for the keys of the rows the owner statement returned, never the
// requested keys. A plan that changes no relation runs nothing more.
func (r *Resource[T]) followUp(plan *UpdatePlan, owner, current []T) (*updateFollowUp[T], error) {
	changed, unchanged, err := r.updatedRows(plan, owner, current)
	if err != nil {
		return nil, err
	}
	follow := &updateFollowUp[T]{result: UpdateResult[T]{Changed: changed, Unchanged: unchanged}}
	if !plan.HasRelations() {
		return follow, nil
	}
	keys, err := r.keys(slices.Concat(changed, unchanged))
	if err != nil {
		return nil, err
	}
	follow.relations, err = r.relationStatements(plan, keys)
	if err != nil {
		return nil, err
	}
	follow.rereadChanged, err = r.rereadStatement(plan, keys[:len(changed)])
	if err != nil {
		return nil, err
	}
	follow.rereadUnchanged, err = r.rereadStatement(plan, keys[len(changed):])
	if err != nil {
		return nil, err
	}
	return follow, nil
}

// checkBatchCurrent checks the rows each chunk's Current read (current, in
// chunk order) against the chunk's keys (UpdateBatchChunk.checkCurrent):
// ErrUpdateBatchTargets when a chunk's rows are not exactly its keys, and
// no Owner is sent, so a batch with a missing target writes nothing.
func (r *Resource[T]) checkBatchCurrent(plan *UpdateBatchPlan, current [][]T) error {
	for i, chunk := range plan.Chunks {
		keys, err := r.keys(current[i])
		if err != nil {
			return err
		}
		err = chunk.checkCurrent(keys)
		if err != nil {
			return fmt.Errorf("parse: %s: %w", r.table, err)
		}
	}
	return nil
}

// batchResult is the result of a batch whose chunks' Current read current
// and whose Owners returned owner, both in chunk order (nil for a chunk
// without an Owner): Changed are the rows the owners wrote, chunk by chunk,
// and Unchanged the rows a chunk's Current read and its Owner left.
func (r *Resource[T]) batchResult(plan *UpdateBatchPlan, current, owner [][]T) (UpdateResult[T], error) {
	var res UpdateResult[T]
	for i := range plan.Chunks {
		unchanged, err := r.unchangedRows(owner[i], current[i])
		if err != nil {
			return UpdateResult[T]{}, err
		}
		res.Changed = append(res.Changed, owner[i]...)
		res.Unchanged = append(res.Unchanged, unchanged...)
	}
	return res, nil
}

// keys are the rows' keys (Key), in the rows' order.
func (r *Resource[T]) keys(items []T) ([]Key, error) {
	keys := make([]Key, len(items))
	for i, item := range items {
		k, err := r.Key(item)
		if err != nil {
			return nil, err
		}
		keys[i] = k
	}
	return keys, nil
}

// ownsPlan refuses a plan r did not build: its keyset and includes name
// another table's columns.
func (r *Resource[T]) ownsPlan(plan *ListPlan) error {
	if plan == nil {
		return fmt.Errorf("parse: %s: nil list plan", r.table)
	}
	if plan.table != r.table {
		return fmt.Errorf("parse: %s: list plan built for %q", r.table, plan.table)
	}
	return nil
}
