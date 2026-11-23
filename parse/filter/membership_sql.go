package filter

import (
	"strings"
)

// buildMember compiles a membership node to an IN subquery.
func (b *SQLBuilder) buildMember(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil || FieldExpr(b.dialect, node.Field) == "" || node.Member == nil {
		return "", invalidLookup("membership node has no field expression or lookup")
	}
	l := node.Member
	if err := l.validate(); err != nil {
		return "", err
	}
	sub, err := b.lookupSQL(s, l)
	if err != nil {
		return "", err
	}
	return FieldExpr(b.dialect, node.Field) + " IN " + sub, nil
}

// lookupSQL renders a lookup as the subquery `(SELECT project FROM table
// WHERE …)`: its condition, then its filter on the table's own columns.
// Every column is qualified by the table, which may name its schema.
func (b *SQLBuilder) lookupSQL(s *buildState, l *Lookup) (string, error) {
	q := b.dialect.QuoteIdent
	parts := make([]string, 0, 2)
	for part := range strings.SplitSeq(l.Table, ".") {
		parts = append(parts, q(part))
	}
	table := strings.Join(parts, ".")
	var sb strings.Builder
	sb.WriteString("(SELECT ")
	sb.WriteString(table + "." + q(l.Project))
	sb.WriteString(" FROM ")
	sb.WriteString(table)
	var where []string
	if l.Where != nil {
		cond, err := b.lookupCondSQL(s, l.Where, table)
		if err != nil {
			return "", err
		}
		where = append(where, cond)
	}
	if l.Filter != nil && l.Filter.ast != nil && l.Filter.ast.Root != nil {
		fb := &SQLBuilder{schema: &ResourceSchema{Name: l.Table}, dialect: b.dialect, scope: b.scope, related: b.related}
		cond, err := s.selecting(func() (string, error) { return fb.buildNode(s, l.Filter.ast.Root) })
		if err != nil {
			return "", err
		}
		where = append(where, cond)
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

// lookupCondSQL renders c on the columns of table, already quoted.
func (b *SQLBuilder) lookupCondSQL(s *buildState, c *LookupCond, table string) (string, error) {
	bind := s.binder(b.dialect)
	q := func(column string) string { return table + "." + b.dialect.QuoteIdent(column) }
	join := func(children []*LookupCond, sep string) (string, error) {
		parts := make([]string, len(children))
		for i, child := range children {
			part, err := b.lookupCondSQL(s, child, table)
			if err != nil {
				return "", err
			}
			parts[i] = part
		}
		return "(" + strings.Join(parts, sep) + ")", nil
	}
	switch {
	case len(c.And) > 0:
		return join(c.And, " AND ")
	case len(c.Or) > 0:
		return join(c.Or, " OR ")
	}
	switch c.Op {
	case LookupOpEq:
		return q(c.Column) + " = " + bind(c.Value), nil
	case LookupOpIn:
		if len(c.Values) == 0 {
			return "1=0", nil
		}
		phs := make([]string, len(c.Values))
		for i, v := range c.Values {
			phs[i] = bind(v)
		}
		return q(c.Column) + " IN (" + strings.Join(phs, ", ") + ")", nil
	case LookupOpNotNull:
		return q(c.Column) + " IS NOT NULL", nil
	case LookupOpMember:
		sub, err := b.lookupSQL(s, c.Lookup)
		if err != nil {
			return "", err
		}
		return q(c.Column) + " IN " + sub, nil
	}
	return q(c.Column) + " IS NULL", nil
}
