package parse

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/sqlplan"
	"github.com/ashtonian/pkg/parse/patch"
)

// Target names the rows an update changes: keys or a filter. The zero
// Target names none, and an update refuses it rather than change every row.
type Target struct {
	keys   []Key
	filter *filter.ParsedFilter
}

// ByID targets the row with key id, on a resource whose key is one column
// (ByKey names a key of several).
func ByID(id any) Target {
	target := Target{keys: []Key{{id}}}
	return target
}

// ByIDs targets the rows with these keys, on a resource whose key is one
// column (ByKeys names keys of several).
func ByIDs(ids ...any) Target {
	keys := make([]Key, len(ids))
	for i, id := range ids {
		keys[i] = Key{id}
	}
	target := Target{keys: keys}
	return target
}

// ByKey targets the row with key k, a value per key column in key order.
func ByKey(k Key) Target {
	target := Target{keys: []Key{slices.Clone(k)}}
	return target
}

// ByKeys targets the rows with these keys, each a value per key column in
// key order.
func ByKeys(ks ...Key) Target {
	keys := make([]Key, len(ks))
	for i, k := range ks {
		keys[i] = slices.Clone(k)
	}
	target := Target{keys: keys}
	return target
}

// ByFilter targets the rows a filter matches.
func ByFilter(f *filter.ParsedFilter) Target { return Target{filter: f} }

func (t Target) empty() bool {
	if len(t.keys) > 0 {
		return false
	}
	ast := t.filter.AST()
	return ast == nil || ast.Root == nil
}

// UpdateOption configures BuildUpdate.
type UpdateOption func(*updateConfig)

type updateConfig struct {
	stampColumn, stampSQL string
	stampValue            time.Time
	increments            []string
	skipUnchanged         bool
}

// Stamp sets column to at on every update that writes a column. An update
// has one stamp: a later Stamp or StampSQL replaces it.
func Stamp(column string, at time.Time) UpdateOption {
	return func(c *updateConfig) { c.stampColumn, c.stampValue, c.stampSQL = column, at, "" }
}

// StampSQL sets column to expr — the database's clock, such as NOW() — on
// every update that writes a column. expr is trusted server SQL.
func StampSQL(column, expr string) UpdateOption {
	return func(c *updateConfig) { c.stampColumn, c.stampSQL = column, expr }
}

// Increment sets column, an integer column, to one more than it holds on
// every update that writes a column: a version the server moves, whatever
// the patch says. The statement's RETURNING reports the moved value. Each
// Increment adds a column; it composes with the stamp.
func Increment(column string) UpdateOption {
	return func(c *updateConfig) { c.increments = append(c.increments, column) }
}

// SkipUnchanged updates only the target rows the patch's columns change: the
// UPDATE's WHERE also requires one of them to read back other than it does,
// so a row the patch leaves as it is keeps its stamp and counters, and
// RETURNING reports only the rows that changed. UpdatePlan.Current, run
// first, reads every target row, changed or not. A patch that assigns no
// column is unaffected: its Owner returns every target row already.
func SkipUnchanged() UpdateOption {
	return func(c *updateConfig) { c.skipUnchanged = true }
}

// UpdatePlan is a patch compiled for its resource's provider
// (BuildUpdate): the statements Update runs, in one transaction.
type UpdatePlan struct {
	// Owner returns the target rows' Columns as the update leaves them:
	// an UPDATE … RETURNING when the patch writes a column; a SELECT that
	// locks the rows (filter.Dialect.RowLock) when it changes relations
	// alone; a plain SELECT — a scoped read — when it changes nothing. It
	// matches only rows the scope admits. Under SkipUnchanged the UPDATE
	// returns only the rows it changed. It runs before the relation
	// statements, so a computed column (ReadExpr) over a relation's pairs
	// reads them as they were; Update reads the rows again once the pairs
	// have changed.
	Owner Statement
	// Current, set when SkipUnchanged or Conflict guards Owner's UPDATE,
	// selects every target row the scope admits, as it stands, and locks it
	// (filter.Dialect.RowLock): Owner's WHERE without the change predicate
	// and the patch's precondition. It runs first, then Conflict, then
	// Owner, in one transaction: its lock holds every target while the
	// others judge them, so Owner and Current split one set of rows. The
	// rows it returns whose keys Owner's do not include are the ones the
	// patch left unchanged (UpdateResult.Unchanged); neither returning a row
	// means the target is not found. Nil when Owner returns every target
	// row.
	Current *Statement
	// Lock, set when Owner's UPDATE can write several target rows on a
	// dialect with row locks and no Current locks them first, locks every
	// target row the scope admits in key order and returns nothing Update
	// reads. It runs just before Owner, in one transaction: an
	// UPDATE locks rows in the order its plan reads them, and two updates of
	// overlapping rows whose plans read them in opposite orders would each
	// hold a row the other waits for (40P01).
	Lock *Statement
	// Conflict, set when the patch writes members of a JSON document (an
	// add, replace or remove below a field), selects the index of the first
	// operation RFC 6902 cannot apply to a target row's stored document:
	// a member a replace or a remove names that is missing, or an object an
	// add sets a member of that is not there (patch.Precondition). It
	// returns at most one row; a row is the update's failure, that
	// operation's E2077, and Owner, whose WHERE holds the same precondition,
	// does not run.
	Conflict *Statement
	// Writes reports whether the plan changes anything: a column Owner
	// assigns, or a relation membership. False for an empty patch, whose
	// Owner is a scoped read.
	Writes bool

	// compiled is the plan as internal/sqlplan compiled it, which judges
	// Conflict's operations and holds the relation changes.
	compiled *sqlplan.Update
}

// conflictError is the failure of an update whose Conflict statement
// returned op: that operation's E2077, as ApplyValidatedPatch reports an
// operation that does not apply.
func (p *UpdatePlan) conflictError(op int64) error { return p.compiled.ConflictError(op) }

// HasRelations reports whether the patch changes relation memberships,
// which Update changes after Owner, in the same transaction.
func (p *UpdatePlan) HasRelations() bool { return p.compiled != nil && p.compiled.HasRelations() }

// BuildUpdate compiles a patch into the statement that applies it to target
// under scope s: the patch's SET clause, then a WHERE of the floor a list in
// s reads (the tenant, live rows only, s.Policy) and the target, then
// RETURNING the resource's Columns; SkipUnchanged also ANDs the patch's
// change predicate into the WHERE. A patch that writes members of a JSON document ANDs in the
// precondition RFC 6902 sets those operations as well, and is planned with
// Current and Conflict, so a target row it does not apply to fails the
// update, as ApplyValidatedPatch fails it, rather than being skipped. A
// tenant-scoped resource needs s.Tenant (ErrTenantRequired), which
// every tenant column it reaches must hold (ErrTenantInvalid); a patch or a
// ByFilter filter the resource did not parse is ErrForeign; a zero Target
// is ErrNoTarget, and a key the key columns cannot hold ErrKeyInvalid;
// Governance or IncludeDeleted is ErrUpdateScope. Update runs the plan;
// the SQL is the provider's dialect's, and a provider that compiles no SQL
// refuses it (ErrUnsupported).
func (r *resourceCore) BuildUpdate(p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (*UpdatePlan, error) {
	err := r.provider.supports(r.table, backend.SQLBuilders)
	if err != nil {
		return nil, err
	}
	req, err := r.composeUpdate(p, target, s, opts)
	if err != nil {
		return nil, err
	}
	compiled, err := sqlplan.CompileUpdate(r.dialect, req)
	if err != nil {
		return nil, err
	}
	plan := &UpdatePlan{
		Owner:    Statement(compiled.Owner),
		Current:  (*Statement)(compiled.Current),
		Lock:     (*Statement)(compiled.Lock),
		Conflict: (*Statement)(compiled.Conflict),
		Writes:   compiled.Writes,
		compiled: compiled,
	}
	return plan, nil
}

// composeUpdate composes p for target under scope s (BuildUpdate) as the
// provider's executor runs it: the target's keys as the key columns hold
// them, the WHERE's filter (updateFilter), the scope its relations are
// reached under, and the options.
func (r *resourceCore) composeUpdate(p *patch.ParsedPatch, target Target, s Scope, opts []UpdateOption) (backend.UpdateRequest, error) {
	err := r.ownsPatch(p, "patch")
	if err != nil {
		return backend.UpdateRequest{}, err
	}
	if target.filter != nil && !target.filter.ParsedFor(r.filterSchema) {
		return backend.UpdateRequest{}, fmt.Errorf("parse: %s: target filter: %w", r.table, ErrForeign)
	}
	if target.empty() {
		return backend.UpdateRequest{}, fmt.Errorf("parse: %s: %w", r.table, ErrNoTarget)
	}
	keys, err := r.targetKeys(target.keys)
	if err != nil {
		return backend.UpdateRequest{}, err
	}
	scope, err := r.bindUpdateScope(s)
	if err != nil {
		return backend.UpdateRequest{}, err
	}
	options, err := r.updateOptions(opts)
	if err != nil {
		return backend.UpdateRequest{}, err
	}
	var targetFilter *filter.FilterAST
	if len(keys) == 0 {
		targetFilter = target.filter.AST()
	}
	where, err := r.updateFilter(keys, targetFilter, scope)
	if err != nil {
		return backend.UpdateRequest{}, err
	}
	req := backend.UpdateRequest{Resource: r.desc, Where: where, Keys: keys, Relations: scope.relations(), Patch: p, Options: options}
	return req, nil
}

// UpdateResult is what Update and UpdateBatch report: the target rows the
// scope admits, as the update left them, split by whether it wrote their
// columns, and how many junction rows its membership changes touched. No
// row in either means the target is not found.
type UpdateResult[T any] struct {
	// Changed are the rows whose columns the update wrote, stamped and
	// counted: emit a per-row event for these.
	Changed []T
	// Unchanged are the target rows whose columns it left: the rows a
	// SkipUnchanged update matched and left as they were, and every row of
	// an update that assigns no column (an empty or a relation-only patch).
	Unchanged []T
	// RelationRows is how many junction rows the relation statements
	// deleted and inserted: zero when no membership changed.
	RelationRows int64
}

// unchangedRows returns the rows of current (an update's targets as they
// stood) whose keys are not among those of changed (the rows it wrote), in
// current's order: the target rows a SkipUnchanged update matched and left
// as they were.
func (r *Resource[T]) unchangedRows(changed, current []T) ([]T, error) {
	written := make(map[any]struct{}, len(changed))
	for _, row := range changed {
		k, err := r.rowKey(row)
		if err != nil {
			return nil, err
		}
		written[k] = struct{}{}
	}
	var out []T
	for _, row := range current {
		k, err := r.rowKey(row)
		if err != nil {
			return nil, err
		}
		if _, ok := written[k]; !ok {
			out = append(out, row)
		}
	}
	return out, nil
}

// targetKeys are keys, a target's, as the key columns hold them: the values
// every statement binds. A key the columns cannot hold is ErrKeyInvalid,
// refused as the key it is before any constraint binds it.
func (r *resourceCore) targetKeys(keys []Key) ([]Key, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]Key, len(keys))
	for i, key := range keys {
		v, err := r.keyColumns().requested(key)
		if err != nil {
			if len(keys) == 1 {
				return nil, fmt.Errorf("parse: %s: target key: %w", r.table, err)
			}
			return nil, fmt.Errorf("parse: %s: target key %d: %w", r.table, i, err)
		}
		out[i] = v
	}
	return out, nil
}

// rowKey is row's key as a map key (keyColumns.identity), so rows scanned
// apart that hold equal keys, through pointers or not, share one. A key
// column the database holds as NULL (core.StoredValue: a nil pointer, a
// Valuer of nil) is a row without a key.
func (r *resourceCore) rowKey(row any) (any, error) {
	k, err := r.Key(row)
	if err != nil {
		return nil, err
	}
	for i, v := range k {
		_, null, err := core.StoredValue(v)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: row key: key column %s: %w", r.table, r.pk[i], err)
		}
		if null {
			return nil, fmt.Errorf("parse: %s: a row has no key: key column %s holds no value", r.table, r.pk[i])
		}
	}
	id, err := r.keyColumns().identity(k)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: row key: %w", r.table, err)
	}
	return id, nil
}

// updateScope is what an update's filter binds: the scope's tenant as
// bindTenant returned it, its policies, and the instant their relative-time
// tokens resolve against (the resource clock, read once per update).
type updateScope struct {
	tenant  any
	policy  *filter.PolicyConstraints
	related map[string]*filter.PolicyConstraints
	now     time.Time
}

// relations is the scope an update's filter and relation changes reach
// related rows under: its tenant and policies, at its instant.
func (s updateScope) relations() filter.RelationScope {
	scope := filter.RelationScope{Tenant: s.tenant, Related: s.related, Now: s.now}
	return scope
}

// bindUpdateScope is the updateScope every statement of one update binds,
// Update's and UpdateBatch's alike. An update writes live rows of one
// tenant, so Governance or IncludeDeleted is ErrUpdateScope; the tenant is
// bound as bindTenant binds it. The clock is read once: Owner and Current
// split one set of rows, so a policy token (`now-1h`) must admit the same
// rows in each.
func (r *resourceCore) bindUpdateScope(s Scope) (updateScope, error) {
	if s.Governance != nil || s.IncludeDeleted {
		return updateScope{}, fmt.Errorf("parse: %s: %w", r.table, ErrUpdateScope)
	}
	tenant, err := r.bindTenant(s.Tenant)
	if err != nil {
		return updateScope{}, err
	}
	scope := updateScope{tenant: tenant, policy: s.Policy, related: s.Related, now: r.cfg.Now()}
	return scope, nil
}

// updateFilter is the filter every statement of an update selects its
// targets by: the floor a list in the same scope reads
// (filter.ComposeConstraints: the tenant, live rows, the policy), ANDed
// with the target, keys (normalized, targetKeys) or, without keys,
// targetFilter.
func (r *resourceCore) updateFilter(keys []Key, targetFilter *filter.FilterAST, s updateScope) (*filter.FilterAST, error) {
	pc := filter.ComposeConstraints(filter.RepositoryScope{TenantColumn: r.tenant, TenantID: s.tenant, DeleteColumn: r.deleted}, nil, s.policy)
	var key *filter.PolicyConstraint
	switch {
	case len(keys) == 1:
		key = &filter.PolicyConstraint{Fields: r.pk, Operator: filter.OpEq, Value: keys[0]}
	case len(keys) > 1:
		key = &filter.PolicyConstraint{Fields: r.pk, Operator: filter.OpIn, Value: keys}
	}
	if key != nil {
		// ComposeConstraints returns a new root, so the target extends no
		// caller's policy tree.
		pc.And = append(pc.And, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{key}})
	}
	return filter.ApplyPolicyConstraintsAt(r.filterSchema, targetFilter, pc, s.now)
}

// updateOptions are opts as an update request carries them, each column
// checked against the resource. A StampSQL on a provider that evaluates no
// SQL is ErrUnsupported.
func (r *resourceCore) updateOptions(opts []UpdateOption) (backend.UpdateOptions, error) {
	var cfg updateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	options := backend.UpdateOptions{SkipUnchanged: cfg.skipUnchanged}
	if cfg.stampColumn != "" {
		if _, ok := r.tableColumns[cfg.stampColumn]; !ok || !core.IsIdentifier(cfg.stampColumn) {
			return backend.UpdateOptions{}, fmt.Errorf("parse: %s: stamp column %q is not a column of the resource", r.table, cfg.stampColumn)
		}
		if r.computed(cfg.stampColumn) {
			return backend.UpdateOptions{}, r.readExprs.writeError(cfg.stampColumn, "is stamped")
		}
		if cfg.stampSQL == "" && cfg.stampValue.IsZero() {
			return backend.UpdateOptions{}, fmt.Errorf("parse: %s: stamp column %q has no time", r.table, cfg.stampColumn)
		}
		if cfg.stampSQL != "" {
			err := r.provider.supports(r.table, backend.StampSQL)
			if err != nil {
				return backend.UpdateOptions{}, err
			}
		}
		options.Stamp = &backend.Stamp{Column: cfg.stampColumn, At: cfg.stampValue, SQL: cfg.stampSQL}
	}
	for _, col := range cfg.increments {
		t, ok := r.tableColumns[col]
		if !ok || !core.IsIdentifier(col) {
			return backend.UpdateOptions{}, fmt.Errorf("parse: %s: increment column %q is not a column of the resource", r.table, col)
		}
		if r.computed(col) {
			return backend.UpdateOptions{}, r.readExprs.writeError(col, "is incremented")
		}
		if core.TypeOf(t).Type != core.FieldTypeInt {
			return backend.UpdateOptions{}, fmt.Errorf("parse: %s: increment column %q is not an integer column", r.table, col)
		}
		options.Increments = append(options.Increments, col)
	}
	return options, nil
}

// RowUpdate narrows one row's key, a value per key column in key order, to
// its policy and applies its column patch.
type RowUpdate struct {
	Key    Key
	Patch  *patch.ParsedPatch
	Policy *filter.PolicyConstraints
}

// UpdateBatchLimits bounds total rows and parameters per statement. Zero uses
// 500 rows and the dialect's conservative parameter budget; negative refuses.
type UpdateBatchLimits struct {
	MaxRows, MaxParameters int
}

// UpdateBatchPlan is a batch of row updates compiled for its resource's
// provider (BuildUpdateBatch): the statements UpdateBatch runs, in one
// transaction, every chunk's Current read and checked before any Owner.
type UpdateBatchPlan struct {
	Chunks []UpdateBatchChunk

	table string
}

// UpdateBatchChunk contains one bounded preimage read and its optional update.
// Owner returns changed rows; absent changed keys retain their Current images.
type UpdateBatchChunk struct {
	Keys    []Key
	Current Statement
	Owner   *Statement

	key keyColumns
}

// checkCurrent verifies the keys of the rows Current returned (Key) against
// the chunk's Keys (keyColumns.checkTargets).
func (c UpdateBatchChunk) checkCurrent(keys []Key) error { return c.key.checkTargets(c.Keys, keys) }

// checkTargets verifies keys, those of the rows a batch part read as its
// targets (Key): each is one of want, the part's keys, and each of those is
// read once. ErrUpdateBatchTargets otherwise.
func (kc keyColumns) checkTargets(want, keys []Key) error {
	if len(keys) != len(want) || len(want) == 0 {
		return ErrUpdateBatchTargets
	}
	wanted := make(map[any]bool, len(want))
	for _, key := range want {
		id, err := kc.identity(key)
		if err != nil || wanted[id] {
			return ErrUpdateBatchTargets
		}
		wanted[id] = true
	}
	seen := make(map[any]bool, len(keys))
	for _, key := range keys {
		id, err := kc.identity(key)
		if err != nil || !wanted[id] || seen[id] {
			return ErrUpdateBatchTargets
		}
		seen[id] = true
	}
	return nil
}

// BuildUpdateBatch compiles distinct column patches through the same typed
// expressions, tenant/live floors and options as BuildUpdate. A row's patch
// the resource did not parse is ErrForeign, and a row key the key column
// cannot hold ErrKeyInvalid; relation changes and identity-column writes
// fail before a plan is returned. UpdateBatch runs the plan; a provider
// that compiles no SQL refuses it (ErrUnsupported).
func (r *resourceCore) BuildUpdateBatch(rows []RowUpdate, scope Scope, limits UpdateBatchLimits, opts ...UpdateOption) (*UpdateBatchPlan, error) {
	err := r.provider.supports(r.table, backend.SQLBuilders)
	if err != nil {
		return nil, err
	}
	req, err := r.composeBatch(rows, scope, limits, opts)
	if err != nil {
		return nil, err
	}
	compiled, err := sqlplan.CompileBatch(r.dialect, req)
	if err != nil {
		return nil, err
	}
	key := r.keyColumns()
	plan := &UpdateBatchPlan{Chunks: make([]UpdateBatchChunk, len(compiled.Chunks)), table: r.table}
	for i, chunk := range compiled.Chunks {
		plan.Chunks[i] = UpdateBatchChunk{Keys: chunk.Keys, Current: Statement(chunk.Current), Owner: (*Statement)(chunk.Owner), key: key}
	}
	return plan, nil
}

// composeBatch composes rows under scope within limits (BuildUpdateBatch)
// as the provider's executor runs them: one scope and one instant for every
// row, each row's key as the database holds it, the rows in key order, and
// each row's filter, the floor and policy ANDed with its own policy and
// its key.
func (r *resourceCore) composeBatch(rows []RowUpdate, scope Scope, limits UpdateBatchLimits, opts []UpdateOption) (backend.UpdateBatchRequest, error) {
	common, err := r.bindUpdateScope(scope)
	if err != nil {
		return backend.UpdateBatchRequest{}, err
	}
	limits, err = updateBatchLimits(limits)
	if err != nil {
		return backend.UpdateBatchRequest{}, err
	}
	if len(rows) > limits.MaxRows {
		return backend.UpdateBatchRequest{}, fmt.Errorf("parse: %s: update batch exceeds %d rows", r.table, limits.MaxRows)
	}
	options, err := r.updateOptions(opts)
	if err != nil {
		return backend.UpdateBatchRequest{}, err
	}
	key := r.keyColumns()
	ordered := make([]RowUpdate, len(rows))
	seen := make(map[any]bool, len(rows))
	for i, row := range rows {
		err := r.ownsPatch(row.Patch, fmt.Sprintf("update batch row %d patch", i))
		if err != nil {
			return backend.UpdateBatchRequest{}, err
		}
		// As the database holds the key, so the rows Current returns check
		// against it and two keys it holds equal are one row's.
		normalized, err := key.requested(row.Key)
		if err == nil && r.dialect != nil {
			normalized, err = key.stored(r.dialect, normalized)
		}
		if err != nil {
			return backend.UpdateBatchRequest{}, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		id, err := key.identity(normalized)
		if err != nil {
			return backend.UpdateBatchRequest{}, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		if seen[id] {
			return backend.UpdateBatchRequest{}, fmt.Errorf("parse: %s: update batch row %d key: duplicate key", r.table, i)
		}
		seen[id], row.Key = true, normalized
		ordered[i] = row
	}
	// Rows lock in key order, as Current's ORDER BY reads them, so two
	// batches over the same rows cannot each hold a lock the other waits on.
	// The keys are normalized, which their columns' types always order; the
	// first refusal is kept and returned all the same.
	var orderErr error
	slices.SortFunc(ordered, func(a, b RowUpdate) int {
		c, err := key.compare(a.Key, b.Key)
		if err != nil && orderErr == nil {
			orderErr = err
		}
		return c
	})
	if orderErr != nil {
		return backend.UpdateBatchRequest{}, fmt.Errorf("parse: %s: update batch: %w", r.table, orderErr)
	}
	batch := make([]backend.BatchRow, len(ordered))
	for i, row := range ordered {
		rowScope := common
		rowScope.policy = updateRowPolicy(common.policy, row.Policy)
		where, err := r.updateFilter([]Key{row.Key}, nil, rowScope)
		if err != nil {
			return backend.UpdateBatchRequest{}, err
		}
		batch[i] = backend.BatchRow{Key: row.Key, Patch: row.Patch, Where: where}
	}
	req := backend.UpdateBatchRequest{Resource: r.desc, Rows: batch, Relations: common.relations(), MaxParameters: limits.MaxParameters, Options: options}
	return req, nil
}

func updateRowPolicy(common, row *filter.PolicyConstraints) *filter.PolicyConstraints {
	if common == nil {
		return row
	}
	if row == nil {
		return common
	}
	both := &filter.PolicyConstraints{And: []*filter.PolicyConstraints{common, row}}
	return both
}

// updateBatchLimits refuses negative limits and gives MaxRows its default;
// a zero MaxParameters is the executor's own budget.
func updateBatchLimits(limits UpdateBatchLimits) (UpdateBatchLimits, error) {
	if limits.MaxRows < 0 || limits.MaxParameters < 0 {
		return limits, errors.New("parse: update batch limits must not be negative")
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = 500
	}
	return limits, nil
}
