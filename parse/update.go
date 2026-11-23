package parse

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Target names the rows an update changes: keys or a filter. The zero
// Target names none, and an update refuses it rather than change every row.
type Target struct {
	keys   []any
	filter *filter.ParsedFilter
}

// ByID targets the row with key id.
func ByID(id any) Target { return Target{keys: []any{id}} }

// ByIDs targets the rows with these keys.
func ByIDs(ids ...any) Target { return Target{keys: ids} }

// ByFilter targets the rows a filter matches.
func ByFilter(f *filter.ParsedFilter) Target { return Target{filter: f} }

func (t Target) empty() bool {
	if len(t.keys) > 0 {
		return false
	}
	ast := t.filter.AST()
	return ast == nil || ast.Root == nil
}

// ErrNoTarget is an update built without keys or a non-empty filter.
var ErrNoTarget = errors.New("parse: an update needs a key or a filter")

// ErrForeign is a request value a resource did not parse handed to it: a
// ListRequest, filter or patch another resource parsed, a WithPaths view's
// or its base's included, or one assembled by hand. A resource compiles and
// applies only what its own schemas validated; parse the value with it.
var ErrForeign = errors.New("not parsed by this resource")

// ErrUpdateScope is an update built with a widening Scope: an update
// reaches only its own tenant's live rows, so Governance and IncludeDeleted
// do not apply to it.
var ErrUpdateScope = errors.New("parse: an update reaches only its tenant's live rows")

// Dialect is the dialect the filter and the patch compiler both speak:
// filter.PostgresDialect or filter.SQLiteDialect.
type Dialect = filter.Dialect

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

// UpdatePlan is a patch compiled for one dialect.
type UpdatePlan struct {
	// Owner returns the target rows' Columns as the update leaves them:
	// an UPDATE … RETURNING when the patch writes a column; a SELECT that
	// locks the rows (Dialect.RowLock) when it changes relations alone; a
	// plain SELECT — a scoped read — when it changes nothing. It matches only
	// rows the scope admits. Under SkipUnchanged the UPDATE returns only the
	// rows it changed. It runs before RelationStatements, so a computed
	// column (ReadExpr) over a relation's pairs reads them as they were;
	// Reread reads the rows again once the pairs have changed.
	Owner Statement
	// Current, set when SkipUnchanged guards Owner's UPDATE, selects every
	// target row the scope admits, as it stands, and locks it
	// (Dialect.RowLock): Owner's WHERE without the change predicate. Run it
	// first, then Owner, in one transaction: its lock holds every target
	// while Owner compares them, so Owner and Current split one set of rows.
	// The rows it returns whose keys Owner's do not include are the ones the
	// patch left unchanged (Unchanged); neither returning a row means the
	// target is not found. Nil when Owner returns every target row.
	Current *Statement
	// Writes reports whether the plan changes anything: a column Owner
	// assigns, or a relation membership. False for an empty patch, whose
	// Owner is a scoped read.
	Writes bool

	// assigns reports whether Owner is an UPDATE: the patch assigns a
	// column, so the rows it returns are the ones it wrote.
	assigns   bool
	relations []patch.RelationPatchOp
	dialect   Dialect
	// fields is the filter schema the plan compiled, its relation changes'
	// targets compiled for dialect.
	fields *filter.ResourceSchema
	table  string
	// tenant is Scope.Tenant in the tenant key's type; nil without one.
	tenant  any
	related map[string]*filter.PolicyConstraints
}

// Dialect is the dialect the plan's statements are written for.
func (p *UpdatePlan) Dialect() Dialect { return p.dialect }

// HasRelations reports whether the patch changes relation memberships:
// run Current when set, Owner, RelationStatements, then Reread when it
// returns a statement, in one transaction.
func (p *UpdatePlan) HasRelations() bool { return len(p.relations) > 0 }

// BuildUpdate compiles a patch into the statement that applies it to target
// under scope s: the patch's SET clause, then a WHERE of the target, the
// tenant floor, live rows only and s.Policy, then RETURNING the resource's
// Columns; SkipUnchanged also ANDs the patch's change predicate into the
// WHERE. A tenant-scoped resource needs s.Tenant (ErrTenantRequired), which
// every tenant column it reaches must hold (ErrTenantInvalid); a patch or a
// ByFilter filter the resource did not parse is ErrForeign; a zero Target
// is ErrNoTarget; Governance or IncludeDeleted is ErrUpdateScope; a dialect
// a computed column's expression (ReadExpr) refuses is an error.
func (r *Resource[T]) BuildUpdate(d Dialect, p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (*UpdatePlan, error) {
	if err := r.ownsPatch(p, "patch"); err != nil {
		return nil, err
	}
	if target.filter != nil && !target.filter.ParsedFor(r.filterSchema) {
		return nil, fmt.Errorf("parse: %s: target filter: %w", r.table, ErrForeign)
	}
	exprs, err := r.computedSQL(d)
	if err != nil {
		return nil, err
	}
	if target.empty() {
		return nil, fmt.Errorf("%s: %w", r.table, ErrNoTarget)
	}
	tenant, err := r.bindTenant(s.Tenant)
	if err != nil {
		return nil, err
	}
	if s.Governance != nil || s.IncludeDeleted {
		return nil, fmt.Errorf("%s: %w", r.table, ErrUpdateScope)
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

	scope := updateScope{tenant: tenant, policy: s.Policy, related: s.Related}
	where, args, err := r.updateWhere(d, target, scope, set.ParamCount)
	if err != nil {
		return nil, err
	}
	columns := strings.Join(r.columns(d, exprs), ", ")
	// The membership changes hold pairs to targets the request may see,
	// compiled with the targets' schemas for d.
	changed := make([]string, len(set.RelationOps))
	for i, op := range set.RelationOps {
		changed[i] = op.Field
	}
	if err := r.checkTargets(d, changed...); err != nil {
		return nil, err
	}
	plan := &UpdatePlan{relations: set.RelationOps, dialect: d, fields: r.filterSchema, table: r.table, tenant: tenant, related: s.Related}
	switch {
	case set.SetClause != "":
		guarded := where
		if cfg.skipUnchanged {
			// where's root is a group, which renders parenthesized, or one
			// condition, so the predicate ANDs onto all of it.
			guarded += " AND " + set.ChangePredicate
			current, currentArgs, err := r.updateWhere(d, target, scope, 0)
			if err != nil {
				return nil, err
			}
			plan.Current = &Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + current + d.RowLock(), Args: currentArgs}
		}
		plan.Owner = Statement{
			SQL:  "UPDATE " + d.QuoteIdent(r.table) + " SET " + set.SetClause + " WHERE " + guarded + " RETURNING " + columns,
			Args: append(set.Args, args...),
		}
		plan.Writes, plan.assigns = true, true
	case len(set.RelationOps) > 0:
		// No column changes: lock the owners so concurrent replacements of
		// one relation cannot interleave their DELETE and INSERT.
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + where + d.RowLock(), Args: args}
		plan.Writes = true
	default:
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + where, Args: args}
	}
	return plan, nil
}

// UpdateResult is what an executor reports for an update it ran: the target
// rows the scope admits, as the update left them, split by whether it wrote
// their columns, and how many junction rows its membership changes touched.
// No row in either means the target is not found.
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

// UpdatedRows splits plan's target rows as UpdateResult reports them:
// owner are the rows Owner returned, current the rows Current returned (nil
// when the plan has none). An executor calls it with both before the
// relation statements run, whose owner keys are those of changed and then
// unchanged.
func (r *Resource[T]) UpdatedRows(plan *UpdatePlan, owner, current []T) (changed, unchanged []T, err error) {
	if err := r.ownsUpdatePlan(plan); err != nil {
		return nil, nil, err
	}
	switch {
	case !plan.assigns:
		return nil, owner, nil
	case plan.Current == nil:
		return owner, nil, nil
	}
	if unchanged, err = r.Unchanged(owner, current); err != nil {
		return nil, nil, err
	}
	return owner, unchanged, nil
}

// Unchanged returns the rows of current (read with UpdatePlan.Current) whose
// keys are not among those of changed (the rows Owner returned), in current's
// order: the target rows a SkipUnchanged update matched and left as they were.
func (r *Resource[T]) Unchanged(changed, current []T) ([]T, error) {
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

// rowKey is row's key as a map key: the value a pointer key points to, since
// rows scanned apart hold distinct pointers to equal keys.
func (r *Resource[T]) rowKey(row T) (any, error) {
	k, err := r.Key(row)
	if err != nil {
		return nil, err
	}
	v := reflect.ValueOf(k)
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() == reflect.Pointer {
		return nil, fmt.Errorf("parse: %s: a row has no key", r.table)
	}
	if !v.Comparable() {
		return nil, fmt.Errorf("parse: %s: key %s is not comparable", r.table, v.Type())
	}
	return v.Interface(), nil
}

// updateScope is what an update statement's WHERE binds: the scope's
// tenant as bindTenant returned it, and its policies.
type updateScope struct {
	tenant  any
	policy  *filter.PolicyConstraints
	related map[string]*filter.PolicyConstraints
}

// updateWhere is the WHERE every update statement carries, its placeholders
// after the SET clause's offset ones, compiled for d.
func (r *Resource[T]) updateWhere(d filter.Dialect, target Target, s updateScope, offset int) (string, []any, error) {
	pc := filter.BuildBaseConstraints(r.tenant, s.tenant, r.deleted, false)
	switch {
	case len(target.keys) == 1:
		pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Field: r.pk, Operator: filter.OpEq, Value: target.keys[0]})
	case len(target.keys) > 1:
		pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Field: r.pk, Operator: filter.OpIn, Value: target.keys})
	}
	if s.policy != nil && !s.policy.IsEmpty() {
		pc.And = append(pc.And, s.policy)
	}
	var userAST *filter.FilterAST
	if len(target.keys) == 0 {
		userAST = target.filter.AST()
	}
	ast, err := filter.ApplyPolicyConstraints(r.filterSchema, userAST, pc)
	if err != nil {
		return "", nil, err
	}
	res, err := filter.NewSQLBuilder(r.filterSchema, d, filter.WithStartParam(offset),
		filter.WithRelationScope(filter.RelationScope{Tenant: s.tenant, Related: s.related})).Build(ast)
	if err != nil {
		return "", nil, err
	}
	return res.Where, res.Args, nil
}

// RelationStatements renders the patch's relation changes for the rows
// Owner returned, and Current when the plan has it — ownerKeys read from
// them with Key, never the requested keys — tenant-scoped as the update was.
// Run them after those statements, in their transaction; the executors
// (parsepgx.Update, parsesql.Update) do. No owners need no statements.
//
// A replace or an add writes one junction row per owner and value, so a
// ByFilter update that matches many rows writes as many pairs as its rows
// times the patch's values: bound the target as the batch it is.
func (r *Resource[T]) RelationStatements(plan *UpdatePlan, ownerKeys []any) ([]patch.RenderedRelationStatement, error) {
	if err := r.ownsUpdatePlan(plan); err != nil {
		return nil, err
	}
	if len(plan.relations) == 0 || len(ownerKeys) == 0 {
		return nil, nil
	}
	var out []patch.RenderedRelationStatement
	for _, op := range plan.relations {
		opts := patch.RenderRelationOpOptions{Owner: patch.RelationOwner{Table: r.table, Key: r.pk, Tenant: r.tenant}}
		opts.TenantValue = plan.tenant
		if op.Schema.TargetTable != "" {
			field := plan.fields.Fields[op.Field]
			opts.TargetVisible = func(alias string, bind func(any) string) (string, error) {
				return filter.VisibleRelated(field, plan.dialect, alias, plan.tenant, plan.related, bind)
			}
		}
		stmts, err := patch.RenderRelationOp(op, plan.dialect, ownerKeys, opts)
		if err != nil {
			return nil, fmt.Errorf("%s: relation %q: %w", r.table, op.Field, err)
		}
		out = append(out, stmts...)
	}
	return out, nil
}

// Reread is the statement that reads the rows with ownerKeys — keys passed
// to RelationStatements — as Columns again: run it after those statements,
// in their transaction, and its rows replace the ones Owner and Current
// returned, in no particular order. A computed column (ReadExpr) may read a
// relation's pairs, which Owner and Current read before the statements
// changed them. Nil when the plan changes no relation, the resource has no
// computed column or there are no keys: the rows already read are current.
//
// The rows are held to the update's tenant and nothing else: the keys are
// the rows the update wrote, which RETURNING reports whatever their deleted
// column or the policy now say.
func (r *Resource[T]) Reread(plan *UpdatePlan, ownerKeys []any) (*Statement, error) {
	if err := r.ownsUpdatePlan(plan); err != nil {
		return nil, err
	}
	if len(plan.relations) == 0 || r.readExprs == nil || len(ownerKeys) == 0 {
		return nil, nil
	}
	exprs, err := r.computedSQL(plan.dialect)
	if err != nil {
		return nil, err
	}
	pc := filter.BuildBaseConstraints(r.tenant, plan.tenant, "", false)
	pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Field: r.pk, Operator: filter.OpIn, Value: ownerKeys})
	ast, err := filter.ApplyPolicyConstraints(r.filterSchema, nil, pc)
	if err != nil {
		return nil, err
	}
	where, err := filter.NewSQLBuilder(r.filterSchema, plan.dialect).Build(ast)
	if err != nil {
		return nil, err
	}
	return &Statement{
		SQL:  "SELECT " + strings.Join(r.columns(plan.dialect, exprs), ", ") + " FROM " + plan.dialect.QuoteIdent(r.table) + " WHERE " + where.Where,
		Args: where.Args,
	}, nil
}

// ownsUpdatePlan refuses a plan r did not build: its relation changes name
// another table's fields.
func (r *Resource[T]) ownsUpdatePlan(plan *UpdatePlan) error {
	if plan == nil {
		return fmt.Errorf("parse: %s: nil update plan", r.table)
	}
	if plan.table != r.table {
		return fmt.Errorf("parse: %s: update plan built for %q", r.table, plan.table)
	}
	return nil
}

func (r *Resource[T]) updateOptions(opts []UpdateOption) (updateConfig, []patch.PatchSQLOption, error) {
	var cfg updateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	var patchOpts []patch.PatchSQLOption
	if cfg.stampColumn != "" {
		if _, ok := r.tableColumns[cfg.stampColumn]; !ok || !core.IsIdentifier(cfg.stampColumn) {
			return updateConfig{}, nil, fmt.Errorf("%s: stamp column %q is not a column of the resource", r.table, cfg.stampColumn)
		}
		if r.computed(cfg.stampColumn) {
			return updateConfig{}, nil, r.readExprs.writeError(cfg.stampColumn, "is stamped")
		}
		if cfg.stampSQL == "" && cfg.stampValue.IsZero() {
			return updateConfig{}, nil, fmt.Errorf("%s: stamp column %q has no time", r.table, cfg.stampColumn)
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
			return updateConfig{}, nil, fmt.Errorf("%s: increment column %q is not a column of the resource", r.table, col)
		}
		if r.computed(col) {
			return updateConfig{}, nil, r.readExprs.writeError(col, "is incremented")
		}
		if core.TypeOf(t).Type != core.FieldTypeInt {
			return updateConfig{}, nil, fmt.Errorf("%s: increment column %q is not an integer column", r.table, col)
		}
		patchOpts = append(patchOpts, patch.WithIncrement(col))
	}
	if cfg.skipUnchanged {
		patchOpts = append(patchOpts, patch.WithChangePredicate())
	}
	return cfg, patchOpts, nil
}

// checkAssignments refuses a statement of builder that assigns the key or
// the tenant column. NewResource refuses a patch path over either, so only
// a server column (Stamp, Increment) can name one here; both builders hold
// every write to the rule checkIdentityPatches states.
func (r *Resource[T]) checkAssignments(assignments []patch.ColumnAssignment, builder string) error {
	for _, assignment := range assignments {
		if assignment.Column == r.pk || (r.tenant != "" && assignment.Column == r.tenant) {
			return fmt.Errorf("%s: %s cannot change identity column %s", r.table, builder, assignment.Column)
		}
	}
	return nil
}
