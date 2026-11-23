package filter

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// buildMember compiles a membership node to an IN subquery. The field and
// the projected column compare as a filter compares the field's type
// (keyOperand): a decimal stored as "1.50" is a member where 1.5 is.
func (b *SQLBuilder) buildMember(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil || FieldExpr(b.dialect, node.Field) == "" || node.Member == nil {
		return "", invalidLookup("membership node has no field expression or lookup")
	}
	node = b.computed(node)
	l := node.Member
	if err := l.validateProjecting(1); err != nil {
		return "", err
	}
	t := core.TypeInfo{Type: node.Field.DataType, NumberKind: node.Field.NumberKind}
	sub, err := b.lookupSQL(s, l, func(_ int, project string) string {
		return keyOperand(b.dialect, keyField(l.Project, project, t))
	})
	if err != nil {
		return "", err
	}
	return keyOperand(b.dialect, node.Field) + " IN " + sub, nil
}

// lookupSQL renders a lookup as the subquery `(SELECT project FROM table
// WHERE …)`: its condition, then its filter on the table's own columns.
// Every column is qualified by the table, which may name its schema; the
// i-th projected column is read through project.
func (b *SQLBuilder) lookupSQL(s *buildState, l *Lookup, project func(i int, column string) string) (string, error) {
	table := quoteTable(b.dialect, l.Table)
	projected := l.projected()
	selected := make([]string, len(projected))
	for i, c := range projected {
		selected[i] = project(i, table+"."+b.dialect.QuoteIdent(c))
	}
	var sb strings.Builder
	sb.WriteString("(SELECT ")
	sb.WriteString(strings.Join(selected, ", "))
	sb.WriteString(" FROM ")
	sb.WriteString(table)
	var where []string
	if l.Where != nil {
		cond, err := b.lookupCondSQL(s, l.Where, l.Table)
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

// lookupCondSQL renders c on the columns of the lookup table, which may
// name its schema.
func (b *SQLBuilder) lookupCondSQL(s *buildState, c *LookupCond, lookupTable string) (string, error) {
	bind := s.binder(b.dialect)
	table := quoteTable(b.dialect, lookupTable)
	q := func(column string) string { return table + "." + b.dialect.QuoteIdent(column) }
	join := func(children []*LookupCond, sep string) (string, error) {
		parts := make([]string, len(children))
		for i, child := range children {
			part, err := b.lookupCondSQL(s, child, lookupTable)
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
		if c.Types == nil && !core.IsKeyType(core.TypeOf(reflect.TypeOf(derefValue(c.Value))).Type) {
			// A value of no scalar type binds as the driver encodes it.
			return q(c.Column) + " = " + bind(c.Value), nil
		}
		// As `$eq` compares its type: a decimal by value, a timestamp as
		// its canonical text.
		column, values, err := lookupSet(&LookupCond{Column: c.Column, Values: []any{c.Value}, Types: c.Types}, q(c.Column))
		if err != nil {
			return "", err
		}
		sql, err := b.dialect.Condition(column, OpEq, values[0], bind)
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
	case LookupOpMember, LookupOpKeyMember:
		return b.lookupMemberSQL(s, c, q)
	case LookupOpKeyIn:
		if len(c.Keys) == 0 {
			return "1=0", nil
		}
		types, keys, err := lookupKeys(c)
		if err != nil {
			return "", err
		}
		sql, err := b.dialect.keyIn(lookupTable, q, c.Columns, types, keys, true, bind)
		if err != nil {
			return "", invalidLookup(fmt.Sprintf("lookup key (%s): %v", strings.Join(c.Columns, ", "), err))
		}
		return sql, nil
	}
	return q(c.Column) + " IS NULL", nil
}

// lookupMemberSQL renders a member condition: its column, or the row of
// its Columns, IN the inner lookup's projection. Typed (LookupCond.Types),
// each column and its projected column compare as the type compares
// (keyOperand); untyped, as stored.
func (b *SQLBuilder) lookupMemberSQL(s *buildState, c *LookupCond, q func(column string) string) (string, error) {
	columns := c.Columns
	if c.Op == LookupOpMember {
		columns = []string{c.Column}
	}
	operand := func(i int, name, expr string) string {
		if c.Types == nil {
			return expr
		}
		return keyOperand(b.dialect, keyField(name, expr, c.Types[i]))
	}
	projected := c.Lookup.projected()
	sub, err := b.lookupSQL(s, c.Lookup, func(i int, project string) string {
		return operand(i, projected[i], project)
	})
	if err != nil {
		return "", err
	}
	operands := make([]string, len(columns))
	for i, column := range columns {
		operands[i] = operand(i, column, q(column))
	}
	if len(operands) == 1 {
		return operands[0] + " IN " + sub, nil
	}
	return "(" + strings.Join(operands, ", ") + ") IN " + sub, nil
}

// lookupKeys are a WhereKeyIn's types and keys as the set Dialect.KeyIn
// binds: its Types, or else each column typed by its values' Go type as
// WhereIn's (lookupSet), each value in its type's canonical form.
func lookupKeys(c *LookupCond) ([]core.TypeInfo, []core.Key, error) {
	if c.Types != nil {
		return c.Types, c.Keys, nil
	}
	types := make([]core.TypeInfo, len(c.Columns))
	keys := make([]core.Key, len(c.Keys))
	for i := range keys {
		keys[i] = make(core.Key, len(c.Columns))
	}
	values := make([]any, len(c.Keys))
	for j, column := range c.Columns {
		for i, k := range c.Keys {
			values[i] = k[j]
		}
		field, members, err := lookupSet(&LookupCond{Column: column, Values: values}, "")
		if err != nil {
			return nil, nil, err
		}
		types[j] = core.TypeInfo{Type: field.DataType}
		for i, m := range members {
			keys[i][j] = m
		}
	}
	return types, keys, nil
}

// lookupSet is a WhereIn list as the set `$in` binds: its column, read as
// expr, typed by its Types, or else by its values' Go type (core.TypeOf),
// one scalar type for all of them, and each value in that type's canonical
// form, so the dialect binds the list as one parameter whatever its length.
func lookupSet(c *LookupCond, expr string) (*FieldSchema, []any, error) {
	if c.Types != nil {
		t := c.Types[0]
		members := make([]any, len(c.Values))
		for i, v := range c.Values {
			m, err := core.NormalizeScalar(t.Type, t.NumberKind, projectedValue(v))
			if err != nil {
				return nil, nil, invalidLookup(fmt.Sprintf("lookup column %q: %v", c.Column, err))
			}
			members[i] = m
		}
		return keyField(c.Column, expr, t), members, nil
	}
	var typ core.FieldType
	members := make([]any, len(c.Values))
	for i, v := range c.Values {
		info := core.TypeOf(reflect.TypeOf(v))
		if !core.IsKeyType(info.Type) {
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
