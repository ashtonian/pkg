package patch

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// RenderedRelationStatement is one DELETE or INSERT emitted for a
// relation patch op. The repository decorator runs each statement on
// the open transaction in slice order; statements within one rendered
// op are atomic with the owner UPDATE that produced ownerPKs.
type RenderedRelationStatement struct {
	SQL  string
	Args []any
}

// RenderRelationOpOptions configures RenderRelationOp.
type RenderRelationOpOptions struct {
	// TenantValue is bound into every junction statement as the value of
	// RelationSchema.TenantColumn. Required when the schema declares a
	// TenantColumn — rendering fails without it unless Unscoped is set
	//. Ignored for unscoped junctions.
	TenantValue any
	// Unscoped deliberately renders a tenant-scoped junction without a
	// tenant predicate. Never set it on a request path.
	Unscoped bool
	// Owner names the owner table, key and tenant column. A many-to-many
	// with a target (RelationSchema.TargetTable) needs it: its inserts
	// select the owner rows and the visible target rows instead of binding
	// raw values, so no pair names a target the request cannot see.
	Owner RelationOwner
	// TargetVisible renders the predicate (starting " AND ", or "") that
	// holds the target rows under alias to those the request may relate,
	// binding values with bind (filter.VisibleRelated). Required with a
	// TargetTable: inserts pair only visible targets, and deletes remove
	// only pairs to them.
	TargetVisible func(alias string, bind func(any) string) (string, error)
}

// RelationOwner is the table a relation's owners are rows of.
type RelationOwner struct {
	Table, Key, Tenant string
}

// relationTarget is the alias the guarded statements read target rows by.
const relationTarget = "target__"

// RenderRelationOp turns a parsed RelationPatchOp into the dialect-
// specific SQL statements that apply the membership change to the
// junction table for the supplied owner PK set.
//
// The output is a list of {SQL, Args} pairs the caller executes in
// order on an open transaction. .
//
// Statement shapes:
//
//	replace, len(values) > 0:
//	  DELETE FROM j WHERE source = ANY/IN (pks) AND target NOT IN (vals) [AND tenant = t]
//	  INSERT INTO j (source, target [, tenant]) VALUES (pk×val) ON CONFLICT DO NOTHING
//	replace, len(values) == 0:
//	  DELETE FROM j WHERE source = ANY/IN (pks) [AND tenant = t]                    -- clear
//	add:
//	  INSERT INTO j (source, target [, tenant]) VALUES (pk×val) ON CONFLICT DO NOTHING
//	remove:
//	  DELETE FROM j WHERE source = ANY/IN (pks) AND target IN (vals) [AND tenant = t]
//
// Cross-product semantics: a patch supplying values [a,b,c] applied to
// PK set [p1,p2] inserts six rows (p1,a),(p1,b),(p1,c),(p2,a),…,(p2,c).
// "Replace" diffs per (pk, target) — each PK in ownerPKs gets exactly
// the supplied value set. Multi-PK patch is allowed but uncommon; the
// usual call shape is one PK at a time.
func RenderRelationOp(op RelationPatchOp, dialect dialect.Dialect, ownerPKs []any, opts RenderRelationOpOptions) ([]RenderedRelationStatement, error) {
	if op.Schema == nil {
		return nil, fmt.Errorf("relation patch op has nil schema")
	}
	if len(ownerPKs) == 0 {
		// No rows matched the owner UPDATE — nothing to mutate on the
		// junction. Returning empty (rather than erroring) keeps the
		// caller from having to special-case zero-row patches.
		return nil, nil
	}

	rel := op.Schema
	if rel.TenantColumn != "" && opts.TenantValue == nil && !opts.Unscoped {
		return nil, fmt.Errorf("relation junction %s is tenant-scoped (%s): set TenantValue or Unscoped", rel.JoinTable, rel.TenantColumn)
	}
	hasTenant := rel.TenantColumn != "" && !opts.Unscoped
	if rel.TargetTable != "" {
		return renderGuarded(op, dialect, ownerPKs, opts, hasTenant)
	}

	switch op.Verb {
	case RelationVerbReplace:
		return renderReplace(rel, dialect, ownerPKs, op.Values, hasTenant, opts.TenantValue)
	case RelationVerbAdd:
		return renderInsert(rel, dialect, ownerPKs, op.Values, hasTenant, opts.TenantValue), nil
	case RelationVerbRemove:
		return []RenderedRelationStatement{
			renderDeleteByTargets(rel, dialect, ownerPKs, op.Values, hasTenant, opts.TenantValue),
		}, nil
	default:
		return nil, fmt.Errorf("unknown relation verb %q", op.Verb)
	}
}

func renderReplace(rel *core.RelationSchema, d dialect.Dialect, pks []any, vals []any, hasTenant bool, tenant any) ([]RenderedRelationStatement, error) {
	stmts := []RenderedRelationStatement{}
	if len(vals) == 0 {
		// Clear: delete every junction row for the owners.
		stmts = append(stmts, renderDeleteAll(rel, d, pks, hasTenant, tenant))
		return stmts, nil
	}
	// Diff: delete any current rows whose target is not in the new set,
	// then insert the new set with ON CONFLICT DO NOTHING.
	stmts = append(stmts, renderDeleteNotIn(rel, d, pks, vals, hasTenant, tenant))
	stmts = append(stmts, renderInsert(rel, d, pks, vals, hasTenant, tenant)...)
	return stmts, nil
}

func renderDeleteAll(rel *core.RelationSchema, d dialect.Dialect, pks []any, hasTenant bool, tenant any) RenderedRelationStatement {
	var sb strings.Builder
	args := make([]any, 0, len(pks)+1)
	idx := 0

	sb.WriteString("DELETE FROM ")
	sb.WriteString(rel.JoinTable)
	sb.WriteString(" WHERE ")
	sb.WriteString(rel.SourceColumn)
	sb.WriteString(" IN (")
	for i, pk := range pks {
		if i > 0 {
			sb.WriteString(", ")
		}
		idx++
		sb.WriteString(d.Placeholder(idx))
		args = append(args, pk)
	}
	sb.WriteByte(')')
	if hasTenant {
		idx++
		sb.WriteString(" AND ")
		sb.WriteString(rel.TenantColumn)
		sb.WriteString(" = ")
		sb.WriteString(d.Placeholder(idx))
		args = append(args, tenant)
	}
	return RenderedRelationStatement{SQL: sb.String(), Args: args}
}

func renderDeleteNotIn(rel *core.RelationSchema, d dialect.Dialect, pks []any, vals []any, hasTenant bool, tenant any) RenderedRelationStatement {
	var sb strings.Builder
	args := make([]any, 0, len(pks)+len(vals)+1)
	idx := 0

	sb.WriteString("DELETE FROM ")
	sb.WriteString(rel.JoinTable)
	sb.WriteString(" WHERE ")
	sb.WriteString(rel.SourceColumn)
	sb.WriteString(" IN (")
	for i, pk := range pks {
		if i > 0 {
			sb.WriteString(", ")
		}
		idx++
		sb.WriteString(d.Placeholder(idx))
		args = append(args, pk)
	}
	sb.WriteString(") AND ")
	sb.WriteString(rel.TargetColumn)
	sb.WriteString(" NOT IN (")
	for i, v := range vals {
		if i > 0 {
			sb.WriteString(", ")
		}
		idx++
		sb.WriteString(d.Placeholder(idx))
		args = append(args, v)
	}
	sb.WriteByte(')')
	if hasTenant {
		idx++
		sb.WriteString(" AND ")
		sb.WriteString(rel.TenantColumn)
		sb.WriteString(" = ")
		sb.WriteString(d.Placeholder(idx))
		args = append(args, tenant)
	}
	return RenderedRelationStatement{SQL: sb.String(), Args: args}
}

func renderDeleteByTargets(rel *core.RelationSchema, d dialect.Dialect, pks []any, vals []any, hasTenant bool, tenant any) RenderedRelationStatement {
	var sb strings.Builder
	args := make([]any, 0, len(pks)+len(vals)+1)
	idx := 0

	sb.WriteString("DELETE FROM ")
	sb.WriteString(rel.JoinTable)
	sb.WriteString(" WHERE ")
	sb.WriteString(rel.SourceColumn)
	sb.WriteString(" IN (")
	for i, pk := range pks {
		if i > 0 {
			sb.WriteString(", ")
		}
		idx++
		sb.WriteString(d.Placeholder(idx))
		args = append(args, pk)
	}
	sb.WriteString(") AND ")
	sb.WriteString(rel.TargetColumn)
	sb.WriteString(" IN (")
	for i, v := range vals {
		if i > 0 {
			sb.WriteString(", ")
		}
		idx++
		sb.WriteString(d.Placeholder(idx))
		args = append(args, v)
	}
	sb.WriteByte(')')
	if hasTenant {
		idx++
		sb.WriteString(" AND ")
		sb.WriteString(rel.TenantColumn)
		sb.WriteString(" = ")
		sb.WriteString(d.Placeholder(idx))
		args = append(args, tenant)
	}
	return RenderedRelationStatement{SQL: sb.String(), Args: args}
}

// renderInsert emits a single VALUES (pk×val) INSERT with ON CONFLICT
// DO NOTHING. Returns a slice (rather than one statement) so callers
// can split into chunks if a future RFC introduces a per-statement
// row cap; today it always returns exactly one row when work is needed.
func renderInsert(rel *core.RelationSchema, d dialect.Dialect, pks []any, vals []any, hasTenant bool, tenant any) []RenderedRelationStatement {
	if len(pks) == 0 || len(vals) == 0 {
		return nil
	}
	var sb strings.Builder
	args := make([]any, 0, len(pks)*len(vals)*2+1)
	idx := 0

	sb.WriteString("INSERT INTO ")
	sb.WriteString(rel.JoinTable)
	sb.WriteString(" (")
	sb.WriteString(rel.SourceColumn)
	sb.WriteString(", ")
	sb.WriteString(rel.TargetColumn)
	if hasTenant {
		sb.WriteString(", ")
		sb.WriteString(rel.TenantColumn)
	}
	sb.WriteString(") VALUES ")

	first := true
	for _, pk := range pks {
		for _, v := range vals {
			if !first {
				sb.WriteString(", ")
			}
			first = false
			sb.WriteByte('(')
			idx++
			sb.WriteString(d.Placeholder(idx))
			args = append(args, pk)
			sb.WriteString(", ")
			idx++
			sb.WriteString(d.Placeholder(idx))
			args = append(args, v)
			if hasTenant {
				sb.WriteString(", ")
				idx++
				sb.WriteString(d.Placeholder(idx))
				args = append(args, tenant)
			}
			sb.WriteByte(')')
		}
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")

	return []RenderedRelationStatement{{SQL: sb.String(), Args: args}}
}

// renderGuarded renders a many-to-many-with-target membership change
// : pairs are inserted from the owner rows and the visible
// target rows, and deletes reach only pairs whose target is visible — a
// replace sets the visible related set, leaving pairs the request cannot
// see alone.
func renderGuarded(op RelationPatchOp, d dialect.Dialect, pks []any, opts RenderRelationOpOptions, hasTenant bool) ([]RenderedRelationStatement, error) {
	rel := op.Schema
	if opts.Owner.Table == "" || opts.Owner.Key == "" || opts.TargetVisible == nil {
		return nil, fmt.Errorf("relation %s: a membership change through a target needs the owner table and the target's visibility", op.Field)
	}
	var stmts []RenderedRelationStatement
	switch op.Verb {
	case RelationVerbAdd:
	case RelationVerbRemove:
		st, err := guardedDelete(rel, d, pks, op.Values, false, opts, hasTenant)
		return []RenderedRelationStatement{st}, err
	case RelationVerbReplace:
		st, err := guardedDelete(rel, d, pks, op.Values, true, opts, hasTenant)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, st)
	default:
		return nil, fmt.Errorf("unknown relation verb %q", op.Verb)
	}
	if len(op.Values) == 0 {
		return stmts, nil
	}
	ins, err := guardedInsert(rel, d, pks, op.Values, opts, hasTenant)
	if err != nil {
		return nil, err
	}
	return append(stmts, ins), nil
}

// guardedDelete removes the owners' pairs whose target is visible and
// among vals (keep false), or not among them (keep true: a replace).
func guardedDelete(rel *core.RelationSchema, d dialect.Dialect, pks, vals []any, keep bool, opts RenderRelationOpOptions, hasTenant bool) (RenderedRelationStatement, error) {
	var sb strings.Builder
	var args []any
	bind := func(v any) string {
		args = append(args, v)
		return d.Placeholder(len(args))
	}
	sb.WriteString("DELETE FROM " + rel.JoinTable + " WHERE " + rel.SourceColumn + " IN (" + binds(pks, bind) + ")")
	if hasTenant {
		sb.WriteString(" AND " + rel.TenantColumn + " = " + bind(opts.TenantValue))
	}
	switch {
	case !keep:
		sb.WriteString(" AND " + rel.TargetColumn + " IN (" + binds(vals, bind) + ")")
	case len(vals) > 0:
		sb.WriteString(" AND " + rel.TargetColumn + " NOT IN (" + binds(vals, bind) + ")")
	}
	visible, err := opts.TargetVisible(relationTarget, bind)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" AND " + rel.TargetColumn + " IN (SELECT " + relationTarget + "." + rel.TargetKey + " FROM " + rel.TargetTable + " " + relationTarget)
	if visible != "" {
		sb.WriteString(" WHERE " + strings.TrimPrefix(visible, " AND "))
	}
	sb.WriteByte(')')
	return RenderedRelationStatement{SQL: sb.String(), Args: args}, nil
}

// guardedInsert pairs every owner with every visible target among vals:
//
//	INSERT INTO j (source, target [, tenant])
//	SELECT owner__.key, target__.key [, owner__.tenant | target__.tenant]
//	FROM owners owner__, targets target__
//	WHERE owner__.key IN (pks) [AND owner__.tenant = t] AND target__.key IN (vals) <visible>
//	ON CONFLICT DO NOTHING
//
// Every value is compared with a column, so it binds as that column's
// type on every driver and protocol.
func guardedInsert(rel *core.RelationSchema, d dialect.Dialect, pks, vals []any, opts RenderRelationOpOptions, hasTenant bool) (RenderedRelationStatement, error) {
	var sb strings.Builder
	var args []any
	bind := func(v any) string {
		args = append(args, v)
		return d.Placeholder(len(args))
	}
	const owner = "owner__"
	cols := rel.SourceColumn + ", " + rel.TargetColumn
	sel := owner + "." + opts.Owner.Key + ", " + relationTarget + "." + rel.TargetKey
	if hasTenant {
		cols += ", " + rel.TenantColumn
		switch {
		case opts.Owner.Tenant != "":
			sel += ", " + owner + "." + opts.Owner.Tenant
		case rel.TargetTenant != "":
			sel += ", " + relationTarget + "." + rel.TargetTenant
		default:
			return RenderedRelationStatement{}, fmt.Errorf("relation junction %s is tenant-scoped, but neither its owners nor its targets hold a tenant", rel.JoinTable)
		}
	}
	sb.WriteString("INSERT INTO " + rel.JoinTable + " (" + cols + ") SELECT " + sel)
	sb.WriteString(" FROM " + opts.Owner.Table + " " + owner + ", " + rel.TargetTable + " " + relationTarget)
	sb.WriteString(" WHERE " + owner + "." + opts.Owner.Key + " IN (" + binds(pks, bind) + ")")
	if hasTenant && opts.Owner.Tenant != "" {
		sb.WriteString(" AND " + owner + "." + opts.Owner.Tenant + " = " + bind(opts.TenantValue))
	}
	sb.WriteString(" AND " + relationTarget + "." + rel.TargetKey + " IN (" + binds(vals, bind) + ")")
	visible, err := opts.TargetVisible(relationTarget, bind)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(visible)
	sb.WriteString(" ON CONFLICT DO NOTHING")
	return RenderedRelationStatement{SQL: sb.String(), Args: args}, nil
}

func binds(vs []any, bind func(any) string) string {
	phs := make([]string, len(vs))
	for i, v := range vs {
		phs[i] = bind(v)
	}
	return strings.Join(phs, ", ")
}
