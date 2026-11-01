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
	return len(t.keys) == 0 && (t.filter == nil || t.filter.AST == nil || t.filter.AST.Root == nil)
}

// ErrNoTarget is an update built without keys or a non-empty filter.
var ErrNoTarget = errors.New("parse: an update needs a key or a filter")

// ErrUpdateScope is an update built with a widening Scope: an update
// reaches only its own tenant's live rows, so Governance and IncludeDeleted
// do not apply to it.
var ErrUpdateScope = errors.New("parse: an update reaches only its tenant's live rows")

// Dialect is a dialect both the filter and the patch compiler speak, as
// filter.PostgresDialect and filter.SQLiteDialect do.
type Dialect interface {
	filter.Dialect
	patch.SQLDialect
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

// UpdatePlan is a patch compiled for one dialect.
type UpdatePlan struct {
	// Owner returns the target rows' Columns as the update leaves them:
	// an UPDATE … RETURNING when the patch writes a column; a SELECT that
	// locks the rows (Dialect.RowLock) when it changes relations alone; a
	// plain SELECT — a scoped read — when it changes nothing. It matches only
	// rows the scope admits. Under SkipUnchanged the UPDATE returns only the
	// rows it changed.
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
	// Writes reports whether Owner changes rows.
	Writes bool

	relations []patch.RelationPatchOp
	dialect   Dialect
	table     string
	tenant    string
	related   map[string]*filter.PolicyConstraints
}

// Dialect is the dialect the plan's statements are written for.
func (p *UpdatePlan) Dialect() Dialect { return p.dialect }

// HasRelations reports whether the patch changes relation memberships:
// run Current when set, Owner, then RelationStatements in one transaction.
func (p *UpdatePlan) HasRelations() bool { return len(p.relations) > 0 }

// BuildUpdate compiles a patch into the statement that applies it to target
// under scope s: the patch's SET clause, then a WHERE of the target, the
// tenant floor, live rows only and s.Policy, then RETURNING the resource's
// Columns; SkipUnchanged also ANDs the patch's change predicate into the
// WHERE. A tenant-scoped resource needs s.Tenant (ErrTenantRequired); a zero
// Target is ErrNoTarget; Governance or IncludeDeleted is ErrUpdateScope.
func (r *Resource[T]) BuildUpdate(d Dialect, p *patch.ParsedPatch, target Target, s Scope, opts ...UpdateOption) (*UpdatePlan, error) {
	if target.empty() {
		return nil, fmt.Errorf("%s: %w", r.table, ErrNoTarget)
	}
	if r.tenant != "" && s.Tenant == "" {
		return nil, fmt.Errorf("%s: %w", r.table, ErrTenantRequired)
	}
	if s.Governance != nil || s.IncludeDeleted {
		return nil, fmt.Errorf("%s: %w", r.table, ErrUpdateScope)
	}
	var cfg updateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	var patchOpts []patch.PatchSQLOption
	if cfg.stampColumn != "" {
		if _, ok := r.tableColumns[cfg.stampColumn]; !ok || !core.IsIdentifier(cfg.stampColumn) {
			return nil, fmt.Errorf("%s: stamp column %q is not a column of the resource", r.table, cfg.stampColumn)
		}
		if cfg.stampSQL == "" && cfg.stampValue.IsZero() {
			return nil, fmt.Errorf("%s: stamp column %q has no time", r.table, cfg.stampColumn)
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
			return nil, fmt.Errorf("%s: increment column %q is not a column of the resource", r.table, col)
		}
		if core.TypeOf(t).Type != core.FieldTypeInt {
			return nil, fmt.Errorf("%s: increment column %q is not an integer column", r.table, col)
		}
		patchOpts = append(patchOpts, patch.WithIncrement(col))
	}
	if cfg.skipUnchanged {
		patchOpts = append(patchOpts, patch.WithChangePredicate())
	}
	set, err := patch.BuildPatchSQL(p, d, patchOpts...)
	if err != nil {
		return nil, err
	}

	where, args, err := r.updateWhere(d, target, s, set.ParamCount)
	if err != nil {
		return nil, err
	}
	columns := strings.Join(r.Columns(d), ", ")
	plan := &UpdatePlan{relations: set.RelationOps, dialect: d, table: r.table, tenant: s.Tenant, related: s.Related}
	switch {
	case set.SetClause != "":
		guarded := where
		if cfg.skipUnchanged {
			// where's root is a group, which renders parenthesized, or one
			// condition, so the predicate ANDs onto all of it.
			guarded += " AND " + set.ChangePredicate
			current, currentArgs, err := r.updateWhere(d, target, s, 0)
			if err != nil {
				return nil, err
			}
			plan.Current = &Statement{SQL: "SELECT " + columns + " FROM " + r.table + " WHERE " + current + d.RowLock(), Args: currentArgs}
		}
		plan.Owner = Statement{
			SQL:  "UPDATE " + r.table + " SET " + set.SetClause + " WHERE " + guarded + " RETURNING " + columns,
			Args: append(set.Args, args...),
		}
		plan.Writes = true
	case len(set.RelationOps) > 0:
		// No column changes: lock the owners so concurrent replacements of
		// one relation cannot interleave their DELETE and INSERT.
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + r.table + " WHERE " + where + d.RowLock(), Args: args}
		plan.Writes = true
	default:
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + r.table + " WHERE " + where, Args: args}
	}
	return plan, nil
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

// updateWhere is the WHERE every update statement carries, its placeholders
// after the SET clause's offset ones.
func (r *Resource[T]) updateWhere(d filter.Dialect, target Target, s Scope, offset int) (string, []any, error) {
	pc := filter.BuildBaseConstraints(r.tenant, s.Tenant, r.deleted, false)
	var userAST *filter.FilterAST
	switch {
	case len(target.keys) == 1:
		pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Field: r.pk, Operator: filter.OpEq, Value: target.keys[0]})
	case len(target.keys) > 1:
		pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Field: r.pk, Operator: filter.OpIn, Value: target.keys})
	default:
		userAST = target.filter.AST
	}
	if s.Policy != nil && !s.Policy.IsEmpty() {
		pc.And = append(pc.And, s.Policy)
	}
	ast, err := filter.ApplyPolicyConstraints(r.filterSchema, userAST, pc)
	if err != nil {
		return "", nil, err
	}
	opts := []filter.SQLBuilderOption{filter.WithStartParam(offset), filter.WithRelatedPolicies(s.Related)}
	if s.Tenant != "" {
		opts = append(opts, filter.WithTenant(s.Tenant))
	}
	res, err := filter.NewSQLBuilder(r.filterSchema, d, opts...).Build(ast)
	if err != nil {
		return "", nil, err
	}
	return res.Where, res.Args, nil
}

// RelationStatements renders the patch's relation changes for the rows
// Owner returned, and Current when the plan has it — ownerKeys read from
// them with Key, never the requested keys — tenant-scoped as the update was.
// Run them after those statements, in their transaction. No owners need no
// statements.
func (r *Resource[T]) RelationStatements(plan *UpdatePlan, ownerKeys []any) ([]patch.RenderedRelationStatement, error) {
	if plan == nil {
		return nil, fmt.Errorf("parse: %s: nil update plan", r.table)
	}
	if plan.table != r.table {
		return nil, fmt.Errorf("parse: %s: update plan built for %q", r.table, plan.table)
	}
	if len(plan.relations) == 0 || len(ownerKeys) == 0 {
		return nil, nil
	}
	var out []patch.RenderedRelationStatement
	for _, op := range plan.relations {
		opts := patch.RenderRelationOpOptions{Owner: patch.RelationOwner{Table: r.table, Key: r.pk, Tenant: r.tenant}}
		if plan.tenant != "" {
			opts.TenantValue = plan.tenant
		}
		if op.Schema.TargetTable != "" {
			field := r.filterSchema.Fields[op.Field]
			opts.TargetVisible = func(alias string, bind func(any) string) (string, error) {
				var tenant any
				if plan.tenant != "" {
					tenant = plan.tenant
				}
				return filter.VisibleRelated(field, plan.dialect, alias, tenant, plan.related, bind)
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
