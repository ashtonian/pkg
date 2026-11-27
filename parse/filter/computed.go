package filter

import "slices"

// ComputedColumn is a column statements read through an expression instead
// of the table's column (parse.ReadExpr): SQL is trusted application SQL in
// the dialect the schema's statements compile for, naming the schema's own
// columns.
type ComputedColumn struct {
	Column string
	SQL    string
}

// ComputedSQL is each of the schema's computed columns' SQL, parenthesized,
// by column; nil when the schema computes none.
func (s *ResourceSchema) ComputedSQL() map[string]string {
	if s == nil || len(s.Computed) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.Computed))
	for _, c := range s.Computed {
		out[c.Column] = "(" + c.SQL + ")"
	}
	return out
}

// columnExprs are a schema's computed columns resolved for the dialect a
// statement is built for: parenthesized SQL by column.
type columnExprs map[string]string

// computes reports whether column is one of the schema's computed columns.
func (s *ResourceSchema) computes(column string) bool {
	return s != nil && slices.ContainsFunc(s.Computed, func(c ComputedColumn) bool { return c.Column == column })
}

// computes reports whether f reads a computed column: a field of its own
// column, matched by the row column rather than its SQL, which a column
// mapper need not write as table.column.
func (e columnExprs) computes(f *FieldSchema) bool {
	if len(e) == 0 || f == nil || f.Relation != nil || f.Path != nil {
		return false
	}
	_, ok := e[f.RowColumn()]
	return ok
}

// field is f as a statement reads it: a computed column's field through its
// expression, any other field as it is.
func (e columnExprs) field(f *FieldSchema) *FieldSchema {
	if !e.computes(f) {
		return f
	}
	out := *f
	out.Expr = e[f.RowColumn()]
	return &out
}

// key is k ordered through its column's expression when the column is
// computed. The cursor still reads the key's value from the row's column.
func (e columnExprs) key(k CursorKey) CursorKey {
	if k.Path != nil {
		return k
	}
	if sql, ok := e[k.Column]; ok {
		k.Expr = sql
	}
	return k
}

// search is s with each field of a computed column searching its
// expression.
func (e columnExprs) search(s *SearchSchema) *SearchSchema {
	if len(e) == 0 || s == nil {
		return s
	}
	out := *s
	out.Fields = slices.Clone(s.Fields)
	for i, f := range out.Fields {
		if sql, ok := e[f.RowColumn()]; ok {
			out.Fields[i].Expr = sql
		}
	}
	return &out
}
