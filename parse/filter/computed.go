package filter

import (
	"errors"
	"fmt"
	"slices"
)

// ComputedExpr is a computed column's SQL in one dialect: trusted
// application SQL naming the schema's own columns, or an error for a dialect
// it cannot serve. Every statement that compiles the schema calls it, so it
// must be cheap and return the same SQL for the same dialect.
type ComputedExpr func(Dialect) (string, error)

// ComputedColumn is a column statements read through an expression instead
// of the table's column (parse.ReadExpr).
type ComputedColumn struct {
	Column string
	Expr   ComputedExpr
}

// ComputedSQL resolves the schema's computed columns for d: each one's SQL,
// parenthesized, by column; nil when the schema computes none. The first
// expression, in declaration order, that refuses d returns its error.
func (s *ResourceSchema) ComputedSQL(d Dialect) (map[string]string, error) {
	if s == nil || len(s.Computed) == 0 {
		return nil, nil
	}
	if d == nil {
		return nil, fmt.Errorf("filter: %s: computed columns need a dialect", s.Name)
	}
	out := make(map[string]string, len(s.Computed))
	for _, c := range s.Computed {
		if c.Expr == nil {
			return nil, fmt.Errorf("filter: %s: computed column %q has no expression", s.Name, c.Column)
		}
		sql, err := c.Expr(d)
		if err != nil {
			return nil, err
		}
		out[c.Column] = "(" + sql + ")"
	}
	return out, nil
}

// columnExprs are a schema's computed columns resolved for the dialect a
// statement is built for: parenthesized SQL by column.
type columnExprs map[string]string

// computes reports whether column is one of the schema's computed columns.
func (s *ResourceSchema) computes(column string) bool {
	return s != nil && slices.ContainsFunc(s.Computed, func(c ComputedColumn) bool { return c.Column == column })
}

// resolve is ComputedSQL inside a build: a refusal passes through the
// builder unwrapped (computedError).
func (s *ResourceSchema) resolve(d Dialect) (columnExprs, error) {
	exprs, err := s.ComputedSQL(d)
	if err != nil {
		return nil, &computedError{err}
	}
	return exprs, nil
}

// computedError is a computed column's refusal of a dialect. A builder
// returns it as the expression returned it, never as a failure to generate
// SQL from the filter.
type computedError struct{ err error }

func (e *computedError) Error() string { return e.err.Error() }
func (e *computedError) Unwrap() error { return e.err }

// computedCause is err as a builder returns it: a computed column's refusal
// as the expression returned it, any other error as it is.
func computedCause(err error) error {
	if ce, ok := errors.AsType[*computedError](err); ok {
		return ce.err
	}
	return err
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
