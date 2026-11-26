package parse

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// An executor runs a plan's statements on its database and hands what they
// read back here; the steps below hold the sequence every executor follows,
// so a backend supplies only how it sends statements, scans rows and holds
// its transaction. CheckPlan comes first, before any statement is sent;
// each later step takes the rows the statements before it read and returns
// the result so far and the statements to run next, in order.

// ErrDialect is a plan an executor refuses because it is compiled for a
// dialect other than the executor's: its placeholders and expressions are
// not its database's.
var ErrDialect = errors.New("plan compiled for another dialect")

// Plan is a plan an executor runs: a *ListPlan, an *UpdatePlan or an
// *UpdateBatchPlan, and nothing else, so CheckPlan judges only plans a
// resource built.
type Plan interface {
	header() planHeader
}

// planHeader is what CheckPlan reads of a plan.
type planHeader struct {
	kind    string
	missing bool
	table   string
	dialect Dialect
}

func (p *ListPlan) header() planHeader {
	h := planHeader{kind: "list", missing: p == nil}
	if p != nil {
		h.table, h.dialect = p.table, p.dialect
	}
	return h
}

func (p *UpdatePlan) header() planHeader {
	h := planHeader{kind: "update", missing: p == nil}
	if p != nil {
		h.table, h.dialect = p.table, p.dialect
	}
	return h
}

func (p *UpdateBatchPlan) header() planHeader {
	h := planHeader{kind: "update batch", missing: p == nil}
	if p != nil {
		h.table, h.dialect = p.table, p.dialect
	}
	return h
}

// CheckPlan refuses a plan an executor for dialect d cannot run with r: a
// nil plan, one another resource built, or one compiled for another dialect
// (ErrDialect). Call it before sending any statement, so a plan that cannot
// run sends none.
func (r *Resource[T]) CheckPlan(plan Plan, d Dialect) error {
	if plan == nil {
		return fmt.Errorf("parse: %s: nil plan", r.table)
	}
	h := plan.header()
	err := r.owns(h)
	if err != nil {
		return err
	}
	if h.dialect == nil || d == nil || h.dialect.Name() != d.Name() {
		return fmt.Errorf("parse: %s: %s plan for %s, run on %s: %w", r.table, h.kind, dialectName(h.dialect), dialectName(d), ErrDialect)
	}
	return nil
}

// owns refuses a plan r did not build: its statements name another table's
// columns.
func (r *Resource[T]) owns(h planHeader) error {
	if h.missing {
		return fmt.Errorf("parse: %s: nil %s plan", r.table, h.kind)
	}
	if h.table != r.table {
		return fmt.Errorf("parse: %s: %s plan built for %q", r.table, h.kind, h.table)
	}
	return nil
}

func dialectName(d Dialect) string {
	if d == nil {
		return "no dialect"
	}
	return d.Name()
}

// StatementError is the failure of one statement an executor ran for a
// plan, as the database or the scan of its rows reported it. Statement
// names it: the plan's page, count, facets, current, conflict, owner,
// relations or reread statement; include <field> for an include; or a step
// of the transaction an update runs in: begin, savepoint, release
// savepoint, rollback to savepoint, rollback or commit. Every executor
// names its statements so, for a log attribute or a metric label.
type StatementError struct {
	Statement string
	Err       error
}

func (e *StatementError) Error() string { return e.Statement + ": " + e.Err.Error() }

func (e *StatementError) Unwrap() error { return e.Err }

// StatementFailed is err as statement's failure, a *StatementError, or nil
// when err is nil, so an executor wraps what running or scanning a
// statement returned as it returns it.
func StatementFailed(statement string, err error) error {
	if err == nil {
		return nil
	}
	failed := &StatementError{Statement: statement, Err: err}
	return failed
}

// ListRows is what an executor read for a list plan before its includes:
// the rows of plan.Page in query order, the value of plan.Count (read only
// when the plan counts) and the rows of plan.Facets (filter.ScanFacetUnion,
// none when the plan has no facets).
type ListRows[T any] struct {
	Items  []T
	Total  int64
	Facets []filter.FacetResult
}

// ListPage builds the page plan's statements read (Page), with its total
// when the plan counts and its facet counts, and returns the include
// statements for its rows' keys (IncludeQueries). Run each include, read
// its rows with its Scan, and attach them with page.SetInclude(q.Field, …).
// No include statement is returned when the plan includes nothing or the
// page holds no row.
func (r *Resource[T]) ListPage(plan *ListPlan, read ListRows[T]) (*filter.Page[T], []filter.IncludeQuery, error) {
	page, err := r.Page(read.Items, plan)
	if err != nil {
		return nil, nil, err
	}
	if plan.Count != nil {
		total := read.Total
		page.PageInfo.TotalCount = &total
	}
	page.Facets = filter.FacetMap(read.Facets)
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

// UpdateFollowUp is what an update runs once its owner statement has
// returned, in the update's transaction.
type UpdateFollowUp[T any] struct {
	// Result splits the target rows as Owner, and Current when the plan
	// has it, left them (UpdatedRows). Its RelationRows is zero until the
	// relation statements have run.
	Result UpdateResult[T]
	// Relations change the memberships of Result's rows, the changed then
	// the unchanged ones (RelationStatements). Run them in order and add
	// the rows each affects to Result.RelationRows.
	Relations []patch.RenderedRelationStatement
	// RereadChanged and RereadUnchanged, set for a resource with computed
	// columns when the plan changes relations (Reread), read Result.Changed
	// and Result.Unchanged again once Relations have run; the rows each
	// returns replace that side of Result. They read the two apart, so
	// each row keeps its side of the split.
	RereadChanged, RereadUnchanged *Statement
}

// FollowUp splits the target rows plan's owner statement returned (owner)
// and, for a plan with Current, the rows Current read (current), and
// renders what the update runs next: its relation statements and rereads,
// for the keys of the rows the owner statement returned, never the
// requested keys. A plan that changes no relation runs nothing more.
func (r *Resource[T]) FollowUp(plan *UpdatePlan, owner, current []T) (*UpdateFollowUp[T], error) {
	changed, unchanged, err := r.UpdatedRows(plan, owner, current)
	if err != nil {
		return nil, err
	}
	follow := &UpdateFollowUp[T]{Result: UpdateResult[T]{Changed: changed, Unchanged: unchanged}}
	if !plan.HasRelations() {
		return follow, nil
	}
	keys, err := r.keys(slices.Concat(changed, unchanged))
	if err != nil {
		return nil, err
	}
	follow.Relations, err = r.RelationStatements(plan, keys)
	if err != nil {
		return nil, err
	}
	follow.RereadChanged, err = r.Reread(plan, keys[:len(changed)])
	if err != nil {
		return nil, err
	}
	follow.RereadUnchanged, err = r.Reread(plan, keys[len(changed):])
	if err != nil {
		return nil, err
	}
	return follow, nil
}

// CheckBatchCurrent checks the rows each chunk's Current read (current, in
// chunk order) against the chunk's keys (UpdateBatchChunk.CheckCurrent):
// ErrUpdateBatchTargets when a chunk's rows are not exactly its keys. Run
// every chunk's Current, then this, and send no Owner when it fails, so a
// batch with a missing target writes nothing.
func (r *Resource[T]) CheckBatchCurrent(plan *UpdateBatchPlan, current [][]T) error {
	err := r.batchReads(plan, current, "Current")
	if err != nil {
		return err
	}
	for i, chunk := range plan.Chunks {
		keys, err := r.keys(current[i])
		if err != nil {
			return err
		}
		err = chunk.CheckCurrent(keys)
		if err != nil {
			return err
		}
	}
	return nil
}

// BatchResult is the result of a batch whose chunks' Current read current
// and whose Owners returned owner, both in chunk order (nil for a chunk
// without an Owner): Changed are the rows the owners wrote, chunk by chunk,
// and Unchanged the rows a chunk's Current read and its Owner left.
func (r *Resource[T]) BatchResult(plan *UpdateBatchPlan, current, owner [][]T) (UpdateResult[T], error) {
	err := r.batchReads(plan, current, "Current")
	if err != nil {
		return UpdateResult[T]{}, err
	}
	err = r.batchReads(plan, owner, "Owner")
	if err != nil {
		return UpdateResult[T]{}, err
	}
	var res UpdateResult[T]
	for i := range plan.Chunks {
		unchanged, err := r.Unchanged(owner[i], current[i])
		if err != nil {
			return UpdateResult[T]{}, err
		}
		res.Changed = append(res.Changed, owner[i]...)
		res.Unchanged = append(res.Unchanged, unchanged...)
	}
	return res, nil
}

// batchReads refuses a plan r did not build, and reads of another number of
// chunks than plan holds: an executor that lost a chunk's rows would report
// its targets missing or unchanged.
func (r *Resource[T]) batchReads(plan *UpdateBatchPlan, reads [][]T, statement string) error {
	err := r.owns(plan.header())
	if err != nil {
		return err
	}
	if len(reads) != len(plan.Chunks) {
		return fmt.Errorf("parse: %s: %s rows of %d chunks for a batch of %d", r.table, statement, len(reads), len(plan.Chunks))
	}
	return nil
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
