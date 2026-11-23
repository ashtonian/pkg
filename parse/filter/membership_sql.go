package filter

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// buildMember compiles a membership node to an IN subquery.
func (b *SQLBuilder) buildMember(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil || FieldExpr(b.dialect, node.Field) == "" || node.Member == nil {
		return "", invalidLookup("membership node has no field expression or lookup")
	}
	node = b.computed(node)
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
		if !isLookupDecimal(c.Value) {
			return q(c.Column) + " = " + bind(c.Value), nil
		}
		sql, _, err := decimalComparison(b.dialect, lookupColumn(c.Column, q(c.Column), core.FieldTypeDecimal), OpEq, c.Value, bind)
		if err != nil {
			return "", invalidLookup(fmt.Sprintf("lookup column %q: %v", c.Column, err))
		}
		return sql, nil
	case LookupOpIn:
		if len(c.Values) == 0 {
			return "1=0", nil
		}
		column, members, err := lookupSet(c, q(c.Column))
		if err != nil {
			return "", err
		}
		sql, err := b.dialect.memberSet(column, members, true, bind)
		if err != nil {
			// The lookup is the server's: its values are no request's fault.
			return "", invalidLookup(fmt.Sprintf("lookup column %q: %v", c.Column, err))
		}
		return sql, nil
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

// lookupSet is a WhereIn list as the set `$in` binds: its column, read as
// expr, typed by its values' Go type (core.TypeOf), one scalar type for all
// of them, and each value in that type's canonical form, so the dialect
// binds the list as one parameter whatever its length.
func lookupSet(c *LookupCond, expr string) (*FieldSchema, []any, error) {
	var typ core.FieldType
	members := make([]any, len(c.Values))
	for i, v := range c.Values {
		info := core.TypeOf(reflect.TypeOf(v))
		if !setScalar(info.Type) {
			return nil, nil, invalidLookup(fmt.Sprintf("lookup column %q lists a %T, no scalar value", c.Column, v))
		}
		if typ == "" {
			typ = info.Type
		} else if info.Type != typ {
			return nil, nil, invalidLookup(fmt.Sprintf("lookup column %q lists %s and %s values; a set holds one type", c.Column, typ, info.Type))
		}
		m, err := core.NormalizeScalar(typ, info.NumberKind, v)
		if err != nil {
			return nil, nil, invalidLookup(fmt.Sprintf("lookup column %q: %v", c.Column, err))
		}
		members[i] = m
	}
	return lookupColumn(c.Column, expr, typ), members, nil
}

// lookupColumn is a lookup column, read as expr, as a field of the type its
// values have: a lookup has no schema, so a comparison takes its type from
// its value, and renders as a field of that type does.
func lookupColumn(name, expr string, typ core.FieldType) *FieldSchema {
	return &FieldSchema{ExternalName: name, Column: name, Expr: expr, DataType: typ}
}

// isLookupDecimal reports a lookup value whose Go type is a decimal
// (core.TypeOf): the column it is compared with is read as a decimal and
// compared by value, so a stored "1.50" equals 1.5.
func isLookupDecimal(v any) bool {
	v = derefValue(v)
	return v != nil && core.TypeOf(reflect.TypeOf(v)).Type == core.FieldTypeDecimal
}

// setScalar reports whether a set of ft values binds as one parameter: a
// scalar type, not a document, array, geometry or opaque value.
func setScalar(ft core.FieldType) bool {
	switch ft {
	case core.FieldTypeJSON, core.FieldTypeObject, core.FieldTypeArray, core.FieldTypeAny, core.FieldTypeGeo, "":
		return false
	}
	return true
}
