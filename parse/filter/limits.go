package filter

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// validateAST checks depth, condition count, and cost limits on a parsed AST.
func (p *FilterParser) validateAST(root *FilterNode) error {
	depth := measureDepth(root, 0)
	if depth > p.config.MaxDepth {
		return &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("filter depth %d exceeds maximum %d", depth, p.config.MaxDepth),
		}
	}

	if n := countRelations(root); n > MaxRelationPredicates {
		return &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeMaxConditions.String(),
			Message: fmt.Sprintf("relation predicate count %d exceeds maximum %d", n, MaxRelationPredicates),
		}
	}

	conditions := countConditions(root)
	if conditions > p.config.MaxConditions {
		return &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeMaxConditions.String(),
			Message: fmt.Sprintf("condition count %d exceeds maximum %d", conditions, p.config.MaxConditions),
		}
	}

	cost := estimateCost(root, &defaultCostFactors)
	if cost > p.config.MaxCost {
		return &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeQueryTooExpensive.String(),
			Message: fmt.Sprintf("query cost %d exceeds maximum %d", cost, p.config.MaxCost),
		}
	}

	return nil
}

// --- Cost Estimation ---

// costFactors weights the cost estimate MaxCost bounds: each condition
// costs BaseCondition, times UnindexedMultiplier on an unindexed field,
// DocumentMultiplier on a JSON / object / array field and
// RelationMultiplier on a relation; geo conditions use the geo multipliers
// instead; unanchored patterns add LeadingWildcardCost; an $or multiplies
// its children's sum. A set costs one condition whatever its size: it binds
// as one parameter, hashed once per statement and probed once per row, and
// MaxArraySize alone bounds it.
type costFactors struct {
	BaseCondition          int
	UnindexedMultiplier    int
	DocumentMultiplier     int
	OrMultiplier           int
	LeadingWildcardCost    int
	RelationMultiplier     int
	GeoIndexedMultiplier   int
	GeoUnindexedMultiplier int
}

var defaultCostFactors = costFactors{
	BaseCondition:          1,
	UnindexedMultiplier:    10,
	DocumentMultiplier:     5,
	OrMultiplier:           2,
	LeadingWildcardCost:    20,
	RelationMultiplier:     8,
	GeoIndexedMultiplier:   5,
	GeoUnindexedMultiplier: 50,
}

func estimateCost(node *FilterNode, factors *costFactors) int {
	if node == nil {
		return 0
	}
	switch node.Op {
	case OpAnd:
		cost := 0
		for _, child := range node.Children {
			cost += estimateCost(child, factors)
		}
		return cost
	case OpOr:
		cost := 0
		for _, child := range node.Children {
			cost += estimateCost(child, factors)
		}
		return cost * factors.OrMultiplier
	case OpNot:
		if len(node.Children) > 0 {
			return estimateCost(node.Children[0], factors)
		}
		return 0
	case OpSome, OpNone:
		// The nested filter runs over the related rows the correlation
		// reaches, or the row's own elements, so its cost adds to the
		// predicate's.
		cost := estimateLeafCost(node, factors)
		if node.Field != nil && node.Field.Relation == nil {
			cost = factors.BaseCondition
			if !node.Field.Indexed {
				cost *= factors.UnindexedMultiplier
			}
		}
		for _, child := range node.Children {
			cost += estimateCost(child, factors)
		}
		return cost
	default:
		return estimateLeafCost(node, factors)
	}
}

func estimateLeafCost(node *FilterNode, factors *costFactors) int {
	cost := factors.BaseCondition
	if node.Field != nil {
		if node.Field.DataType == core.FieldTypeGeo {
			if node.Field.Indexed {
				cost *= factors.GeoIndexedMultiplier
			} else {
				cost *= factors.GeoUnindexedMultiplier
			}
			return cost
		}
		if !node.Field.Indexed {
			cost *= factors.UnindexedMultiplier
		}
		if core.IsDocument(node.Field.DataType) || node.Field.DataType == core.FieldTypeArray {
			cost *= factors.DocumentMultiplier
		}
		if node.Field.Relation != nil {
			cost *= factors.RelationMultiplier
		}
	}
	switch node.Op {
	case OpLike, OpILike:
		if s, ok := node.Value.(string); ok {
			if strings.HasPrefix(s, "%") || strings.HasPrefix(s, "_") {
				cost += factors.LeadingWildcardCost
			}
		}
	case OpContains:
		if node.Field != nil && node.Field.DataType == core.FieldTypeString {
			cost += factors.LeadingWildcardCost // an unanchored substring scan
		}
	}
	return cost
}

func countConditions(node *FilterNode) int {
	if node == nil {
		return 0
	}
	switch node.Op {
	case OpAnd, OpOr, OpNot:
		count := 0
		for _, child := range node.Children {
			count += countConditions(child)
		}
		return count
	case OpSome, OpNone:
		count := 1
		for _, child := range node.Children {
			count += countConditions(child)
		}
		return count
	default:
		return 1
	}
}

// countRelations counts the relation predicates in a filter, nested ones
// included.
func countRelations(node *FilterNode) int {
	if node == nil {
		return 0
	}
	n := 0
	if node.Field != nil && node.Field.Relation != nil {
		n = 1
	}
	for _, child := range node.Children {
		n += countRelations(child)
	}
	return n
}

func measureDepth(node *FilterNode, current int) int {
	if node == nil {
		return current
	}
	maxDepth := current
	for _, child := range node.Children {
		d := measureDepth(child, current+1)
		if d > maxDepth {
			maxDepth = d
		}
	}
	return maxDepth
}
