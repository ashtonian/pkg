package filter

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// ErrPolicyInvalid is a policy the schema cannot compile: no schema, a field
// the schema does not have, an operator the field's type does not take, a
// value that does not fit the field, a membership lookup that does not
// apply, or a negated empty group. Policy is the server's, never the
// request's, so it is a plain error and never a *core.ParseError.
var ErrPolicyInvalid = errors.New("filter: invalid policy constraint")

// policyFault is err, raised compiling the policy constraint on field, as
// the server fault it is: wrapping ErrPolicyInvalid, and keeping a
// ParseError's message and cause but not the ParseError, whose code would
// answer the request as at fault.
func policyFault(field string, err error) error {
	pe, ok := errors.AsType[*core.ParseError](err)
	switch {
	case !ok && errors.Is(err, ErrPolicyInvalid):
		return fmt.Errorf("field %q: %w", field, err)
	case !ok:
		return fmt.Errorf("%w: field %q: %w", ErrPolicyInvalid, field, err)
	}
	fault := &policyError{msg: fmt.Sprintf("%v: field %q: %s", ErrPolicyInvalid, field, pe.Message)}
	if _, nested := errors.AsType[*core.ParseError](pe.Cause); !nested {
		fault.cause = pe.Cause
	}
	return fault
}

// policyError is a policy fault whose message already states its cause, a
// ParseError's text, so the cause stays in the chain without being written
// twice.
type policyError struct {
	msg   string
	cause error
}

func (e *policyError) Error() string { return e.msg }

func (e *policyError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrPolicyInvalid}
	}
	return []error{ErrPolicyInvalid, e.cause}
}

// PolicyConstraint represents a single policy-derived filter condition.
type PolicyConstraint struct {
	Field    string
	Operator FilterOperator
	Value    any
	Negate   bool
	// UnknownIfInvalid compiles a value that does not apply to the field —
	// its type, or the operator's — to SQL's unknown instead of an error:
	// no row satisfies it, nor its negation, so it narrows whatever group
	// holds it. Authorization sets it: a policy value no row can hold
	// neither fails a list nor widens one.
	UnknownIfInvalid bool

	// RawSQL is a pre-built SQL expression injected into the WHERE clause
	// in parentheses, so it is one operand of the AND, OR or NOT that holds
	// it: `a OR b` needs no parentheses of its own. When set,
	// Field/Operator/Value are ignored. Use parameterized placeholders ($1,
	// $2) with RawArgs. This enables subquery-based constraints like
	// grant-aware scoping. Every `$N` in it is a placeholder, inside a string
	// literal or dollar-quoted text too: write a literal `$` some other way.
	RawSQL  string
	RawArgs []any

	// Member makes the constraint a membership: Field's value is one of the
	// values the lookup projects. Operator, Value and RawSQL
	// are unused. Every backend evaluates it — SQL as a subquery, memory
	// through ResolveMemberships — unlike RawSQL, which memory cannot.
	Member *Lookup

	// Fields makes the constraint a key constraint over these columns, in
	// key order, in place of Field: under OpEq, Value is one core.Key and
	// the row's columns equal it; under OpIn, Value is a []core.Key and the
	// row's columns, together, equal one of them, SQL binding the set as one
	// parameter. Each key holds a value per column. One column compiles as
	// Field does; a set over several compares string, UUID and integer
	// columns only.
	Fields []string
}

// PolicyConstraints is a composable collection of constraints.
type PolicyConstraints struct {
	Constraints []*PolicyConstraint
	Or          []*PolicyConstraints
	And         []*PolicyConstraints
	// Not negates the group: a row is admitted when it does not satisfy it.
	Not bool
}

// IsEmpty reports whether pc constrains nothing: no constraints, no groups
// and no negation. A negated group is never empty, so it always reaches
// compilation, which refuses one with nothing to negate (E2072) rather than
// let it admit every row.
func (pc *PolicyConstraints) IsEmpty() bool {
	if pc == nil {
		return true
	}
	return !pc.Not && len(pc.Constraints) == 0 && len(pc.Or) == 0 && len(pc.And) == 0
}

// ApplyPolicyConstraints merges policy constraints into the filter AST.
// Constraint values are normalized as request values are,
// since dialects and the memory backend read canonical values only; request
// limits do not apply, as policy lists (allowed IDs) are trusted and may be
// long. Relative-time tokens resolve against one reading of the wall clock.
// A policy that does not compile is an error wrapping ErrPolicyInvalid.
func ApplyPolicyConstraints(schema *ResourceSchema, userAST *FilterAST, pc *PolicyConstraints) (*FilterAST, error) {
	return applyPolicyConstraints(schema, userAST, pc, time.Now().UTC())
}

// applyPolicyConstraints is ApplyPolicyConstraints with every relative-time
// token resolved against now.
func applyPolicyConstraints(schema *ResourceSchema, userAST *FilterAST, pc *PolicyConstraints, now time.Time) (*FilterAST, error) {
	if pc == nil || pc.IsEmpty() {
		return userAST, nil
	}
	if schema == nil {
		return nil, fmt.Errorf("%w: no schema to apply it to", ErrPolicyInvalid)
	}
	norm := NewFilterParser(schema, &core.ParserConfig{MaxDepth: math.MaxInt, MaxArraySize: math.MaxInt}).At(now)
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
			return nil, fmt.Errorf("%w: a negated group holds no constraint", ErrPolicyInvalid)
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
	if c.Operator == OpUnknown { // its own negation: NOT unknown is unknown
		return unknownNode(), nil
	}
	if len(c.Fields) > 0 {
		return keyNode(norm, schema, c)
	}

	field, ok, err := schema.PolicyField(c.Field)
	if err != nil {
		return nil, policyFault(c.Field, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: field %q is not a field of %s", ErrPolicyInvalid, c.Field, schema.Name)
	}
	if c.Member != nil {
		node, err := memberNode(field, c.Member)
		if err != nil {
			return nil, policyFault(c.Field, err)
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
		return nil, policyFault(c.Field, err)
	}
	node := &FilterNode{Field: field, Op: op, Value: value}
	if c.Negate {
		return &FilterNode{Op: OpNot, Children: []*FilterNode{node}}, nil
	}
	return node, nil
}

// unknownSQL is how SQL writes its unknown (OpUnknown).
const unknownSQL = "NULL"

// UnknownConstraint returns the constraint whose value is SQL's unknown
// (OpUnknown): no row satisfies it, nor its negation. A group holding it
// admits no row; a negated group holding it still excludes the rows its
// other constraints match.
func UnknownConstraint() *PolicyConstraint {
	return &PolicyConstraint{Operator: OpUnknown}
}

func unknownNode() *FilterNode {
	return &FilterNode{Op: OpUnknown}
}

// policyValue returns the operator and canonical value a constraint compiles
// to. The NULL tests compile to a bare `$null` or `$not_null`: without a
// value they are the test their operator names, and a boolean means what the
// filter grammar means by it (`$null: false` is `$not_null`, `$exists` maps
// onto them); typed geo shapes are validated as built; everything else
// normalizes through the request path. The operator must apply to the
// field's type — policies bypass a field's declared operators, not its
// type.
func policyValue(norm *FilterParser, field *FieldSchema, op FilterOperator, value any) (FilterOperator, any, error) {
	switch op {
	case OpNull, OpNotNull, OpExists:
		isNull := op == OpNull
		if value != nil || op == OpExists {
			want, ok := value.(bool)
			if !ok {
				return "", nil, newFilterError(ECodeValueTypeMismatch, fmt.Sprintf("$%s requires a boolean value, got %s", op, gowalker.TypeText(reflect.TypeOf(value))))
			}
			if !want {
				isNull = !isNull
			}
		}
		if isNull {
			return OpNull, nil, nil
		}
		return OpNotNull, nil, nil
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
