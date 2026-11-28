package sqlplan

import (
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// Update is a composed update compiled for a dialect: the statements that
// read, judge, lock and write its targets (parse.UpdatePlan describes
// each), and what decides the statements that follow once its owners' keys
// are known (Relations, Reread).
type Update struct {
	Owner    Statement
	Current  *Statement
	Lock     *Statement
	Conflict *Statement
	// Writes reports whether the update changes anything: a column Owner
	// assigns, or a relation membership.
	Writes bool
	// Assigns reports whether Owner is an UPDATE: the patch assigns a
	// column, so the rows it returns are the ones it wrote.
	Assigns bool

	// preconditions are the operations Conflict judges (ConflictError).
	preconditions []patch.Precondition
	relations     []patch.RelationPatchOp
	table         string
}

// ConflictError is the failure of an update whose Conflict statement
// returned op: that operation's E2077, as patch.ApplyValidatedPatch reports
// an operation that does not apply.
func (u *Update) ConflictError(op int64) error {
	for _, pre := range u.preconditions {
		if int64(pre.Op) == op {
			return pre.Err()
		}
	}
	return fmt.Errorf("parse: %s: the conflict statement returned operation %d, which it does not judge", u.table, op)
}

// HasRelations reports whether the patch changes relation memberships,
// which an update changes after Owner, in the same transaction.
func (u *Update) HasRelations() bool { return len(u.relations) > 0 }

// CompileUpdate compiles req for d: the patch's SET clause, then a WHERE of
// req.Where, then RETURNING the resource's Columns; SkipUnchanged also ANDs
// the patch's change predicate into the WHERE. A patch that writes members
// of a JSON document ANDs in the precondition RFC 6902 sets those
// operations as well, and is compiled with Current and Conflict, so a
// target row it does not apply to fails the update rather than being
// skipped.
func CompileUpdate(d filter.Dialect, req backend.UpdateRequest) (*Update, error) {
	res := req.Resource
	sql, err := sqlOf(res)
	if err != nil {
		return nil, err
	}
	update, err := req.Options.Compose(req.Patch)
	if err != nil {
		return nil, err
	}
	err = res.CheckUpdate(update)
	if err != nil {
		return nil, err
	}
	set, err := update.SQL(d, renderOptions(req.Options)...)
	if err != nil {
		return nil, err
	}

	where, args, err := compileWhere(d, res, req.Where, req.Relations, set.ParamCount)
	if err != nil {
		return nil, err
	}
	columns := sql.Projection
	table := d.QuoteIdent(res.Table)
	// A statement that locks rows of a target that can hold several takes
	// them in key order, as a batch's Current does, so two updates over
	// overlapping rows lock in one order and neither holds a row the other
	// waits for while it waits for one the other holds. A dialect without
	// row locks serializes writers in their transaction instead.
	locked := d.RowLock()
	if locked != "" && len(req.Keys) != 1 {
		locked = " ORDER BY " + keyOrder(d, res) + locked
	}
	plan := &Update{relations: set.RelationOps, table: res.Table}
	switch {
	case set.SetClause != "":
		// where's root is a group, which renders parenthesized, or one
		// condition, so each guard ANDs onto all of it.
		guarded := where
		if set.Precondition != "" {
			guarded += " AND " + set.Precondition
		}
		if req.Options.SkipUnchanged {
			guarded += " AND " + set.ChangePredicate
		}
		guardedByCurrent := req.Options.SkipUnchanged || set.Precondition != ""
		// Without Current, the UPDATE would take its own locks in the order
		// its plan reads the rows, so Lock takes them in key order first.
		ordersLocks := d.RowLock() != "" && len(req.Keys) != 1
		if guardedByCurrent || ordersLocks {
			bare, bareArgs, err := compileWhere(d, res, req.Where, req.Relations, 0)
			if err != nil {
				return nil, err
			}
			if guardedByCurrent {
				plan.Current = &Statement{SQL: "SELECT " + columns + " FROM " + table + " WHERE " + bare + locked, Args: bareArgs}
			} else {
				plan.Lock = &Statement{SQL: "SELECT 1 FROM " + table + " WHERE " + bare + locked, Args: bareArgs}
			}
		}
		if set.Precondition != "" {
			plan.Conflict, err = conflictStatement(d, req, set.Preconditions)
			if err != nil {
				return nil, err
			}
			plan.preconditions = set.Preconditions
		}
		plan.Owner = Statement{
			SQL:  "UPDATE " + table + " SET " + set.SetClause + " WHERE " + guarded + " RETURNING " + columns,
			Args: append(set.Args, args...),
		}
		plan.Writes, plan.Assigns = true, true
	case len(set.RelationOps) > 0:
		// No column changes: lock the owners so concurrent replacements of
		// one relation cannot interleave their DELETE and INSERT.
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + table + " WHERE " + where + locked, Args: args}
		plan.Writes = true
	default:
		plan.Owner = Statement{SQL: "SELECT " + columns + " FROM " + table + " WHERE " + where, Args: args}
	}
	return plan, nil
}

// conflictStatement selects, from one target row the patch's preconditions
// do not all hold for, the index of the first operation that fails there.
func conflictStatement(d filter.Dialect, req backend.UpdateRequest, pre []patch.Precondition) (*Statement, error) {
	where, args, err := compileWhere(d, req.Resource, req.Where, req.Relations, 0)
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
	stmt := &Statement{SQL: "SELECT " + firstUnmet + " FROM " + d.QuoteIdent(req.Resource.Table) + " WHERE " + where + " AND NOT " + all + " LIMIT 1", Args: args}
	return stmt, nil
}

// compileWhere is the WHERE an update statement carries: where, the floor a
// list in the update's scope reads ANDed with the target, compiled for d
// with its placeholders after offset ones, reaching related rows under
// scope.
func compileWhere(d filter.Dialect, res *backend.Resource, where *filter.FilterAST, scope filter.RelationScope, offset int) (string, []any, error) {
	built, err := filter.NewSQLBuilder(res.Schema, d, filter.WithStartParam(offset), filter.WithRelationScope(scope)).Build(where)
	if err != nil {
		return "", nil, err
	}
	return built.Where, built.Args, nil
}

// Relations renders the update's relation changes for owners: the keys of
// the rows plan's Owner, and Current when it has it, returned, changed then
// unchanged, normalized (backend.UpdateSink.Keys), never the requested
// keys, tenant-scoped as the update was. An update runs them after those
// statements, in their transaction. No owners need no statements.
//
// A replace or an add writes one junction row per owner and value, so a
// filter-targeted update that matches many rows writes as many pairs as its
// rows times the patch's values: bound the target as the batch it is.
func Relations(d filter.Dialect, req backend.UpdateRequest, plan *Update, owners []core.Key) ([]patch.RenderedRelationStatement, error) {
	if len(plan.relations) == 0 || len(owners) == 0 {
		return nil, nil
	}
	res := req.Resource
	var out []patch.RenderedRelationStatement
	for _, op := range plan.relations {
		opts := patch.RenderRelationOpOptions{Owner: patch.RelationOwner{Table: res.Table, Key: slices.Clone(res.Key), Tenant: res.Tenant}}
		opts.TenantValue = req.Relations.Tenant
		if op.Schema.TargetTable != "" {
			field := res.Schema.Fields[op.Field]
			opts.TargetVisible = func(alias string, bind func(any) string) (string, error) {
				return filter.VisibleRelated(field, d, alias, req.Relations, bind)
			}
		}
		stmts, err := patch.RenderRelationOp(op, d, owners, opts)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: relation %q: %w", res.Table, op.Field, err)
		}
		out = append(out, stmts...)
	}
	return out, nil
}

// Reread is the statement that reads the rows with keys owners, keys
// Relations was given, as the resource's Columns again: an update runs it
// after the relation statements, in their transaction, and its rows
// replace the ones Owner and Current returned, in no particular order. A
// computed column (parse.ReadExpr) may read a relation's pairs, which Owner
// and Current read before the statements changed them. Nil when plan
// changes no relation, the resource has no computed column or there are no
// keys: the rows already read are current.
//
// The rows are held to the update's tenant and nothing else: the keys are
// the rows the update wrote, which RETURNING reports whatever their deleted
// column or the policy now say.
func Reread(d filter.Dialect, req backend.UpdateRequest, plan *Update, owners []core.Key) (*Statement, error) {
	res := req.Resource
	if len(plan.relations) == 0 || !res.Computed || len(owners) == 0 {
		return nil, nil
	}
	sql, err := sqlOf(res)
	if err != nil {
		return nil, err
	}
	pc := filter.BuildBaseConstraints(res.Tenant, req.Relations.Tenant, "", false)
	pc.Constraints = append(pc.Constraints, &filter.PolicyConstraint{Fields: res.Key, Operator: filter.OpIn, Value: owners})
	ast, err := filter.ApplyPolicyConstraints(res.Schema, nil, pc)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: reread: %w", res.Table, err)
	}
	where, err := filter.NewSQLBuilder(res.Schema, d).Build(ast)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: reread: %w", res.Table, err)
	}
	stmt := &Statement{
		SQL:  "SELECT " + sql.Projection + " FROM " + d.QuoteIdent(res.Table) + " WHERE " + where.Where,
		Args: where.Args,
	}
	return stmt, nil
}

// renderOptions are the options an update's SQL is rendered with beyond
// its placeholders' offset: the change predicate, for an update that skips
// the rows it leaves unchanged.
func renderOptions(o backend.UpdateOptions) []patch.PatchSQLOption {
	if o.SkipUnchanged {
		opts := []patch.PatchSQLOption{patch.WithChangePredicate()}
		return opts
	}
	return nil
}
