package filter

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
// no plain column, or, for a set, one whose values are not compared by
// value on every backend (core.IsKeyType).
func keySetField(f *FieldSchema, op FilterOperator) error {
	if f.Relation != nil || f.Path != nil || f.Expr != "" || f.Column == "" {
		return fmt.Errorf("field %s is not a column of %s", f.ExternalName, f.Table)
	}
	if op == OpIn && !core.IsKeyType(f.DataType) {
		return fmt.Errorf("a key set compares string, UUID and integer columns, and %s is %s", f.ExternalName, f.DataType)
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
// set, bound as one parameter whatever its length.
func (b *SQLBuilder) buildKeySet(s *buildState, node *FilterNode) (string, error) {
	ks := node.Keys
	if ks == nil || len(ks.Fields) < 2 || len(ks.Keys) == 0 || ks.Table == "" {
		return "", fmt.Errorf("%w: key set node has no table, columns or keys", ErrPolicyInvalid)
	}
	exprs := make([]string, len(ks.Fields))
	for i, f := range ks.Fields {
		exprs[i] = FieldExpr(b.dialect, f)
	}
	return b.dialect.keySet(ks, "("+strings.Join(exprs, ", ")+")", s.binder(b.dialect))
}

// keySet binds the keys as one JSON parameter and reads them back as rows
// of the table's row type, so every position has its column's own type: a
// string field over a uuid column compares as uuid, and the key index
// serves the semi-join.
func (d PostgresDialect) keySet(ks *KeySet, row string, bind func(any) string) (string, error) {
	doc := make([]map[string]any, len(ks.Keys))
	for i, k := range ks.Keys {
		doc[i] = make(map[string]any, len(ks.Fields))
		for j, f := range ks.Fields {
			doc[i][f.Column] = k[j]
		}
	}
	arr, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("key set on %s: %w", ks.Table, err)
	}
	columns := make([]string, len(ks.Fields))
	for i, f := range ks.Fields {
		columns[i] = qualified(d, "k", f.Column)
	}
	return row + " IN (SELECT " + strings.Join(columns, ", ") + " FROM jsonb_populate_recordset(NULL::" + d.QuoteIdent(ks.Table) +
		", " + bind(string(arr)) + ") AS " + d.QuoteIdent("k") + ")", nil
}

// keySet binds the keys as one JSON array of arrays and reads each position
// back with json_extract, the value a scalar parameter binds.
func (SQLiteDialect) keySet(ks *KeySet, row string, bind func(any) string) (string, error) {
	doc := make([][]any, len(ks.Keys))
	for i, k := range ks.Keys {
		for j, f := range ks.Fields {
			if err := sqliteIntegers(f, k[j]); err != nil {
				return "", err
			}
		}
		doc[i] = k
	}
	arr, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("key set on %s: %w", ks.Table, err)
	}
	positions := make([]string, len(ks.Fields))
	for i := range ks.Fields {
		positions[i] = "json_extract(value, '$[" + strconv.Itoa(i) + "]')"
	}
	return row + " IN (SELECT " + strings.Join(positions, ", ") + " FROM json_each(" + bind(string(arr)) + "))", nil
}
