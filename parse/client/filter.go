package client

import (
	"encoding/json"
	"errors"
)

var (
	// ErrEmptyOr reports an Or of no filters. A disjunction of nothing
	// matches no row, which the grammar cannot express.
	ErrEmptyOr = errors.New("client: Or needs at least one filter")
	// ErrNegatedEmpty reports Not of the empty filter, which would match no
	// row; the grammar cannot express that either.
	ErrNegatedEmpty = errors.New("client: Not of the empty filter matches nothing")
)

// Filter is a filter expression in parse's request grammar, sent as a list's
// filter parameter. The zero value matches every row. Filters are immutable:
// field methods build conditions, and And, Or and Not combine them.
//
// A filter the grammar cannot express (an Or of nothing, Not of the empty
// filter, a sub-second relative time) carries its error, and encoding it
// returns that error.
type Filter struct {
	n   *node
	err error
}

// node is a condition ({field: {op: operand}}) or a logical node ($and and
// $or over children, $not over one child).
type node struct {
	op       string
	field    string
	operand  any
	children []*node
}

const (
	opAnd = "$and"
	opOr  = "$or"
	opNot = "$not"
)

// IsZero reports whether f is the empty filter, which matches every row.
func (f Filter) IsZero() bool { return f.n == nil && f.err == nil }

// Err is the error f carries, if the grammar cannot express it.
func (f Filter) Err() error { return f.err }

// MarshalJSON encodes f as a filter document: {} for the empty filter.
func (f Filter) MarshalJSON() ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return json.Marshal(f.n.document())
}

func (n *node) document() any {
	if n == nil {
		return map[string]any{}
	}
	switch n.op {
	case opAnd, opOr:
		items := make([]any, len(n.children))
		for i, c := range n.children {
			items[i] = c.document()
		}
		return map[string]any{n.op: items}
	case opNot:
		return map[string]any{opNot: n.children[0].document()}
	}
	return map[string]any{n.field: map[string]any{n.op: n.operand}}
}

// And matches the rows every filter matches; And of no filters is the empty
// filter. Nested Ands are flattened, so a filter built up in a loop stays
// within the server's nesting depth.
func And(filters ...Filter) Filter {
	var kept []*node
	for _, f := range filters {
		switch {
		case f.err != nil:
			return f
		case f.n == nil:
			continue
		case f.n.op == opAnd:
			kept = append(kept, f.n.children...)
		default:
			kept = append(kept, f.n)
		}
	}
	return logical(opAnd, kept)
}

// Or matches the rows any filter matches. An Or holding the empty filter
// matches every row; an Or of no filters is ErrEmptyOr. Nested Ors are
// flattened, as And's are.
func Or(filters ...Filter) Filter {
	if len(filters) == 0 {
		return Filter{err: ErrEmptyOr}
	}
	var kept []*node
	matchesAll := false
	for _, f := range filters {
		switch {
		case f.err != nil:
			return f
		case f.n == nil:
			matchesAll = true
		case f.n.op == opOr:
			kept = append(kept, f.n.children...)
		default:
			kept = append(kept, f.n)
		}
	}
	if matchesAll {
		return Filter{}
	}
	return logical(opOr, kept)
}

// Not matches the rows f does not. Not of the empty filter is
// ErrNegatedEmpty.
func Not(f Filter) Filter {
	switch {
	case f.err != nil:
		return f
	case f.n == nil:
		return Filter{err: ErrNegatedEmpty}
	}
	return Filter{n: &node{op: opNot, children: []*node{f.n}}}
}

func logical(op string, children []*node) Filter {
	switch len(children) {
	case 0:
		return Filter{}
	case 1:
		return Filter{n: children[0]}
	}
	return Filter{n: &node{op: op, children: children}}
}

func condition(field, op string, operand any) Filter {
	return Filter{n: &node{field: field, op: op, operand: operand}}
}

// nested is a condition whose operand is a filter over a relation's target
// or an array's elements.
func nested(field, op string, f Filter) Filter {
	if f.err != nil {
		return f
	}
	return condition(field, op, f)
}

// list copies vs, so a filter never shares the caller's backing array, and
// encodes no values as [] rather than null.
func list[V any](vs []V) []V {
	return append(make([]V, 0, len(vs)), vs...)
}
