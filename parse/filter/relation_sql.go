package filter

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrRelatedPolicyRequired is a relation predicate on a policy-scoped
// relation built without the server's policy for the target table: the
// related rows the caller may see are undecided, so nothing is built.
var ErrRelatedPolicyRequired = errors.New("filter: a policy-scoped relation is built without the target's policy")

// WithRelatedPolicies supplies the server's policy for the target tables of
// policy-scoped relations (core.RelationSchema.Policy), keyed by table. A
// relation predicate on one without its table's policy fails with
// ErrRelatedPolicyRequired.
func WithRelatedPolicies(policies map[string]*PolicyConstraints) SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.related = policies }
}

// buildRelated compiles an OpSome / OpNone relation predicate
// to one correlated EXISTS / NOT EXISTS over the related rows: the junction
// or target rows correlated to the owner, held to the tenant (or the
// target's policy) and to live rows, satisfying the node's child — the
// target's own filter — on one row. Each depth has its own aliases, so a
// relation back to the owner's table does not shadow it.
func (b *SQLBuilder) buildRelated(s *buildState, node *FilterNode) (string, error) {
	rel, target := node.Field.Relation, node.Field.Related
	if target == nil {
		return "", invalidRelation(node.Field, "has no target resource to filter")
	}
	depth := b.depth + 1
	visible := append([]string{b.schema.Name}, b.visible...)
	bind := s.binder(b.dialect)

	pk := rel.PrimaryKey
	if pk == "" {
		pk = "id"
	}
	join := relationTableAlias(rel.JoinTable, depth, visible)
	related := join
	if rel.TargetTable != "" {
		join = "rel" + strconv.Itoa(depth) + "__"
		related = relationTableAlias(rel.TargetTable, depth, append(visible, join))
	}

	var sb strings.Builder
	if node.Op == OpNone {
		sb.WriteString("NOT ")
	}
	sb.WriteString("EXISTS (SELECT 1 FROM ")
	writeTable(&sb, rel.JoinTable, join)
	if rel.TargetTable != "" {
		sb.WriteString(" JOIN ")
		writeTable(&sb, rel.TargetTable, related)
		sb.WriteString(" ON " + related + "." + rel.TargetKey + " = " + join + "." + rel.TargetColumn)
	}
	sb.WriteString(" WHERE " + join + "." + rel.SourceColumn + " = " + b.schema.Name + "." + pk)

	// A policy-scoped relation declares no tenant column on its target:
	// the policy below decides which target rows are visible.
	scope, err := b.scope.clause(rel, join, bind)
	if err != nil {
		return "", err
	}
	sb.WriteString(scope)
	if rel.DeletedColumn != "" {
		sb.WriteString(" AND " + join + "." + rel.DeletedColumn + " IS NULL")
	}
	if rel.TargetTenant != "" {
		targetScope, err := b.scope.clause(&core.RelationSchema{JoinTable: rel.TargetTable, TenantColumn: rel.TargetTenant}, related, bind)
		if err != nil {
			return "", err
		}
		sb.WriteString(targetScope)
	}
	if rel.TargetDeleted != "" {
		sb.WriteString(" AND " + related + "." + rel.TargetDeleted + " IS NULL")
	}

	nested := (*FilterNode)(nil)
	if len(node.Children) > 0 {
		nested = node.Children[0]
	}
	if rel.Policy {
		pc, ok := b.related[rel.RelatedTable()]
		if !ok {
			return "", fmt.Errorf("relation %s on %s: %w", node.Field.ExternalName, rel.RelatedTable(), ErrRelatedPolicyRequired)
		}
		ast, err := ApplyPolicyConstraints(target, &FilterAST{Root: nested}, pc)
		if err != nil {
			return "", fmt.Errorf("relation %s: %w", node.Field.ExternalName, err)
		}
		nested = ast.Root
	}
	if nested != nil {
		if related != target.Name {
			var err error
			if nested, err = rebaseNode(nested, target.Name, related, map[*FieldSchema]*FieldSchema{}); err != nil {
				return "", fmt.Errorf("relation %s: %w", node.Field.ExternalName, err)
			}
		}
		nb := &SQLBuilder{
			schema: &ResourceSchema{Name: related}, dialect: b.dialect, scope: b.scope, related: b.related,
			depth: depth, visible: append(visible, join, related),
		}
		cond, err := nb.buildNode(s, nested)
		if err != nil {
			return "", err
		}
		sb.WriteString(" AND " + cond)
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

// relationTableAlias is table itself unless a table already in scope has
// that name, else a name unique to the depth.
func relationTableAlias(table string, depth int, visible []string) string {
	if slices.Contains(visible, table) {
		return table + "_rel" + strconv.Itoa(depth) + "__"
	}
	return table
}

func writeTable(sb *strings.Builder, table, alias string) {
	sb.WriteString(table)
	if alias != table {
		sb.WriteByte(' ')
		sb.WriteString(alias)
	}
}

// rebaseNode is n with every field moved from table to alias: expressions
// that are table's columns, or JSON paths into them, name alias instead. A
// field computed some other way, or raw policy SQL, cannot be moved and is
// an error.
func rebaseNode(n *FilterNode, table, alias string, moved map[*FieldSchema]*FieldSchema) (*FilterNode, error) {
	if n == nil {
		return nil, nil
	}
	if n.Op == OpRawSQL {
		return nil, fmt.Errorf("raw SQL cannot be applied to the aliased relation table %s", alias)
	}
	out := *n
	if n.Field != nil && n.Field.Relation == nil {
		f, err := rebaseField(n.Field, table, alias, moved)
		if err != nil {
			return nil, err
		}
		out.Field = f
	}
	if n.Field != nil && n.Field.Relation != nil {
		// A nested relation correlates to its owner, the builder's alias;
		// its own predicates are its target's.
		out.Children = n.Children
		return &out, nil
	}
	if len(n.Children) > 0 {
		out.Children = make([]*FilterNode, len(n.Children))
		for i, c := range n.Children {
			rc, err := rebaseNode(c, table, alias, moved)
			if err != nil {
				return nil, err
			}
			out.Children[i] = rc
		}
	}
	return &out, nil
}

func rebaseField(f *FieldSchema, table, alias string, moved map[*FieldSchema]*FieldSchema) (*FieldSchema, error) {
	if m, ok := moved[f]; ok {
		return m, nil
	}
	prefix := table + "."
	c := *f
	switch {
	case f.Path != nil:
		if !strings.HasPrefix(f.Path.Column, prefix) {
			return nil, fmt.Errorf("field %s is not a column of %s and cannot be filtered through a relation", f.ExternalName, table)
		}
		p := *f.Path
		p.Column = alias + "." + strings.TrimPrefix(p.Column, prefix)
		c.Path = &p
	case strings.HasPrefix(f.SQLExpr, prefix) && core.IsIdentifier(strings.TrimPrefix(f.SQLExpr, prefix)):
		c.SQLExpr = alias + "." + strings.TrimPrefix(f.SQLExpr, prefix)
	default:
		return nil, fmt.Errorf("field %s is not a column of %s and cannot be filtered through a relation", f.ExternalName, table)
	}
	moved[f] = &c
	return &c, nil
}

func invalidRelation(f *FieldSchema, msg string) error {
	return &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: ECodeInvalidRelation.String(),
		Message: fmt.Sprintf("relation '%s' %s", f.ExternalName, msg), Details: map[string]any{"field": f.ExternalName},
	}
}
