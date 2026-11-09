package filter

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// Relation predicate limits: relations nest at most
// MaxRelationDepth deep (devices → commands → receipts), and one filter
// holds at most MaxRelationPredicates of them, nested ones included.
const (
	MaxRelationDepth      = 2
	MaxRelationPredicates = 8
)

func isRelationPredicate(op FilterOperator) bool {
	return op == OpSome || op == OpNone || op == OpIs || op == OpIsNot
}

// parseRelated parses `$some` / `$none` / `$is` / `$is_not`: the value is
// the target's own filter, parsed against the target's schema.
func (p *FilterParser) parseRelated(field *FieldSchema, op FilterOperator, value any, depth int) (*FilterNode, error) {
	if isScalarArray(field) {
		return p.parseScalarElements(field, op, value, depth)
	}
	if field.Elements != nil && field.Relation == nil {
		return p.parseElements(field, op, value, depth)
	}
	if field.Relation == nil || field.Related == nil {
		return nil, invalidRelation(field, "has no target resource to filter")
	}
	if p.relDepth >= MaxRelationDepth {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindFilter, Code: ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("relation predicates nest at most %d deep", MaxRelationDepth),
			Details: map[string]any{"field": field.ExternalName},
		}
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires the target's filter object", op, field.ExternalName))
	}
	nested := &FilterParser{schema: field.Related, config: p.config, codec: p.codec, now: p.now, relDepth: p.relDepth + 1}
	child, err := nested.parseNode(obj, depth+1)
	if err != nil {
		return nil, err
	}
	resolved := OpSome
	if op == OpNone || op == OpIsNot {
		resolved = OpNone
	}
	node := &FilterNode{Field: field, Op: resolved}
	if child != nil {
		node.Children = []*FilterNode{child}
	}
	return node, nil
}

// relatedShorthand turns a key comparison on a relation with a target
// resource into the relation predicate it abbreviates: `x` / `$in` are
// `$some` with the target key equal to / among the values, `$ne` / `$nin`
// the matching `$none`, `$null: true` no visible related row and
// `$null: false` some. A relation compared by key alone is returned as is.
func relatedShorthand(n *FilterNode) (*FilterNode, error) {
	f := n.Field
	if f == nil || f.Relation == nil || f.Related == nil {
		return n, nil
	}
	key, err := relatedKey(f)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case OpNull:
		return &FilterNode{Field: f, Op: OpNone}, nil
	case OpNotNull:
		return &FilterNode{Field: f, Op: OpSome}, nil
	case OpEq, OpIn:
		return &FilterNode{Field: f, Op: OpSome, Children: []*FilterNode{{Field: key, Op: n.Op, Value: n.Value}}}, nil
	case OpNe:
		return &FilterNode{Field: f, Op: OpNone, Children: []*FilterNode{{Field: key, Op: OpEq, Value: n.Value}}}, nil
	case OpNin:
		return &FilterNode{Field: f, Op: OpNone, Children: []*FilterNode{{Field: key, Op: OpIn, Value: n.Value}}}, nil
	}
	return nil, invalidRelation(f, fmt.Sprintf("does not compare its key with $%s", n.Op))
}

// relatedKey is the target key's field in the target's schema.
func relatedKey(f *FieldSchema) (*FieldSchema, error) {
	rel := f.Relation
	name := rel.TargetColumn
	if rel.TargetTable != "" {
		name = rel.TargetKey
	}
	if k, ok := f.Related.SystemFields[name]; ok {
		return k, nil
	}
	if k, ok := f.Related.Fields[name]; ok && k.Relation == nil {
		return k, nil
	}
	return nil, invalidRelation(f, fmt.Sprintf("target %s declares no key field %q", rel.RelatedTable(), name))
}

// parseElements parses `$some` / `$none` on a JSON array document: the
// value is a filter on its element paths, held by one element.
func (p *FilterParser) parseElements(field *FieldSchema, op FilterOperator, value any, depth int) (*FilterNode, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires a filter of its element paths", op, field.ExternalName))
	}
	nested := &FilterParser{schema: field.Elements, config: p.config, codec: p.codec, now: p.now, relDepth: p.relDepth}
	child, err := nested.parseNode(obj, depth+1)
	if err != nil {
		return nil, err
	}
	node := &FilterNode{Field: field, Op: op}
	if child != nil {
		node.Children = []*FilterNode{child}
	}
	return node, nil
}

// parseScalarElements parses `$some` / `$none` on a scalar array: the value
// is a condition on one element (parseElementCondition); {} has none, and
// asks whether the array has an element.
func (p *FilterParser) parseScalarElements(field *FieldSchema, op FilterOperator, value any, depth int) (*FilterNode, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires a condition on its elements, such as {\"$in\": [...]}", op, field.ExternalName))
	}
	child, err := p.parseElementCondition(elementOf(field), obj, depth+1)
	if err != nil {
		return nil, err
	}
	node := &FilterNode{Field: field, Op: op}
	if child != nil {
		node.Children = []*FilterNode{child}
	}
	return node, nil
}

// parseElementCondition parses a condition on a scalar array's element:
// operators of the element type, applied to one element together, and
// $and / $or / $not of such conditions. Nil for {}.
func (p *FilterParser) parseElementCondition(elem *FieldSchema, obj map[string]any, depth int) (*FilterNode, error) {
	if depth > p.config.MaxDepth {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("filter depth exceeds maximum %d", p.config.MaxDepth),
		}
	}
	var children []*FilterNode
	for _, key := range slices.Sorted(maps.Keys(obj)) { // deterministic SQL text
		value := obj[key]
		var node *FilterNode
		var err error
		switch op := OperatorFromString[key]; op {
		case OpAnd, OpOr:
			node, err = p.parseElementLogical(elem, op, value, depth)
		case OpNot:
			inner, ok := value.(map[string]any)
			if !ok {
				return nil, newFilterError(ECodeInvalidJSON, "$not requires an object value")
			}
			var child *FilterNode
			if child, err = p.parseElementCondition(elem, inner, depth+1); err == nil && child == nil {
				err = newFilterError(ECodeInvalidJSON, "$not requires a non-empty object")
			}
			node = &FilterNode{Op: OpNot, Children: []*FilterNode{child}}
		default:
			node, err = p.parseOperatorObject(elem, map[string]any{key: value}, depth)
		}
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}
	switch len(children) {
	case 0:
		return nil, nil
	case 1:
		return children[0], nil
	}
	return &FilterNode{Op: OpAnd, Children: children}, nil
}

// parseElementLogical parses $and / $or over element conditions.
func (p *FilterParser) parseElementLogical(elem *FieldSchema, op FilterOperator, value any, depth int) (*FilterNode, error) {
	arr, ok := value.([]any)
	if !ok {
		return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s requires an array value", op))
	}
	var children []*FilterNode
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s array items must be objects", op))
		}
		child, err := p.parseElementCondition(elem, obj, depth+1)
		if err != nil {
			return nil, err
		}
		if child != nil {
			children = append(children, child)
		}
	}
	if len(children) == 0 {
		return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s requires at least one non-empty condition", op))
	}
	return &FilterNode{Op: op, Children: children}, nil
}
