package filter

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// ApplyPolicyConstraints merges policy constraints into the filter AST.
// Constraint values are normalized as request values are,
// since dialects and the memory backend read canonical values only; request
// limits do not apply, as policy lists (allowed IDs) are trusted and may be
// long. Relative-time tokens resolve against the wall clock.
func ApplyPolicyConstraints(schema *ResourceSchema, userAST *FilterAST, pc *PolicyConstraints) (*FilterAST, error) {
	return applyPolicyConstraints(schema, userAST, pc, nil)
}

// applyPolicyConstraints is ApplyPolicyConstraints with the relative-time
// clock; nil is the wall clock.
func applyPolicyConstraints(schema *ResourceSchema, userAST *FilterAST, pc *PolicyConstraints, clock func() time.Time) (*FilterAST, error) {
	if pc == nil || pc.IsEmpty() {
		return userAST, nil
	}
	if schema == nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeSchemaNotFound.String(),
			Message: "cannot apply policy constraints: schema is nil",
		}
	}
	norm := NewFilterParser(schema, &core.ParserConfig{MaxDepth: math.MaxInt, MaxArraySize: math.MaxInt, Clock: clock})
	policyNode, err := constraintsToNode(norm, schema, pc)
	if err != nil {
		return nil, err
	}
	if policyNode == nil { // every group empty — no constraint to apply
		return userAST, nil
	}
	if userAST == nil || userAST.Root == nil {
		return &FilterAST{Root: policyNode}, nil
	}
	return &FilterAST{
		Root: &FilterNode{
			Op:       OpAnd,
			Children: []*FilterNode{userAST.Root, policyNode},
		},
	}, nil
}

func constraintsToNode(norm *FilterParser, schema *ResourceSchema, pc *PolicyConstraints) (*FilterNode, error) {
	var children []*FilterNode

	for _, c := range pc.Constraints {
		node, err := constraintToNode(norm, schema, c)
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}

	for _, g := range pc.And {
		node, err := constraintsToNode(norm, schema, g)
		if err != nil {
			return nil, err
		}
		if node != nil { // empty nested group — nothing to constrain
			children = append(children, node)
		}
	}

	if len(pc.Or) > 0 {
		var orChildren []*FilterNode
		for _, g := range pc.Or {
			node, err := constraintsToNode(norm, schema, g)
			if err != nil {
				return nil, err
			}
			if node != nil { // empty OR branch would widen to match-all via a nil child
				orChildren = append(orChildren, node)
			}
		}
		if len(orChildren) > 0 {
			children = append(children, &FilterNode{Op: OpOr, Children: orChildren})
		}
	}

	var node *FilterNode
	switch len(children) {
	case 0:
		if pc.Not {
			// An empty group admits every row; its negation admits none, which
			// a caller states with a constraint rather than an empty group.
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindFilter, Code: ECodeInvalidOperator.String(),
				Message: "policy constraint negates an empty group",
			}
		}
		return nil, nil
	case 1:
		node = children[0]
	default:
		node = &FilterNode{Op: OpAnd, Children: children}
	}
	if pc.Not {
		node = &FilterNode{Op: OpNot, Children: []*FilterNode{node}}
	}
	return node, nil
}

func constraintToNode(norm *FilterParser, schema *ResourceSchema, c *PolicyConstraint) (*FilterNode, error) {
	// Raw SQL constraints bypass field lookup — used for subquery-based scoping.
	if c.RawSQL != "" {
		node := &FilterNode{Op: OpRawSQL, RawSQL: c.RawSQL, RawArgs: c.RawArgs}
		if c.Negate {
			return &FilterNode{Op: OpNot, Children: []*FilterNode{node}}, nil
		}
		return node, nil
	}

	field := lookupField(schema, c.Field)
	if field == nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeUnknownField.String(),
			Message: "policy constraint references unknown field '" + c.Field + "'",
		}
	}
	if c.Member != nil {
		node, err := memberNode(field, c.Member)
		if err != nil {
			return nil, err
		}
		if c.Negate {
			return &FilterNode{Op: OpNot, Children: []*FilterNode{node}}, nil
		}
		return node, nil
	}
	op, value, err := policyValue(norm, field, c.Operator, c.Value)
	if err != nil && c.UnknownIfInvalid {
		return unknownNode(), nil
	}
	if err != nil {
		if pe, ok := errors.AsType[*core.ParseError](err); ok {
			pe.Message = "policy constraint on '" + c.Field + "': " + pe.Message
			return nil, pe
		}
		return nil, err
	}
	node := &FilterNode{Field: field, Op: op, Value: value}
	if c.Negate {
		return &FilterNode{Op: OpNot, Children: []*FilterNode{node}}, nil
	}
	return node, nil
}

// unknownSQL is a predicate whose value is SQL's unknown on every backend:
// NULL in SQL, and the memory evaluator's unknown for a raw predicate.
const unknownSQL = "NULL"

// UnknownConstraint returns the constraint whose value is SQL's unknown: no
// row satisfies it, nor its negation. A group holding it admits no row; a
// negated group holding it still excludes the rows its other constraints
// match.
func UnknownConstraint() *PolicyConstraint {
	return &PolicyConstraint{RawSQL: unknownSQL}
}

func unknownNode() *FilterNode {
	return &FilterNode{Op: OpRawSQL, RawSQL: unknownSQL}
}

func lookupField(schema *ResourceSchema, name string) *FieldSchema {
	if schema.SystemFields != nil {
		if f, ok := schema.SystemFields[name]; ok {
			return f
		}
	}
	if f, ok := schema.Fields[name]; ok {
		return f
	}
	return nil
}

// policyValue returns the operator and canonical value a constraint compiles
// to. The NULL tests carry no value (`$exists` maps onto them, as in the
// filter grammar); typed geo shapes are validated as built; everything else
// normalizes through the request path. The operator must apply to the
// field's type — policies bypass a field's declared
// operators, not its type.
func policyValue(norm *FilterParser, field *FieldSchema, op FilterOperator, value any) (FilterOperator, any, error) {
	switch op {
	case OpNull, OpNotNull:
		return op, nil, nil
	case OpExists:
		exists, ok := value.(bool)
		if !ok {
			return "", nil, newFilterError(ECodeValueTypeMismatch, fmt.Sprintf("$exists requires a boolean value, got %T", value))
		}
		if exists {
			return OpNotNull, nil, nil
		}
		return OpNull, nil, nil
	}
	if field.DataType != "" && !slices.Contains(OperatorsFor(field.DataType, field.Relation != nil), op) {
		return "", nil, newFilterError(ECodeInvalidOperator, fmt.Sprintf("operator '%s' does not apply to %s field '%s'", op, field.DataType, field.ExternalName))
	}
	switch value.(type) {
	case *GeoDistance, *GeoBoundingBox, *GeoPolygon:
		return op, value, validateGeoValue(value)
	}
	v, err := norm.normalizeValue(field, op, value)
	return op, v, err
}
