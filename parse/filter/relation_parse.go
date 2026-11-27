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
		return nil, unresolvableRelation(field, "has no target resource to filter")
	}
	if err := p.nestRelation(field); err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires the target's filter object", op, field.ExternalName),
			core.WithField(field.ExternalName))
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

// nestRelation refuses a relation predicate on field past MaxRelationDepth:
// one nests a level below the predicates enclosing this parser.
func (p *FilterParser) nestRelation(field *FieldSchema) error {
	if p.relDepth < MaxRelationDepth {
		return nil
	}
	return core.NewError(ECodeDepthExceeded, fmt.Sprintf("relation predicates nest at most %d deep", MaxRelationDepth),
		core.WithField(field.ExternalName))
}

// relatedShorthand is the request's key comparison on a relation as the
// predicate it abbreviates (the package's relatedShorthand), held to
// MaxRelationDepth as `$some` is: the predicate nests a level, however
// short its spelling.
func (p *FilterParser) relatedShorthand(n *FilterNode) (*FilterNode, error) {
	if n.Field != nil && n.Field.Relation != nil {
		if err := p.nestRelation(n.Field); err != nil {
			return nil, err
		}
	}
	return relatedShorthand(n)
}

// relatedShorthand turns a key comparison on a relation with a target
// resource into the relation predicate it abbreviates: `x` / `$in` are
// `$some` with the target key equal to / among the values, `$ne` / `$nin`
// the matching `$none`, `$null: true` no visible related row and
// `$null: false` some. A relation compared by key alone is returned as is.
// A target key of several columns takes only the NULL tests: a comparison
// names one value.
func relatedShorthand(n *FilterNode) (*FilterNode, error) {
	f := n.Field
	if f == nil || f.Relation == nil || f.Related == nil {
		return n, nil
	}
	switch n.Op {
	case OpNull:
		return &FilterNode{Field: f, Op: OpNone}, nil
	case OpNotNull:
		return &FilterNode{Field: f, Op: OpSome}, nil
	}
	if len(f.Relation.TargetColumns) != 1 {
		return nil, invalidRelation(f, fmt.Sprintf("compares a key of several columns, which $%s cannot name; filter its target with $some", n.Op))
	}
	key, err := relatedKeyField(f, 0)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case OpEq, OpIn:
		return &FilterNode{Field: f, Op: OpSome, Children: []*FilterNode{{Field: key, Op: n.Op, Value: n.Value}}}, nil
	case OpNe:
		return &FilterNode{Field: f, Op: OpNone, Children: []*FilterNode{{Field: key, Op: OpEq, Value: n.Value}}}, nil
	case OpNin:
		return &FilterNode{Field: f, Op: OpNone, Children: []*FilterNode{{Field: key, Op: OpIn, Value: n.Value}}}, nil
	}
	return nil, invalidRelation(f, fmt.Sprintf("does not compare its key with $%s", n.Op))
}

// relatedKeyField is the field, in the target's schema, of the target key
// column TargetColumns[i] holds.
func relatedKeyField(f *FieldSchema, i int) (*FieldSchema, error) {
	rel := f.Relation
	if f.Related == nil {
		return nil, unresolvableRelation(f, "has no target resource to type its key")
	}
	name := rel.TargetColumns[i]
	if rel.TargetTable != "" {
		name = rel.TargetKeys[i]
	}
	if k, ok := f.Related.SystemFields[name]; ok {
		return k, nil
	}
	if k, ok := f.Related.Fields[name]; ok && k.Relation == nil {
		return k, nil
	}
	return nil, unresolvableRelation(f, fmt.Sprintf("has a target, %s, that declares no key field %q", rel.RelatedTable(), name))
}

// parseElements parses `$some` / `$none` on a JSON array document: the
// value is a filter on its element paths, held by one element.
func (p *FilterParser) parseElements(field *FieldSchema, op FilterOperator, value any, depth int) (*FilterNode, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires a filter of its element paths", op, field.ExternalName),
			core.WithField(field.ExternalName))
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
		return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s on '%s' requires a condition on its elements, such as {\"$in\": [...]}", op, field.ExternalName),
			core.WithField(field.ExternalName))
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
		return nil, core.NewError(ECodeDepthExceeded, fmt.Sprintf("filter depth exceeds maximum %d", p.config.MaxDepth))
	}
	var children []*FilterNode
	for _, key := range slices.Sorted(maps.Keys(obj)) { // deterministic SQL text
		value := obj[key]
		var node *FilterNode
		var err error
		op, _ := ParseOperator(key)
		switch op {
		case OpAnd, OpOr:
			node, err = p.parseElementLogical(elem, op, value, depth)
		case OpNot:
			inner, ok := value.(map[string]any)
			if !ok {
				return nil, core.NewError(ECodeInvalidJSON, "$not requires an object value", core.WithField(elem.ExternalName))
			}
			var child *FilterNode
			if child, err = p.parseElementCondition(elem, inner, depth+1); err == nil && child == nil {
				err = core.NewError(ECodeInvalidJSON, "$not requires a non-empty object", core.WithField(elem.ExternalName))
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
		return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s requires an array value", op), core.WithField(elem.ExternalName))
	}
	var children []*FilterNode
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s array items must be objects", op), core.WithField(elem.ExternalName))
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
		return nil, core.NewError(ECodeInvalidJSON, fmt.Sprintf("$%s requires at least one non-empty condition", op), core.WithField(elem.ExternalName))
	}
	return &FilterNode{Op: op, Children: children}, nil
}
