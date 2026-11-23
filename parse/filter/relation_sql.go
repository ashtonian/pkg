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

// buildRelated compiles an OpSome / OpNone relation predicate
// to one correlated EXISTS / NOT EXISTS over the related rows: the junction
// or target rows correlated to the owner, held to the tenant (or the
// target's policy) and to live rows, satisfying the node's child — the
// target's own filter — on one row. Each depth has its own aliases, so a
// relation back to the owner's table does not shadow it. Every relation
// subquery is built here, a pair table the relation tag declares included
// (buildPairTable).
func (b *SQLBuilder) buildRelated(s *buildState, node *FilterNode) (string, error) {
	rel, target := node.Field.Relation, node.Field.Related
	if target == nil {
		return "", invalidRelation(node.Field, "has no target resource to filter")
	}
	if err := rel.Check(); err != nil {
		return "", invalidRelation(node.Field, err.Error())
	}
	// The target's predicates read its computed columns as the owner's
	// dialect does; one the dialect refuses fails the build.
	exprs, err := target.resolve(b.dialect)
	if err != nil {
		return "", err
	}
	depth := b.depth + 1
	visible := append([]string{b.schema.Name}, b.visible...)
	bind := s.binder(b.dialect)
	d := b.dialect

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
	writeTable(&sb, d, rel.JoinTable, join)
	if rel.TargetTable != "" {
		sb.WriteString(" JOIN ")
		writeTable(&sb, d, rel.TargetTable, related)
		sb.WriteString(" ON " + correlation(d, related, rel.TargetKeys, join, rel.TargetColumns, rel.TargetTypes))
	}
	sb.WriteString(" WHERE " + correlation(d, join, rel.SourceColumns, b.schema.Name, rel.OwnerColumns(), rel.SourceTypes))

	// A policy-scoped relation declares no tenant column on its target:
	// the policy below decides which target rows are visible.
	scope, err := b.scope.clause(d, rel.JoinTable, rel.TenantColumn, join, joinTenantType(node.Field), bind)
	if err != nil {
		return "", err
	}
	sb.WriteString(scope)
	if rel.DeletedColumn != "" {
		sb.WriteString(" AND " + qualified(d, join, rel.DeletedColumn) + " IS NULL")
	}
	targetScope, err := b.scope.clause(d, rel.TargetTable, rel.TargetTenant, related, tenantColumnType(target, rel.TargetTenant), bind)
	if err != nil {
		return "", err
	}
	sb.WriteString(targetScope)
	if rel.TargetDeleted != "" {
		sb.WriteString(" AND " + qualified(d, related, rel.TargetDeleted) + " IS NULL")
	}

	nested := (*FilterNode)(nil)
	if len(node.Children) > 0 {
		nested = node.Children[0]
	}
	if rel.Policy {
		if nested, err = b.related.on(node.Field, nested); err != nil {
			return "", err
		}
	}
	if nested != nil {
		if related != target.Name {
			if nested, err = rebaseNode(nested, target.Name, related, exprs, map[*FieldSchema]*FieldSchema{}); err != nil {
				return "", fmt.Errorf("relation %s: %w", node.Field.ExternalName, err)
			}
		}
		nb := &SQLBuilder{
			schema: &ResourceSchema{Name: related}, dialect: b.dialect, scope: b.scope, related: b.related,
			depth: depth, visible: append(visible, join, related), exprs: exprs,
		}
		cond, err := s.selecting(func() (string, error) { return nb.buildNode(s, nested) })
		if err != nil {
			return "", err
		}
		sb.WriteString(" AND " + cond)
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

// joinTenantType is the declared type of a relation's join-table tenant
// column: the target's, when the join table is the target's own table; ""
// for a pair table, which declares no types.
func joinTenantType(f *FieldSchema) core.FieldType {
	if f.Relation.TargetTable != "" {
		return ""
	}
	return tenantColumnType(f.Related, f.Relation.TenantColumn)
}

// relationTableAlias is table itself unless a table already in scope has
// that name, else a name unique to the depth.
func relationTableAlias(table string, depth int, visible []string) string {
	if slices.Contains(visible, table) {
		return table + "_rel" + strconv.Itoa(depth) + "__"
	}
	return table
}

// writeTable writes table as a FROM item named alias, both quoted: an alias
// made from a table's name is the name's exact spelling too.
func writeTable(sb *strings.Builder, d Dialect, table, alias string) {
	sb.WriteString(d.QuoteIdent(table))
	if alias != table {
		sb.WriteByte(' ')
		sb.WriteString(d.QuoteIdent(alias))
	}
}

// rebaseNode is n with every field moved from table to alias: expressions
// that are table's columns, or JSON paths into them, name alias instead.
// SQL's unknown names no table and stays as it is. A field computed some
// other way (a column mapper's expression, or one of computed), or raw
// policy SQL, cannot be moved and is an error.
func rebaseNode(n *FilterNode, table, alias string, computed columnExprs, moved map[*FieldSchema]*FieldSchema) (*FilterNode, error) {
	if n == nil {
		return nil, nil
	}
	if n.Op == OpUnknown {
		return n, nil
	}
	if n.Op == OpRawSQL {
		return nil, fmt.Errorf("raw SQL cannot be applied to the aliased relation table %s", alias)
	}
	out := *n
	// An element field or element path stays a placeholder: its own
	// `$some` / `$none` binds it to the element alias.
	if n.Field != nil && n.Field.Relation == nil && !isElement(n.Field) && !isElementPath(n.Field) {
		f, err := rebaseField(n.Field, table, alias, computed, moved)
		if err != nil {
			return nil, err
		}
		out.Field = f
	}
	if n.Keys != nil {
		ks := *n.Keys
		ks.Fields = make([]*FieldSchema, len(n.Keys.Fields))
		for i, kf := range n.Keys.Fields {
			f, err := rebaseField(kf, table, alias, computed, moved)
			if err != nil {
				return nil, err
			}
			ks.Fields[i] = f
		}
		out.Keys = &ks
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
			rc, err := rebaseNode(c, table, alias, computed, moved)
			if err != nil {
				return nil, err
			}
			out.Children[i] = rc
		}
	}
	return &out, nil
}

func rebaseField(f *FieldSchema, table, alias string, computed columnExprs, moved map[*FieldSchema]*FieldSchema) (*FieldSchema, error) {
	if m, ok := moved[f]; ok {
		return m, nil
	}
	c := *f
	switch {
	case f.Path != nil:
		if f.Path.Expr != "" || f.Path.Table != table {
			return nil, fmt.Errorf("field %s is not a column of %s and cannot be filtered through a relation", f.ExternalName, table)
		}
		p := *f.Path
		p.Table = alias
		c.Path = &p
	case f.Expr == "" && f.Table == table && !computed.computes(f):
	default:
		return nil, fmt.Errorf("field %s is not a column of %s and cannot be filtered through a relation", f.ExternalName, table)
	}
	c.Table = alias
	moved[f] = &c
	return &c, nil
}

func invalidRelation(f *FieldSchema, msg string) error {
	return &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: ECodeInvalidRelation.String(),
		Message: fmt.Sprintf("relation '%s' %s", f.ExternalName, msg), Details: map[string]any{core.DetailField: f.ExternalName},
	}
}
