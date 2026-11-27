package parse

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
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

	// assigns reports whether Owner is an UPDATE: the patch assigns a
	// column, so the rows it returns are the ones it wrote.
	assigns bool
	// preconditions are the operations Conflict judges, for conflictError.
	preconditions []patch.Precondition
	relations     []patch.RelationPatchOp
	// fields is the filter schema the plan compiled, its relation changes'
	// targets included.
	fields *filter.ResourceSchema
	table  string
	// tenant is Scope.Tenant in the tenant key's type; nil without one.
	tenant  any
	related map[string]*filter.PolicyConstraints
	// now is the instant the plan's policies' relative-time tokens resolve
	// against, read once when it was built: its relation statements admit
	// the targets its Owner and Current did.
	now time.Time
}

// conflictError is the failure of an update whose Conflict statement
// returned op: that operation's E2077, as ApplyValidatedPatch reports an
// operation that does not apply.
func (p *UpdatePlan) conflictError(op int64) error {
	for _, pre := range p.preconditions {
		if int64(pre.Op) == op {
			return pre.Err()
		}
	}
	return fmt.Errorf("parse: %s: the conflict statement returned operation %d, which it does not judge", p.table, op)
}

// HasRelations reports whether the patch changes relation memberships,
// which Update changes after Owner, in the same transaction.
func (p *UpdatePlan) HasRelations() bool { return len(p.relations) > 0 }

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
// the SQL is the provider's dialect's.
func (r *resourceCore) BuildUpdate(p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (*UpdatePlan, error) {
	if err := r.ownsPatch(p, "patch"); err != nil {
		return nil, err
	}
	if target.filter != nil && !target.filter.ParsedFor(r.filterSchema) {
		return nil, fmt.Errorf("parse: %s: target filter: %w", r.table, ErrForeign)
	}
	d := r.dialect
	if target.empty() {
		return nil, fmt.Errorf("parse: %s: %w", r.table, ErrNoTarget)
	}
	var err error
	if target.keys, err = r.targetKeys(target.keys); err != nil {
		return nil, err
	}
	scope, err := r.bindUpdateScope(s)
	if err != nil {
		return nil, err
	}
	cfg, patchOpts, err := r.updateOptions(opts)
	if err != nil {
		return nil, err
	}
	set, err := patch.BuildPatchSQL(p, d, patchOpts...)
	if err != nil {
		return nil, err
	}
	if err := r.checkAssignments(set.Assignments, "update"); err != nil {
		return nil, err
	}

	where, args, err := r.updateWhere(target, scope, set.ParamCount)
	if err != nil {
		return nil, err
	}
	columns := r.projection
	// A statement that locks rows of a target that can hold several takes
	// them in key order, as a batch's Current does, so two updates over
	// overlapping rows lock in one order and neither holds a row the other
	// waits for while it waits for one the other holds. A dialect without
	// row locks serializes writers in their transaction instead.
	locked := d.RowLock()
	if locked != "" && len(target.keys) != 1 {
		locked = " ORDER BY " + r.keyColumns().order(d, r.table) + locked
	}
	plan := &UpdatePlan{relations: set.RelationOps, fields: r.filterSchema, table: r.table, tenant: scope.tenant, related: scope.related, now: scope.now}
	switch {
	case set.SetClause != "":
		// where's root is a group, which renders parenthesized, or one
		// condition, so each guard ANDs onto all of it.
		guarded := where
		if set.Precondition != "" {
			guarded += " AND " + set.Precondition
		}
		if cfg.skipUnchanged {
			guarded += " AND " + set.ChangePredicate
		}
		guardedByCurrent := cfg.skipUnchanged || set.Precondition != ""
		// Without Current, the UPDATE would take its own locks in the order
		// its plan reads the rows, so Lock takes them in key order first.
		ordersLocks := d.RowLock() != "" && len(target.keys) != 1
		if guardedByCurrent || ordersLocks {
			bare, bareArgs, err := r.updateWhere(target, scope, 0)
			if err != nil {
				return nil, err
			}
			if guardedByCurrent {
				plan.Current = &Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + bare + locked, Args: bareArgs}
			} else {
				plan.Lock = &Statement{SQL: "SELECT 1 FROM " + d.QuoteIdent(r.table) + " WHERE " + bare + locked, Args: bareArgs}
			}
		}
		if set.Precondition != "" {
			plan.Conflict, err = r.conflictStatement(target, scope, set.Preconditions)
			if err != nil {
				return nil, err
			}
			plan.preconditions = set.Preconditions
		}
		plan.Owner = Statement{
			SQL:  "UPDATE " + d.QuoteIdent(r.table) + " SET " + set.SetClause + " WHERE " + guarded + " RETURNING " + columns,
			Args: append(set.Args, args...),
		}
		plan.Writes, plan.assigns = true, true
	case len(set.RelationOps) > 0:
		// No column changes: lock the owners so concurrent replacements of
		// one relation cannot interleave their DELETE and INSERT.
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + where + locked, Args: args}
		plan.Writes = true
	default:
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + where, Args: args}
	}
	return plan, nil
}

// conflictStatement selects, from one target row the patch's preconditions
// do not all hold for, the index of the first operation that fails there.
func (r *resourceCore) conflictStatement(target Target, scope updateScope, pre []patch.Precondition) (*Statement, error) {
	d := r.dialect
	where, args, err := r.updateWhere(target, scope, 0)
	if err != nil {
		return nil, err
	}
	bind := func(v any) string {
		args = append(args, v)
		return d.Placeholder(len(args))
	}
	all, firstUnmet, err := patch.RenderPreconditions(d, pre, bind)
	if err != nil {
		return nil, err
	}
	stmt := &Statement{SQL: "SELECT " + firstUnmet + " FROM " + d.QuoteIdent(r.table) + " WHERE " + where + " AND NOT " + all + " LIMIT 1", Args: args}
	return stmt, nil
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

// updatedRows splits plan's target rows as UpdateResult reports them:
// owner are the rows Owner returned, current the rows Current returned (nil
// when the plan has none). Update calls it with both before the relation
// statements run, whose owner keys are those of changed and then
// unchanged.
func (r *Resource[T]) updatedRows(plan *UpdatePlan, owner, current []T) (changed, unchanged []T, err error) {
	switch {
	case !plan.assigns:
		return nil, owner, nil
	case plan.Current == nil:
		return owner, nil, nil
	}
	unchanged, err = r.unchangedRows(owner, current)
	if err != nil {
		return nil, nil, err
	}
	return owner, unchanged, nil
}

// unchangedRows returns the rows of current (read with UpdatePlan.Current)
// whose keys are not among those of changed (the rows Owner returned), in
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

// updateScope is what an update statement's WHERE binds: the scope's
// tenant as bindTenant returned it, its policies, and the instant their
// relative-time tokens resolve against (the resource clock, read once per
// plan).
type updateScope struct {
	tenant  any
	policy  *filter.PolicyConstraints
	related map[string]*filter.PolicyConstraints
	now     time.Time
}

// bindUpdateScope is the updateScope every statement of one plan binds,
// BuildUpdate's and BuildUpdateBatch's alike. An update writes live rows of
// one tenant, so Governance or IncludeDeleted is ErrUpdateScope; the tenant
// is bound as bindTenant binds it. The clock is read once: Owner and
// Current split one set of rows, so a policy token (`now-1h`) must admit
// the same rows in each.
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

// updateWhere is the WHERE every update statement carries, its placeholders
// after the SET clause's offset ones, compiled for d: the floor a list in
// the same scope reads (filter.ComposeConstraints: the tenant, live rows,
// the policy), ANDed with the target.
func (r *resourceCore) updateWhere(target Target, s updateScope, offset int) (string, []any, error) {
	pc := filter.ComposeConstraints(filter.RepositoryScope{TenantColumn: r.tenant, TenantID: s.tenant, DeleteColumn: r.deleted}, nil, s.policy)
	var key *filter.PolicyConstraint
	switch {
	case len(target.keys) == 1:
		key = &filter.PolicyConstraint{Fields: r.pk, Operator: filter.OpEq, Value: target.keys[0]}
	case len(target.keys) > 1:
		key = &filter.PolicyConstraint{Fields: r.pk, Operator: filter.OpIn, Value: target.keys}
	}
	if key != nil {
		// ComposeConstraints returns a new root, so the target extends no
		// caller's policy tree.
		pc.And = append(pc.And, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{key}})
	}
	var userAST *filter.FilterAST
	if len(target.keys) == 0 {
		userAST = target.filter.AST()
	}
	ast, err := filter.ApplyPolicyConstraintsAt(r.filterSchema, userAST, pc, s.now)
	if err != nil {
		return "", nil, err
	}
	res, err := filter.NewSQLBuilder(r.filterSchema, r.dialect, filter.WithStartParam(offset),
		filter.WithRelationScope(filter.RelationScope{Tenant: s.tenant, Related: s.related, Now: s.now})).Build(ast)
	if err != nil {
		return "", nil, err
	}
	return res.Where, res.Args, nil
}

// relationStatements renders the patch's relation changes for the rows
// Owner returned, and Current when the plan has it — ownerKeys read from
// them with Key, never the requested keys — tenant-scoped as the update
// was. Update runs them after those statements, in their transaction. No
// owners need no statements.
//
// A replace or an add writes one junction row per owner and value, so a
// ByFilter update that matches many rows writes as many pairs as its rows
// times the patch's values: bound the target as the batch it is.
func (r *resourceCore) relationStatements(plan *UpdatePlan, ownerKeys []Key) ([]patch.RenderedRelationStatement, error) {
	if len(plan.relations) == 0 || len(ownerKeys) == 0 {
		return nil, nil
	}
	owners, err := r.ownerKeys(ownerKeys)
	if err != nil {
		return nil, err
	}
	var out []patch.RenderedRelationStatement
	for _, op := range plan.relations {
		opts := patch.RenderRelationOpOptions{Owner: patch.RelationOwner{Table: r.table, Key: slices.Clone(r.pk), Tenant: r.tenant}}
		opts.TenantValue = plan.tenant
		if op.Schema.TargetTable != "" {
			field := plan.fields.Fields[op.Field]
			opts.TargetVisible = func(alias string, bind func(any) string) (string, error) {
				return filter.VisibleRelated(field, r.dialect, alias, filter.RelationScope{Tenant: plan.tenant, Related: plan.related, Now: plan.now}, bind)
			}
		}
		stmts, err := patch.RenderRelationOp(op, r.dialect, owners, opts)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: relation %q: %w", r.table, op.Field, err)
		}
		out = append(out, stmts...)
	}
	return out, nil
}

// rereadStatement is the statement that reads the rows with ownerKeys —
// keys passed to relationStatements — as Columns again: Update runs it
// after those statements, in their transaction, and its rows replace the
// ones Owner and Current returned, in no particular order. A computed
// column (ReadExpr) may read a relation's pairs, which Owner and Current
// read before the statements changed them. Nil when the plan changes no
// relation, the resource has no computed column or there are no keys: the
// rows already read are current.
//
// The rows are held to the update's tenant and nothing else: the keys are
// the rows the update wrote, which RETURNING reports whatever their deleted
// column or the policy now say.
func (r *resourceCore) rereadStatement(plan *UpdatePlan, ownerKeys []Key) (*Statement, error) {
	if len(plan.relations) == 0 || r.readExprs == nil || len(ownerKeys) == 0 {
		return nil, nil
	}
	pc := filter.BuildBaseConstraints(r.tenant, plan.tenant, "", false)
	pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Fields: r.pk, Operator: filter.OpIn, Value: ownerKeys})
	ast, err := filter.ApplyPolicyConstraints(r.filterSchema, nil, pc)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: reread: %w", r.table, err)
	}
	where, err := filter.NewSQLBuilder(r.filterSchema, r.dialect).Build(ast)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: reread: %w", r.table, err)
	}
	stmt := &Statement{
		SQL:  "SELECT " + r.projection + " FROM " + r.dialect.QuoteIdent(r.table) + " WHERE " + where.Where,
		Args: where.Args,
	}
	return stmt, nil
}

func (r *resourceCore) updateOptions(opts []UpdateOption) (updateConfig, []patch.PatchSQLOption, error) {
	var cfg updateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	var patchOpts []patch.PatchSQLOption
	if cfg.stampColumn != "" {
		if _, ok := r.tableColumns[cfg.stampColumn]; !ok || !core.IsIdentifier(cfg.stampColumn) {
			return updateConfig{}, nil, fmt.Errorf("parse: %s: stamp column %q is not a column of the resource", r.table, cfg.stampColumn)
		}
		if r.computed(cfg.stampColumn) {
			return updateConfig{}, nil, r.readExprs.writeError(cfg.stampColumn, "is stamped")
		}
		if cfg.stampSQL == "" && cfg.stampValue.IsZero() {
			return updateConfig{}, nil, fmt.Errorf("parse: %s: stamp column %q has no time", r.table, cfg.stampColumn)
		}
		if cfg.stampSQL != "" {
			patchOpts = append(patchOpts, patch.WithAutoTimestampSQL(cfg.stampColumn, cfg.stampSQL))
		} else {
			at := cfg.stampValue
			patchOpts = append(patchOpts, patch.WithAutoTimestamp(cfg.stampColumn), patch.WithTimeFunc(func() time.Time { return at }))
		}
	}
	for _, col := range cfg.increments {
		t, ok := r.tableColumns[col]
		if !ok || !core.IsIdentifier(col) {
			return updateConfig{}, nil, fmt.Errorf("parse: %s: increment column %q is not a column of the resource", r.table, col)
		}
		if r.computed(col) {
			return updateConfig{}, nil, r.readExprs.writeError(col, "is incremented")
		}
		if core.TypeOf(t).Type != core.FieldTypeInt {
			return updateConfig{}, nil, fmt.Errorf("parse: %s: increment column %q is not an integer column", r.table, col)
		}
		patchOpts = append(patchOpts, patch.WithIncrement(col))
	}
	if cfg.skipUnchanged {
		patchOpts = append(patchOpts, patch.WithChangePredicate())
	}
	return cfg, patchOpts, nil
}

// checkAssignments refuses a statement of builder that assigns a key column
// or the tenant column. NewResource refuses a patch path over either, so only
// a server column (Stamp, Increment) can name one here; both builders hold
// every write to the rule checkIdentityPatches states.
func (r *resourceCore) checkAssignments(assignments []patch.ColumnAssignment, builder string) error {
	for _, assignment := range assignments {
		if slices.Contains(r.pk, assignment.Column) || (r.tenant != "" && assignment.Column == r.tenant) {
			return fmt.Errorf("parse: %s: %s cannot change identity column %s", r.table, builder, assignment.Column)
		}
	}
	return nil
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

// checkCurrent verifies the keys of the rows Current returned (Key): each
// is one of the chunk's Keys, and each of those is returned once.
func (c UpdateBatchChunk) checkCurrent(keys []Key) error {
	if len(keys) != len(c.Keys) || len(c.Keys) == 0 {
		return ErrUpdateBatchTargets
	}
	wanted := make(map[any]bool, len(c.Keys))
	for _, key := range c.Keys {
		id, err := c.key.identity(key)
		if err != nil || wanted[id] {
			return ErrUpdateBatchTargets
		}
		wanted[id] = true
	}
	seen := make(map[any]bool, len(keys))
	for _, key := range keys {
		id, err := c.key.identity(key)
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
// fail before a plan is returned. UpdateBatch runs the plan.
func (r *resourceCore) BuildUpdateBatch(rows []RowUpdate, scope Scope, limits UpdateBatchLimits, opts ...UpdateOption) (*UpdateBatchPlan, error) {
	d := r.dialect
	// One scope, and one instant, for every row's statements.
	common, err := r.bindUpdateScope(scope)
	if err != nil {
		return nil, err
	}
	limits, err = updateBatchLimits(d, limits)
	if err != nil {
		return nil, err
	}
	if len(rows) > limits.MaxRows {
		return nil, fmt.Errorf("parse: %s: update batch exceeds %d rows", r.table, limits.MaxRows)
	}
	config, options, err := r.updateOptions(opts)
	if err != nil {
		return nil, err
	}
	key := r.keyColumns()
	ordered := make([]RowUpdate, len(rows))
	seen := make(map[any]bool, len(rows))
	for i, row := range rows {
		err := r.ownsPatch(row.Patch, fmt.Sprintf("update batch row %d patch", i))
		if err != nil {
			return nil, err
		}
		// As the database holds the key, so the rows Current returns check
		// against it and two keys it holds equal are one row's.
		normalized, err := key.requested(row.Key)
		if err == nil {
			normalized, err = key.stored(d, normalized)
		}
		if err != nil {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		id, err := key.identity(normalized)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: duplicate key", r.table, i)
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
		return nil, fmt.Errorf("parse: %s: update batch: %w", r.table, orderErr)
	}
	plan := &UpdateBatchPlan{Chunks: []UpdateBatchChunk{}, table: r.table}
	var compiled []compiledRowUpdate
	var offset int
	for _, row := range ordered {
		item, err := r.compileRowUpdate(row, common, options, offset)
		if err != nil {
			return nil, err
		}
		if offset+len(item.args) > limits.MaxParameters {
			if len(compiled) == 0 {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", r.table)
			}
			chunk, err := r.renderUpdateBatch(compiled, common, key, config.skipUnchanged)
			if err != nil {
				return nil, err
			}
			plan.Chunks = append(plan.Chunks, chunk)
			compiled, offset = nil, 0
			item, err = r.compileRowUpdate(row, common, options, 0)
			if err != nil {
				return nil, err
			}
			if len(item.args) > limits.MaxParameters {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", r.table)
			}
		}
		compiled = append(compiled, item)
		offset += len(item.args)
	}
	if len(compiled) > 0 {
		chunk, err := r.renderUpdateBatch(compiled, common, key, config.skipUnchanged)
		if err != nil {
			return nil, err
		}
		plan.Chunks = append(plan.Chunks, chunk)
	}
	return plan, nil
}

type compiledRowUpdate struct {
	row         RowUpdate
	assignments []patch.ColumnAssignment
	where       string
	change      string
	args        []any
}

func (r *resourceCore) compileRowUpdate(row RowUpdate, scope updateScope, options []patch.PatchSQLOption, offset int) (compiledRowUpdate, error) {
	set, err := patch.BuildPatchSQL(row.Patch, r.dialect, append(slices.Clone(options), patch.WithStartParam(offset))...)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	if len(set.RelationOps) != 0 {
		return compiledRowUpdate{}, fmt.Errorf("parse: %s: update batch relation mutations are unsupported", r.table)
	}
	if len(set.Preconditions) != 0 {
		// A batch locks and checks its rows by key alone; which operation a
		// row's document fails is BuildUpdate's Conflict to report.
		refused := core.NewError(patch.ECodeOpNotSQLCompilable, fmt.Sprintf("row %v: a batch update does not compile an operation below a JSON document field that the stored row must decide; update the row with BuildUpdate", row.Key),
			core.WithPath(set.Preconditions[0].Path))
		return compiledRowUpdate{}, refused
	}
	err = r.checkAssignments(set.Assignments, "update batch")
	if err != nil {
		return compiledRowUpdate{}, err
	}
	scope.policy = updateRowPolicy(scope.policy, row.Policy)
	where, args, err := r.updateWhere(ByKey(row.Key), scope, offset+set.ParamCount)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	compiled := compiledRowUpdate{row: row, assignments: set.Assignments, where: where, change: set.ChangePredicate, args: append(set.Args, args...)}
	return compiled, nil
}

func (r *resourceCore) renderUpdateBatch(rows []compiledRowUpdate, scope updateScope, key keyColumns, skipUnchanged bool) (UpdateBatchChunk, error) {
	d := r.dialect
	chunk := UpdateBatchChunk{Keys: make([]Key, len(rows)), key: key}
	var currentWhere, changedWhere []string
	var args []any
	assignments := map[string][]string{}
	for i, row := range rows {
		chunk.Keys[i] = row.row.Key
		currentScope := scope
		currentScope.policy = updateRowPolicy(scope.policy, row.row.Policy)
		where, currentArgs, err := r.updateWhere(ByKey(row.row.Key), currentScope, len(chunk.Current.Args))
		if err != nil {
			return UpdateBatchChunk{}, err
		}
		currentWhere = append(currentWhere, "("+where+")")
		chunk.Current.Args = append(chunk.Current.Args, currentArgs...)
		args = append(args, row.args...)
		if len(row.assignments) == 0 {
			// Keep every bound predicate parameter typed even for a no-op item.
			changedWhere = append(changedWhere, "(("+row.where+") AND FALSE)")
			continue
		}
		guard := "(" + row.where + ")"
		if skipUnchanged {
			guard += " AND (" + row.change + ")"
		}
		changedWhere = append(changedWhere, "("+guard+")")
		for _, assignment := range row.assignments {
			assignments[assignment.Column] = append(assignments[assignment.Column], "WHEN ("+row.where+") THEN "+assignment.Expression)
		}
	}
	columns := r.projection
	chunk.Current.SQL = "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + strings.Join(currentWhere, " OR ") + " ORDER BY " + key.order(d, r.table) + d.RowLock()
	if len(assignments) != 0 {
		clauses := make([]string, 0, len(assignments))
		for _, column := range slices.Sorted(maps.Keys(assignments)) {
			quoted := d.QuoteIdent(column)
			clauses = append(clauses, quoted+" = CASE "+strings.Join(assignments[column], " ")+" ELSE "+quoted+" END")
		}
		chunk.Owner = &Statement{SQL: "UPDATE " + d.QuoteIdent(r.table) + " SET " + strings.Join(clauses, ", ") + " WHERE " + strings.Join(changedWhere, " OR ") + " RETURNING " + columns, Args: args}
	}
	return chunk, nil
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

func updateBatchLimits(d filter.Dialect, limits UpdateBatchLimits) (UpdateBatchLimits, error) {
	if limits.MaxRows < 0 || limits.MaxParameters < 0 {
		return limits, errors.New("parse: update batch limits must not be negative")
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = 500
	}
	if limits.MaxParameters == 0 {
		limits.MaxParameters = d.MaxParams()
	}
	return limits, nil
}
