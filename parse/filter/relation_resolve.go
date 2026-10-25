package filter

import (
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// ResolveOption configures ResolveMemberships' relation scoping, as the SQL
// builder's options scope relation subqueries.
type ResolveOption func(*resolveConfig)

type resolveConfig struct {
	scope   relationScope
	related map[string]*PolicyConstraints
}

// ResolveTenant binds the tenant tenant-scoped relations' related rows are
// held to (WithTenant).
func ResolveTenant(value any) ResolveOption {
	return func(c *resolveConfig) {
		c.scope.tenant = value
		c.scope.hasTenant = value != nil
	}
}

// ResolveUnscopedRelations deliberately resolves tenant-scoped relations
// without a tenant (WithUnscopedRelations). Never use it on a request path.
func ResolveUnscopedRelations() ResolveOption {
	return func(c *resolveConfig) { c.scope.unscoped = true }
}

// ResolveRelatedPolicies supplies the policies of policy-scoped relations'
// target tables (WithRelatedPolicies).
func ResolveRelatedPolicies(policies map[string]*PolicyConstraints) ResolveOption {
	return func(c *resolveConfig) { c.related = policies }
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
// correlation field: `$some` is the field among the related rows'
// correlation values, `$none` its negation or a NULL field (a NULL foreign
// key relates no row, so NOT EXISTS admits it).
func (r *memberResolver) relation(n *FilterNode) (*FilterNode, error) {
	f := n.Field
	if r.lookup == nil {
		return nil, invalidRelation(f, "needs a membership lookup to be evaluated outside SQL")
	}
	if f.RelatedBy == nil {
		return nil, invalidRelation(f, "has no owner correlation field to evaluate outside SQL")
	}
	if f.Related != nil && n.Op != OpSome && n.Op != OpNone {
		sh, err := relatedShorthand(n)
		if err != nil {
			return nil, err
		}
		n = sh
	}
	l, some, err := r.relationLookup(n)
	if err != nil {
		return nil, err
	}
	members, _, err := r.values(f.RelatedBy, l)
	if err != nil {
		return nil, err
	}
	in := &FilterNode{Field: f.RelatedBy, Op: OpIn, Value: members}
	if some {
		return in, nil
	}
	return &FilterNode{Op: OpOr, Children: []*FilterNode{
		{Op: OpNot, Children: []*FilterNode{in}},
		{Field: f.RelatedBy, Op: OpNull},
	}}, nil
}

// relationLookup is the lookup of the related rows' correlation values a
// relation predicate tests, and whether the predicate holds for owners
// among them (else for owners outside them).
func (r *memberResolver) relationLookup(n *FilterNode) (*Lookup, bool, error) {
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
		cond, some, err = keyCondition(f, rel.TargetColumn, n)
		if err != nil {
			return nil, false, err
		}
	} else {
		var nested *FilterNode
		if len(n.Children) > 0 {
			nested = n.Children[0]
		}
		if rel.Policy {
			pc, ok := r.cfg.related[rel.RelatedTable()]
			if !ok {
				return nil, false, fmt.Errorf("relation %s on %s: %w", f.ExternalName, rel.RelatedTable(), ErrRelatedPolicyRequired)
			}
			ast, policyErr := ApplyPolicyConstraints(f.Related, &FilterAST{Root: nested}, pc)
			if policyErr != nil {
				return nil, false, fmt.Errorf("relation %s: %w", f.ExternalName, policyErr)
			}
			nested = ast.Root
		}
		nested, err = r.resolve(nested) // the target's own relations, depth 2
		if err != nil {
			return nil, false, err
		}
		if nested != nil {
			filter = NewParsedFilter(&FilterAST{Root: nested})
		}
	}

	if rel.TargetTable == "" {
		where, scopeErr := r.scoped(rel.JoinTable, rel.SourceColumn, rel.TenantColumn, rel.DeletedColumn, cond)
		if scopeErr != nil {
			return nil, false, scopeErr
		}
		return &Lookup{Table: rel.JoinTable, Project: rel.SourceColumn, Where: where, Filter: filter}, some, nil
	}
	targetWhere, err := r.scoped(rel.TargetTable, rel.TargetKey, rel.TargetTenant, rel.TargetDeleted, nil)
	if err != nil {
		return nil, false, err
	}
	target := &Lookup{Table: rel.TargetTable, Project: rel.TargetKey, Where: targetWhere, Filter: filter}
	targets := WhereMember(rel.TargetColumn, target)
	if cond != nil {
		targets = WhereAll(cond, targets)
	}
	where, err := r.scoped(rel.JoinTable, rel.SourceColumn, rel.TenantColumn, rel.DeletedColumn, targets)
	if err != nil {
		return nil, false, err
	}
	return &Lookup{Table: rel.JoinTable, Project: rel.SourceColumn, Where: where}, some, nil
}

// scoped is a lookup condition over table's live rows of the tenant, the
// projected column never NULL, and cond.
func (r *memberResolver) scoped(table, project, tenant, deleted string, cond *LookupCond) (*LookupCond, error) {
	conds := []*LookupCond{WhereNotNull(project)}
	if tenant != "" && !r.cfg.scope.unscoped {
		if !r.cfg.scope.hasTenant {
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
// makes on its target column, and whether owners among the matching pairs
// satisfy it (`$eq`, `$in`, `$not_null`) or owners outside them (`$ne`,
// `$nin`, `$null`).
func keyCondition(f *FieldSchema, column string, n *FilterNode) (*LookupCond, bool, error) {
	switch n.Op {
	case OpEq:
		return WhereEq(column, n.Value), true, nil
	case OpNe:
		return WhereEq(column, n.Value), false, nil
	case OpIn, OpNin:
		vs, ok := core.AnySlice(n.Value)
		if !ok {
			return nil, false, fmt.Errorf("$%s requires an array value, got %T", n.Op, n.Value)
		}
		return WhereIn(column, vs...), n.Op == OpIn, nil
	case OpNotNull:
		return nil, true, nil
	case OpNull:
		return nil, false, nil
	}
	return nil, false, invalidRelation(f, fmt.Sprintf("compares its key with $%s, which only SQL evaluates", n.Op))
}
