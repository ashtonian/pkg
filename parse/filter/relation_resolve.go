package filter

import (
	"context"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// ResolveOption configures ResolveMemberships' relation scoping, as the SQL
// builder's options scope relation subqueries.
type ResolveOption func(*resolveConfig)

type resolveConfig struct {
	scope   relationScope
	related relatedPolicies
}

// ResolveRelationScope scopes the relations ResolveMemberships resolves
// (RelationScope). A lookup reads one tenant's rows, so a RowTenant is
// refused: resolve each tenant's rows with its Tenant.
func ResolveRelationScope(s RelationScope) ResolveOption {
	return func(c *resolveConfig) { c.scope, c.related = s.tenancy(), s.policies() }
}

func hasRelation(n *FilterNode) bool {
	if n == nil {
		return false
	}
	if n.Field != nil && n.Field.Relation != nil {
		return true
	}
	return slices.ContainsFunc(n.Children, hasRelation)
}

// relation resolves a relation predicate to a membership of the owner's
// correlation fields: `$some` is the fields among the related rows'
// correlation keys, `$none` their negation or a NULL field (a NULL foreign
// key relates no row, so NOT EXISTS admits it). A key of several columns
// is a key set (OpKeyIn) over the owner's fields, as the correlated
// EXISTS equates every column.
func (r *memberResolver) relation(ctx context.Context, n *FilterNode) (*FilterNode, error) {
	f := n.Field
	if r.lookup == nil {
		return nil, invalidRelation(f, "needs a membership lookup to be evaluated outside SQL")
	}
	if err := checkResolvable(f); err != nil {
		return nil, err
	}
	by := f.RelatedBy
	if len(by) != len(f.Relation.SourceColumns) {
		return nil, invalidRelation(f, "has no owner correlation field for each key column to evaluate outside SQL")
	}
	if f.Related != nil && n.Op != OpSome && n.Op != OpNone {
		sh, err := relatedShorthand(n)
		if err != nil {
			return nil, err
		}
		n = sh
	}
	l, some, err := r.relationLookup(ctx, n)
	if err != nil {
		return nil, err
	}
	in, err := r.correlated(ctx, by, l)
	if err != nil {
		return nil, err
	}
	if some {
		return in, nil
	}
	none := []*FilterNode{{Op: OpNot, Children: []*FilterNode{in}}}
	for _, field := range by {
		none = append(none, &FilterNode{Field: field, Op: OpNull})
	}
	return &FilterNode{Op: OpOr, Children: none}, nil
}

// correlated is the owner's correlation fields among the keys l projects:
// one field `$in` its values, several a key set of their keys, and no keys
// an empty `$in`, which admits no row.
func (r *memberResolver) correlated(ctx context.Context, by []*FieldSchema, l *Lookup) (*FilterNode, error) {
	if len(by) == 1 {
		members, _, err := r.values(ctx, by[0], l)
		if err != nil {
			return nil, err
		}
		return &FilterNode{Field: by[0], Op: OpIn, Value: members}, nil
	}
	keys, err := r.keys(ctx, by, l)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return &FilterNode{Field: by[0], Op: OpIn, Value: []any{}}, nil
	}
	return &FilterNode{Op: OpKeyIn, Keys: &KeySet{Table: by[0].Table, Fields: by, Keys: keys}}, nil
}

// checkResolvable refuses, outside SQL, a relation that does not check.
func checkResolvable(f *FieldSchema) error {
	if err := f.Relation.Check(); err != nil {
		return invalidRelation(f, err.Error())
	}
	return nil
}

// projecting is l projecting columns: Project for one, ProjectKey for
// several.
func projecting(l *Lookup, columns []string) *Lookup {
	if len(columns) == 1 {
		l.Project, l.ProjectKey = columns[0], nil
	} else {
		l.Project, l.ProjectKey = "", columns
	}
	return l
}

// memberOf is the condition that columns are among what l projects:
// WhereMember for one column, WhereKeyMember for several.
func memberOf(columns []string, l *Lookup) *LookupCond {
	if len(columns) == 1 {
		return WhereMember(columns[0], l)
	}
	return WhereKeyMember(columns, l)
}

// relationLookup is the lookup of the related rows' correlation values a
// relation predicate tests, and whether the predicate holds for owners
// among them (else for owners outside them).
func (r *memberResolver) relationLookup(ctx context.Context, n *FilterNode) (*Lookup, bool, error) {
	f := n.Field
	rel := f.Relation
	var cond *LookupCond
	var filter *ParsedFilter
	some := n.Op == OpSome
	var err error
	if f.Related == nil {
		if rel.Policy {
			return nil, false, fmt.Errorf("policy-scoped relation has no target schema")
		}
		// A pair table compared by key: the predicate's comparison is on
		// the pair's target column.
		cond, some, err = keyCondition(f, n)
		if err != nil {
			return nil, false, err
		}
	} else {
		var nested *FilterNode
		if len(n.Children) > 0 {
			nested = n.Children[0]
		}
		if rel.Policy {
			if nested, err = r.cfg.related.on(f, nested); err != nil {
				return nil, false, err
			}
		}
		nested, err = r.resolve(ctx, nested) // the target's own relations, depth 2
		if err != nil {
			return nil, false, err
		}
		if nested != nil {
			filter = NewParsedFilter(&FilterAST{Root: nested})
		}
	}

	if rel.TargetTable == "" {
		where, scopeErr := r.scoped(rel.JoinTable, rel.SourceColumns, rel.TenantColumn, rel.DeletedColumn, cond)
		if scopeErr != nil {
			return nil, false, scopeErr
		}
		return projecting(&Lookup{Table: rel.JoinTable, Where: where, Filter: filter}, rel.SourceColumns), some, nil
	}
	targetWhere, err := r.scoped(rel.TargetTable, rel.TargetKeys, rel.TargetTenant, rel.TargetDeleted, nil)
	if err != nil {
		return nil, false, err
	}
	target := projecting(&Lookup{Table: rel.TargetTable, Where: targetWhere, Filter: filter}, rel.TargetKeys)
	targets := memberOf(rel.TargetColumns, target).typed(rel.TargetTypes)
	if cond != nil {
		targets = WhereAll(cond, targets)
	}
	where, err := r.scoped(rel.JoinTable, rel.SourceColumns, rel.TenantColumn, rel.DeletedColumn, targets)
	if err != nil {
		return nil, false, err
	}
	return projecting(&Lookup{Table: rel.JoinTable, Where: where}, rel.SourceColumns), some, nil
}

// scoped is a lookup condition over table's live rows of the tenant, no
// projected column NULL, and cond.
func (r *memberResolver) scoped(table string, project []string, tenant, deleted string, cond *LookupCond) (*LookupCond, error) {
	conds := make([]*LookupCond, 0, len(project)+3)
	for _, column := range project {
		conds = append(conds, WhereNotNull(column))
	}
	if tenant != "" && !r.cfg.scope.unscoped {
		if r.cfg.scope.tenant == nil {
			return nil, fmt.Errorf("relation table %s is tenant-scoped (%s): bind a tenant or opt out explicitly with an unscoped-relations option", table, tenant)
		}
		conds = append(conds, WhereEq(tenant, r.cfg.scope.tenant))
	}
	if deleted != "" {
		conds = append(conds, WhereNull(deleted))
	}
	if cond != nil {
		conds = append(conds, cond)
	}
	return WhereAll(conds...), nil
}

// keyCondition is the lookup condition a key comparison on a pair table
// makes on its one target column, typed as the relation types it, and
// whether owners among the matching pairs satisfy it (`$eq`, `$in`,
// `$not_null`) or owners outside them (`$ne`, `$nin`, `$null`). A key of
// several columns takes only the NULL tests, as in SQL (checkPairKeyOp).
func keyCondition(f *FieldSchema, n *FilterNode) (*LookupCond, bool, error) {
	if err := checkPairKeyOp(f, n.Op); err != nil {
		return nil, false, err
	}
	column, types := f.Relation.TargetColumns[0], f.Relation.TargetTypes[:1]
	switch n.Op {
	case OpEq:
		return WhereEq(column, n.Value).typed(types), true, nil
	case OpNe:
		return WhereEq(column, n.Value).typed(types), false, nil
	case OpIn, OpNin:
		vs, ok := core.AnySlice(n.Value)
		if !ok {
			return nil, false, fmt.Errorf("$%s requires an array value, got %T", n.Op, n.Value)
		}
		return WhereIn(column, vs...).typed(types), n.Op == OpIn, nil
	case OpNotNull:
		return nil, true, nil
	case OpNull:
		return nil, false, nil
	}
	return nil, false, invalidRelation(f, fmt.Sprintf("compares its key with $%s, which only SQL evaluates", n.Op))
}
