package filter

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// buildElements compiles `$some` / `$none` to one EXISTS / NOT EXISTS over
// the array's elements (Dialect.elementRows), the node's child holding for one
// element: a filter on a JSON array document's element paths, or a
// condition on a scalar array's element. A scalar array's elements are its
// values of its element type, so an element of another kind is no element.
func (b *SQLBuilder) buildElements(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	negate := ""
	if node.Op == OpNone {
		negate = "NOT "
	}
	var child *FilterNode
	if len(node.Children) > 0 {
		child = node.Children[0]
	}
	if isScalarArray(f) {
		sql, ok, err := b.dialect.containsElement(f, child, s.binder(b.dialect))
		if err != nil || ok {
			return negate + sql, err
		}
	}
	alias := elementAlias(b.depth + 1)
	var conds []string
	if isScalarArray(f) {
		elem := b.dialect.scalarElement(f, alias)
		conds = append(conds, FieldExpr(b.dialect, elem)+" IS NOT NULL")
		if child != nil {
			child = bindElement(child, elem)
		}
	} else if child != nil {
		var err error
		if child, err = rebaseElements(child, b.dialect.objectElement(alias), map[*FieldSchema]*FieldSchema{}); err != nil {
			return "", err
		}
	}
	if child != nil {
		nb := &SQLBuilder{schema: b.schema, dialect: b.dialect, scope: b.scope, related: b.related, depth: b.depth + 1, visible: b.visible}
		cond, err := s.selecting(func() (string, error) { return nb.buildNode(s, child) })
		if err != nil {
			return "", err
		}
		conds = append(conds, cond)
	}
	sql := negate + "EXISTS (SELECT 1 FROM " + b.dialect.elementRows(f, alias)
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	return sql + ")", nil
}

// buildArrayPath compiles a NULL test or `$contains` on an array path,
// whose value is its elements of its element type (PathValue): NULL when it
// has none, so `$contains` is Unknown there, as on a NULL array column.
func (b *SQLBuilder) buildArrayPath(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	switch node.Op {
	case OpNotNull:
		return b.buildElements(s, &FilterNode{Field: f, Op: OpSome})
	case OpNull:
		return b.buildElements(s, &FilterNode{Field: f, Op: OpNone})
	case OpContains:
		values, ok := core.AnySlice(node.Value)
		if !ok {
			return "", fmt.Errorf("field %s: $contains on an array requires an array value, got %T", f.ExternalName, node.Value)
		}
		present, err := b.buildElements(s, &FilterNode{Field: f, Op: OpSome})
		if err != nil {
			return "", err
		}
		held, err := containsAll(b.dialect, f, values, elementAlias(b.depth+1), s.binder(b.dialect))
		if err != nil {
			return "", err
		}
		return "(CASE WHEN " + present + " THEN " + held + " END)", nil
	}
	return "", unsupportedOperator(f, node.Op)
}

// containsAll renders "each of values equals an element of f's array", the
// elements read as f's element type from rows aliased alias. The values are
// one parameter, and the SQL's size is independent of their number: the
// distinct elements equal to a listed value number as many as the distinct
// listed values exactly when each has an equal element, equality being an
// equivalence. The listed values are hashed or indexed once per statement,
// each row reading its own elements. One EXISTS per value would pass SQLite's
// expression depth and PostgreSQL's parameter limit, and probe a row's
// elements once per value.
func containsAll(d Dialect, f *FieldSchema, values []any, alias string, bind func(any) string) (string, error) {
	if len(values) == 0 {
		return "1=1", nil
	}
	if slices.Contains(values, nil) {
		return "1=0", nil // no element equals NULL
	}
	elem := d.scalarElement(f, alias)
	set, err := d.setRows(elem, values, bind)
	if err != nil {
		return "", err
	}
	// set holds one placeholder, so its two uses read one parameter.
	e := FieldExpr(d, elem)
	return "((SELECT count(DISTINCT " + e + ") FROM " + d.elementRows(f, alias) + " WHERE " + e + " IN (" + set + "))" +
		" = (SELECT count(DISTINCT v) FROM (" + set + ") AS " + alias + "set))", nil
}

// elementAlias is the alias of the element rows a predicate at depth reads,
// distinct from an enclosing predicate's.
func elementAlias(depth int) string { return "elem" + strconv.Itoa(depth) + "__" }

// bindElement is n with a scalar array's element field replaced by elem,
// the element of one row of its element table.
func bindElement(n *FilterNode, elem *FieldSchema) *FilterNode {
	out := *n
	if n.Field != nil && isElement(n.Field) {
		out.Field = elem
	}
	if len(n.Children) > 0 {
		out.Children = make([]*FilterNode, len(n.Children))
		for i, c := range n.Children {
			out.Children[i] = bindElement(c, elem)
		}
	}
	return &out
}

// rebaseElements is n with its element paths reading value, the element. A
// scalar array's element field inside is bound by its own `$some` /
// `$none`.
func rebaseElements(n *FilterNode, value string, moved map[*FieldSchema]*FieldSchema) (*FilterNode, error) {
	out := *n
	if f := n.Field; f != nil && !isElement(f) {
		if !isElementPath(f) {
			return nil, fmt.Errorf("field %s is not an element path", f.ExternalName)
		}
		m, ok := moved[f]
		if !ok {
			c := bound(f)
			p := *f.Path
			p.Table, p.Column, p.Expr, p.Storage = "", "", value, StorageJSONB
			c.Path = &p
			m = &c
			moved[f] = m
		}
		out.Field = m
	}
	if len(n.Children) > 0 {
		out.Children = make([]*FilterNode, len(n.Children))
		for i, c := range n.Children {
			rc, err := rebaseElements(c, value, moved)
			if err != nil {
				return nil, err
			}
			out.Children[i] = rc
		}
	}
	return &out, nil
}
