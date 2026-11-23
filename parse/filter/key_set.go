package filter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// KeySet is the set an OpKeyIn node tests a row against: the row's Fields,
// together, equal one of Keys, each holding a canonical value per field in
// field order. Table is the table the fields are columns of: a statement
// may read them under an alias, but PostgreSQL types the bound set by the
// table's own row type.
type KeySet struct {
	Table  string
	Fields []*FieldSchema
	Keys   []core.Key
}

// keyNode compiles a key constraint (PolicyConstraint.Fields). One column
// compiles exactly as a constraint on Field does; a key of several columns
// is an AND of equalities under OpEq and a KeySet under OpIn.
func keyNode(norm *FilterParser, schema *ResourceSchema, c *PolicyConstraint) (*FilterNode, error) {
	if c.Field != "" || c.Member != nil {
		return nil, fmt.Errorf("%w: a key constraint names its columns in Fields alone, with no Field or Member", ErrPolicyInvalid)
	}
	if c.Operator != OpEq && c.Operator != OpIn {
		return nil, fmt.Errorf("%w: key constraint on %s: operator %q; a key constraint is $eq or $in", ErrPolicyInvalid, strings.Join(c.Fields, ", "), c.Operator)
	}
	keys, err := constraintKeys(c)
	if err != nil {
		return nil, err
	}
	if len(c.Fields) == 1 {
		single := *c
		single.Field, single.Fields = c.Fields[0], nil
		single.Value = keys[0][0]
		if c.Operator == OpIn {
			values := make([]any, len(keys))
			for i, k := range keys {
				values[i] = k[0]
			}
			single.Value = values
		}
		return constraintToNode(norm, schema, &single)
	}
	fields := make([]*FieldSchema, len(c.Fields))
	for i, name := range c.Fields {
		if slices.Contains(c.Fields[:i], name) {
			return nil, fmt.Errorf("%w: key constraint names %q twice", ErrPolicyInvalid, name)
		}
		field, ok, err := schema.PolicyField(name)
		if err != nil {
			return nil, policyFault(name, err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: field %q is not a field of %s", ErrPolicyInvalid, name, schema.Name)
		}
		if err := keySetField(field, c.Operator); err != nil {
			return nil, policyFault(name, err)
		}
		if i > 0 && field.Table != fields[0].Table {
			return nil, fmt.Errorf("%w: key constraint spans tables %s and %s", ErrPolicyInvalid, fields[0].Table, field.Table)
		}
		fields[i] = field
	}
	normalized := make([]core.Key, len(keys))
	for i, k := range keys {
		normalized[i] = make(core.Key, len(fields))
		for j, field := range fields {
			_, v, err := policyValue(norm, field, OpEq, k[j])
			if err != nil {
				if c.UnknownIfInvalid {
					return unknownNode(), nil
				}
				return nil, policyFault(field.ExternalName, err)
			}
			normalized[i][j] = v
		}
	}
	var node *FilterNode
	switch {
	case c.Operator == OpEq:
		children := make([]*FilterNode, len(fields))
		for j, field := range fields {
			children[j] = &FilterNode{Field: field, Op: OpEq, Value: normalized[0][j]}
		}
		node = &FilterNode{Op: OpAnd, Children: children}
	case len(normalized) == 0:
		// An empty set admits no row, as an empty `$in` does.
		node = &FilterNode{Field: fields[0], Op: OpIn, Value: []any{}}
	default:
		node = &FilterNode{Op: OpKeyIn, Keys: &KeySet{Table: fields[0].Table, Fields: fields, Keys: normalized}}
	}
	if c.Negate {
		return &FilterNode{Op: OpNot, Children: []*FilterNode{node}}, nil
	}
	return node, nil
}

// constraintKeys are a key constraint's keys, each as long as its Fields:
// one core.Key under OpEq, a []core.Key under OpIn.
func constraintKeys(c *PolicyConstraint) ([]core.Key, error) {
	var keys []core.Key
	switch v := c.Value.(type) {
	case core.Key:
		if c.Operator != OpEq {
			return nil, fmt.Errorf("%w: key constraint on %s: $in takes a []core.Key", ErrPolicyInvalid, strings.Join(c.Fields, ", "))
		}
		keys = []core.Key{v}
	case []core.Key:
		if c.Operator != OpIn {
			return nil, fmt.Errorf("%w: key constraint on %s: $eq takes one core.Key", ErrPolicyInvalid, strings.Join(c.Fields, ", "))
		}
		keys = v
	default:
		return nil, fmt.Errorf("%w: key constraint on %s: value %T is no core.Key", ErrPolicyInvalid, strings.Join(c.Fields, ", "), c.Value)
	}
	for i, k := range keys {
		if len(k) != len(c.Fields) {
			return nil, fmt.Errorf("%w: key constraint on %s: key %d has %d values; the key has %d columns",
				ErrPolicyInvalid, strings.Join(c.Fields, ", "), i, len(k), len(c.Fields))
		}
	}
	return keys, nil
}

// keySetField refuses a key column a bound key set cannot test: one that is
// no plain column, or, for a set, one with no equality to compare by
// (core.IsKeyType).
func keySetField(f *FieldSchema, op FilterOperator) error {
	if f.Relation != nil || f.Path != nil || f.Expr != "" || f.Column == "" {
		return fmt.Errorf("field %s is not a column of %s", f.ExternalName, f.Table)
	}
	if op == OpIn && !core.IsKeyType(f.DataType) {
		return fmt.Errorf("a key set compares scalar columns, and %s is %s", f.ExternalName, f.DataType)
	}
	return nil
}

// expand is the OR, over the keys, of the AND of each field's equality:
// what the set means, for a backend that evaluates rows one at a time.
func (ks *KeySet) expand() *FilterNode {
	branches := make([]*FilterNode, len(ks.Keys))
	for i, k := range ks.Keys {
		terms := make([]*FilterNode, len(ks.Fields))
		for j, f := range ks.Fields {
			terms[j] = &FilterNode{Field: f, Op: OpEq, Value: k[j]}
		}
		branches[i] = &FilterNode{Op: OpAnd, Children: terms}
	}
	return &FilterNode{Op: OpOr, Children: branches}
}

// buildKeySet compiles an OpKeyIn node: the row value of its columns IN the
// set, bound as one parameter whatever its length (Dialect.KeyIn).
func (b *SQLBuilder) buildKeySet(s *buildState, node *FilterNode) (string, error) {
	ks := node.Keys
	if ks == nil || len(ks.Fields) < 2 || len(ks.Keys) == 0 || ks.Table == "" {
		return "", fmt.Errorf("%w: key set node has no table, columns or keys", ErrPolicyInvalid)
	}
	columns := make([]string, len(ks.Fields))
	types := make([]core.TypeInfo, len(ks.Fields))
	for i, f := range ks.Fields {
		if f.Table != ks.Fields[0].Table {
			return "", fmt.Errorf("%w: key set spans %s and %s", ErrPolicyInvalid, ks.Fields[0].Table, f.Table)
		}
		columns[i], types[i] = f.Column, core.TypeInfo{Type: f.DataType, NumberKind: f.NumberKind}
	}
	return b.dialect.KeyIn(ks.Table, ks.Fields[0].Table, columns, types, ks.Keys, true, s.binder(b.dialect))
}

// keyText is the compact JSON array of the expressions' texts, the text
// core.KeyText gives a key of several columns.
func (d PostgresDialect) keyText(exprs []string) string {
	texts := make([]string, len(exprs))
	for i, e := range exprs {
		texts[i] = d.textKey(e)
	}
	return "array_to_json(ARRAY[" + strings.Join(texts, ", ") + "])::text"
}

// keyText is the compact JSON array of the expressions' texts, the text
// core.KeyText gives a key of several columns.
func (d SQLiteDialect) keyText(exprs []string) string {
	texts := make([]string, len(exprs))
	for i, e := range exprs {
		texts[i] = d.textKey(e)
	}
	return "json_array(" + strings.Join(texts, ", ") + ")"
}
