package parse

import (
	"context"
	"fmt"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// A resource composes each request it runs (composeList, composeUpdate,
// composeBatch), hands it to its provider's executor with a sink
// (sink.go), and builds the result from the rows the sink scanned into T.
// Which statements run, in which order and round trips, is the executor's:
// the SQL providers' is internal/sqlexec.

// Provider is the provider the resource was built on (NewResource).
func (r *resourceCore) Provider() *Provider { return r.provider }

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
	err := r.joins(tx)
	if err != nil {
		return nil, err
	}
	exec, err := r.provider.db.Join(tx.tx)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %s transaction: %w", r.table, tx.provider, err)
	}
	scoped := *r
	scoped.exec, scoped.view = exec, true
	return &scoped, nil
}

// joins refuses a transaction the resource cannot run in (In).
func (r *resourceCore) joins(tx *Tx) error {
	if tx == nil {
		return fmt.Errorf("parse: %s: nil transaction", r.table)
	}
	if tx.provider != r.provider.name {
		return fmt.Errorf("parse: %s: %s transaction for a resource on %s: %w", r.table, tx.provider, r.provider.name, ErrProviderMismatch)
	}
	if tx.began != nil && tx.began != r.provider {
		return fmt.Errorf("parse: %s: transaction another %s provider began: %w", r.table, tx.provider, ErrProviderMismatch)
	}
	return nil
}

// List composes req under scope s, as BuildList does, and reads the page on
// the provider's database: its rows, its total when the request asks for
// it, its facet counts, and the related keys of its rows for each relation
// it includes. On a SQL provider the page, count and facet statements are
// sent together, and the includes, which need the page's keys, after them.
// Each statement runs in its own snapshot unless the resource runs in a
// transaction that holds one (In, on PostgreSQL at REPEATABLE READ or
// stricter); hold one when the total and facets must describe exactly the
// page's rows under concurrent writes.
func (r *Resource[T]) List(ctx context.Context, req *ListRequest, s Scope) (*filter.Page[T], error) {
	list, err := r.composeList(req, s)
	if err != nil {
		return nil, err
	}
	sink := &listSink[T]{r: r, list: list}
	err = r.exec.List(ctx, list, sink)
	if err != nil {
		return nil, err
	}
	return sink.result()
}

// Facets composes a facets request (ParseFacets) under scope s, as
// BuildFacets does, and counts the rows it matches by each facet it names;
// nil when it names none.
func (r *resourceCore) Facets(ctx context.Context, req *ListRequest, s Scope) (map[string][]filter.FacetBucket, error) {
	list, err := r.composeList(req, s)
	if err != nil {
		return nil, err
	}
	results, err := r.exec.Facets(ctx, list)
	if err != nil {
		return nil, err
	}
	return filter.FacetMap(results), nil
}

// Query runs st, a statement of the application's own over the resource's
// table that selects Columns, in the provider's dialect, and returns its
// rows scanned through the row contract (RowScanner). A row that fails to
// scan yields no rows at all. A provider that runs no SQL refuses it
// (ErrUnsupported).
func (r *Resource[T]) Query(ctx context.Context, st Statement) ([]T, error) {
	err := r.provider.supports(r.table, backend.Query)
	if err != nil {
		return nil, err
	}
	sink := &querySink[T]{r: r}
	err = r.exec.Query(ctx, backend.QueryRequest{Resource: r.desc, SQL: st.SQL, Args: st.Args}, sink)
	if err != nil {
		return nil, err
	}
	return sink.rows, nil
}

// Update composes p for target under scope s, as BuildUpdate does, applies
// it, and reports the target rows as the update left them: split into the
// rows whose columns it wrote and the rows it left, with how many junction
// rows its membership changes touched. Neither split holding a row means
// the target is not found.
//
// The update runs in one transaction, or under a savepoint of the one the
// resource is scoped to (In), settled only once every row has scanned: an
// error means nothing it wrote is kept, unless errors.Is(err,
// ErrCommitUnknown). On a SQL provider its statements run in order.
// Current goes first when the plan has it (SkipUnchanged, or a patch of
// document members), locking every target; then Conflict, whose row fails
// the update before anything is written (E2077); then Lock, which takes a
// multi-row target's rows in key order; then Owner. A patch that changes
// relation memberships changes them for the keys of the rows Owner returned
// (changed, then unchanged), never the requested keys, then, for a resource
// with computed columns (ReadExpr), reads those rows again, so a column
// over a relation's pairs reads them as the update left them.
func (r *Resource[T]) Update(ctx context.Context, p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (UpdateResult[T], error) {
	req, err := r.composeUpdate(p, target, s, opts)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	sink := &updateSink[T]{r: r}
	err = r.exec.Update(ctx, req, sink)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	return sink.result(), nil
}

// UpdateBatch composes rows under scope s within limits, as
// BuildUpdateBatch does, and applies them in one transaction, held as
// Update holds its own. On a SQL provider every chunk's Current goes first,
// locking its targets in key order, and each chunk's rows must be exactly
// its keys (ErrUpdateBatchTargets otherwise, and nothing is written); every
// Owner then goes. The result holds as Changed the rows the owners wrote,
// chunk by chunk, and as Unchanged the locked rows a SkipUnchanged owner
// left as they were.
func (r *Resource[T]) UpdateBatch(ctx context.Context, rows []RowUpdate, s Scope, limits UpdateBatchLimits, opts ...UpdateOption) (UpdateResult[T], error) {
	req, err := r.composeBatch(rows, s, limits, opts)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	sink := &batchSink[T]{r: r}
	err = r.exec.UpdateBatch(ctx, req, sink)
	if err != nil {
		return UpdateResult[T]{}, err
	}
	return sink.res, nil
}

// scanRows scans rows, a result's over Columns, through the row contract.
// A provider that reports which of the result's columns are documents
// (backend.Rows.Documents) does so once the first row is read; they decode
// as stored documents, so their rules are built then.
func (r *Resource[T]) scanRows(rows backend.Rows) ([]T, error) {
	var (
		out   []T
		rules *rowRules
	)
	for rows.Next() {
		if rules == nil {
			var err error
			rules, err = r.rulesFor(rows.Documents())
			if err != nil {
				return nil, err
			}
		}
		item, err := scanRow[T](rules, rows.Scan)
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

// rulesFor are the scan rules for a result whose documents columns the
// database reports as JSON documents: the resource's own when there are
// none. Its errors, as a scan's, are a statement's, which name the table.
func (r *resourceCore) rulesFor(documents []string) (*rowRules, error) {
	if len(documents) == 0 {
		return &r.scanRules, nil
	}
	rules, err := r.rowRules(ScanJSON(documents...))
	if err != nil {
		return nil, err
	}
	return &rules, nil
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
// another table's columns. A provider that compiles no SQL builds none
// (ErrUnsupported).
func (r *resourceCore) ownsPlan(plan *ListPlan) error {
	err := r.provider.supports(r.table, backend.SQLBuilders)
	if err != nil {
		return err
	}
	if plan == nil {
		return fmt.Errorf("parse: %s: nil list plan", r.table)
	}
	if plan.table != r.table {
		return fmt.Errorf("parse: %s: list plan built for %q", r.table, plan.table)
	}
	return nil
}
