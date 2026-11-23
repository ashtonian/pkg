package filter

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// FilterParser parses JSON filter expressions into a FilterAST.
type FilterParser struct {
	schema *ResourceSchema
	config *core.ParserConfig
	codec  core.Codec
	// now returns the anchor for relative-time token resolution (`now-7d`
	// etc.). Each Parse, ParseMap and ParseBytes reads it once (At).
	now func() time.Time
	// relDepth is how many relation predicates enclose the filter being
	// parsed: a nested filter is parsed against the target's schema.
	relDepth int
}

// NewFilterParser creates a new FilterParser for the given schema and config.
func NewFilterParser(schema *ResourceSchema, config *core.ParserConfig) *FilterParser {
	config = config.Effective()
	return &FilterParser{
		schema: schema,
		config: config,
		codec:  config.GetCodec(),
		now:    config.Now,
	}
}

// At returns a copy of p whose relative-time tokens (`now`, `now-1h`)
// resolve against now. Parsers that read one request (its JSON filter, its
// flat parameters, its policy) take one instant, so `now-1h … now` is an
// hour and `$gte now` with `$lte now` can hold. Each parse of p alone reads
// its clock once.
func (p *FilterParser) At(now time.Time) *FilterParser {
	c := *p
	c.now = func() time.Time { return now }
	return &c
}

// Parse parses a JSON filter string into a FilterAST.
func (p *FilterParser) Parse(queryParam string) (*FilterAST, error) {
	if queryParam == "" {
		return &FilterAST{}, nil
	}
	return p.ParseBytes([]byte(queryParam))
}

// ParseMap parses a pre-decoded filter map into a FilterAST.
// Use this when the input has already been decoded from a non-JSON format
// (protobuf, msgpack, YAML, form data, etc.).
func (p *FilterParser) ParseMap(raw map[string]any) (*FilterAST, error) {
	if len(raw) == 0 {
		return &FilterAST{}, nil
	}
	p = p.At(p.now())

	root, err := p.parseNode(raw, 0)
	if err != nil {
		return nil, err
	}

	if root == nil {
		return &FilterAST{}, nil
	}

	if err := p.validateAST(root); err != nil {
		return nil, err
	}

	return &FilterAST{Root: root}, nil
}

// ParseBytes parses a JSON filter from raw bytes into a FilterAST.
func (p *FilterParser) ParseBytes(data []byte) (*FilterAST, error) {
	if len(data) == 0 {
		return &FilterAST{}, nil
	}

	if p.config.MaxInputSize > 0 && len(data) > p.config.MaxInputSize {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInputTooLarge.String(),
			Message: fmt.Sprintf("input size %d exceeds maximum %d bytes", len(data), p.config.MaxInputSize),
		}
	}

	p = p.At(p.now())
	var raw map[string]any
	if err := p.codec.Decode(data, &raw); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidJSON.String(),
			Message: "invalid JSON syntax",
			Cause:   err,
		}
	}

	root, err := p.parseNode(raw, 0)
	if err != nil {
		return nil, err
	}

	if root == nil {
		return &FilterAST{}, nil
	}

	if err := p.validateAST(root); err != nil {
		return nil, err
	}

	return &FilterAST{Root: root}, nil
}

// And combines filters parsed apart — a request's JSON filter and its flat
// field parameters — into one that matches rows matching all of them, and
// bounds the combination by the parser's limits as one filter: two filters
// each within MaxCost can be over it together. Empty filters are dropped.
func (p *FilterParser) And(filters ...*FilterAST) (*FilterAST, error) {
	var roots []*FilterNode
	for _, f := range filters {
		if f != nil && f.Root != nil {
			roots = append(roots, f.Root)
		}
	}
	switch len(roots) {
	case 0:
		return &FilterAST{}, nil
	case 1:
		return &FilterAST{Root: roots[0]}, nil
	}
	root := &FilterNode{Op: OpAnd, Children: roots}
	if err := p.validateAST(root); err != nil {
		return nil, err
	}
	return &FilterAST{Root: root}, nil
}

func (p *FilterParser) parseNode(raw map[string]any, depth int) (*FilterNode, error) {
	if depth > p.config.MaxDepth {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("filter depth exceeds maximum %d", p.config.MaxDepth),
		}
	}

	var children []*FilterNode

	// Sorted walk: one filter always renders the same SQL text, which
	// statement caches and pg_stat_statements key on.
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		value := raw[key]
		if op, ok := ParseOperator(key); ok && isLogical(op) {
			node, err := p.parseLogical(op, value, depth)
			if err != nil {
				return nil, err
			}
			children = append(children, node)
			continue
		}

		node, err := p.parseFieldCondition(key, value, depth)
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}

	if len(children) == 0 {
		return nil, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}

	return &FilterNode{Op: OpAnd, Children: children}, nil
}

func (p *FilterParser) parseLogical(op FilterOperator, value any, depth int) (*FilterNode, error) {
	switch op {
	case OpNot:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, newFilterError(ECodeInvalidJSON, "$not requires an object value")
		}
		child, err := p.parseNode(obj, depth+1)
		if err != nil {
			return nil, err
		}
		if child == nil { // empty {} yields a nil node — would SIGSEGV the SQL builder
			return nil, newFilterError(ECodeInvalidJSON, "$not requires a non-empty object")
		}
		return &FilterNode{Op: OpNot, Children: []*FilterNode{child}}, nil

	case OpOr, OpAnd:
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
			child, err := p.parseNode(obj, depth+1)
			if err != nil {
				return nil, err
			}
			if child != nil {
				children = append(children, child)
			}
		}
		if len(children) == 0 { // [] or [{}] — zero children builds `WHERE ()`
			return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("$%s requires at least one non-empty condition", op))
		}
		return &FilterNode{Op: op, Children: children}, nil
	}

	return nil, newFilterError(ECodeInvalidJSON, fmt.Sprintf("unknown logical operator: %s", op))
}

func (p *FilterParser) parseFieldCondition(fieldName string, value any, depth int) (*FilterNode, error) {
	field, ok := p.schema.RequestField(fieldName)
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeUnknownField.String(),
			Message: fmt.Sprintf("field '%s' is not filterable", fieldName),
			Details: map[string]any{core.DetailField: fieldName},
		}
	}

	obj, isObj := value.(map[string]any)
	if isObj && hasOperatorKeys(obj) {
		return p.parseOperatorObject(field, obj, depth)
	}

	if !field.AllowsOperator(OpEq) {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidOperator.String(),
			Message: fmt.Sprintf("operator 'eq' not allowed on field '%s'", field.ExternalName),
		}
	}
	value, err := fromWire(field, OpEq, value)
	if err != nil {
		return nil, err
	}
	normalized, err := p.normalizeValue(field, OpEq, value)
	if err != nil {
		return nil, err
	}
	return p.relatedShorthand(&FilterNode{Field: field, Op: OpEq, Value: normalized})
}

func (p *FilterParser) parseOperatorObject(field *FieldSchema, obj map[string]any, depth int) (*FilterNode, error) {
	var children []*FilterNode

	for _, key := range slices.Sorted(maps.Keys(obj)) { // deterministic SQL text
		val := obj[key]
		if key == "" || key[0] != '$' {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidOperator.String(),
				Message: fmt.Sprintf("unexpected key '%s' in operator object (keys must start with $)", key),
			}
		}
		op, ok := ParseOperator(key)
		if !ok {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidOperator.String(),
				Message: fmt.Sprintf("unknown operator '%s'", key),
			}
		}
		if !field.AllowsOperator(op) {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidOperator.String(),
				Message: fmt.Sprintf("operator '%s' not allowed on field '%s'", op, field.ExternalName),
			}
		}

		if isRelationPredicate(op) {
			node, err := p.parseRelated(field, op, val, depth)
			if err != nil {
				return nil, err
			}
			children = append(children, node)
			continue
		}

		if isNullTest(op) {
			// $null:true, $not_null:false and $exists:false select NULL;
			// their opposites select non-NULL.
			want, ok := val.(bool)
			if !ok {
				return nil, newFilterError(ECodeValueTypeMismatch, key+" requires a boolean value")
			}
			isNull := want
			if op != OpNull {
				isNull = !want
			}
			resolved := OpNotNull
			if isNull {
				resolved = OpNull
			}
			node, err := p.relatedShorthand(&FilterNode{Field: field, Op: resolved})
			if err != nil {
				return nil, err
			}
			children = append(children, node)
			continue
		}

		val, err := fromWire(field, op, val)
		if err != nil {
			return nil, err
		}
		normalized, err := p.normalizeValue(field, op, val)
		if err != nil {
			return nil, err
		}
		node, err := p.relatedShorthand(&FilterNode{Field: field, Op: op, Value: normalized})
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}

	if len(children) == 1 {
		return children[0], nil
	}
	return &FilterNode{Op: OpAnd, Children: children}, nil
}

// hasOperatorKeys reports whether a field's object value is an operator
// object rather than a literal: some key is an operator, or every key starts
// with `$`. A misspelt operator ({"$contain": …}) is then an unknown
// operator, never a document compared for equality; a document whose keys
// all start with `$` is written as the operand of `$eq`.
func hasOperatorKeys(obj map[string]any) bool {
	if len(obj) == 0 {
		return false
	}
	dollars := true
	for key := range obj {
		if _, ok := ParseOperator(key); ok {
			return true
		}
		if !strings.HasPrefix(key, "$") {
			dollars = false
		}
	}
	return dollars
}

// NewParsedFilter wraps a FilterAST with computed cost and condition count.
// The filter is parsed for no schema (ParsedFor): an evaluator or a
// membership resolver takes it, a resource's builders do not.
func NewParsedFilter(ast *FilterAST) *ParsedFilter {
	return NewParsedFilterFor(nil, ast)
}

// NewParsedFilterFor wraps ast, which a parser built from schema, as a
// filter ParsedFor(schema). A resource marks the filters it parses with its
// own schema, which it never hands out, so its builders refuse a filter
// parsed against any other.
func NewParsedFilterFor(schema *ResourceSchema, ast *FilterAST) *ParsedFilter {
	f := &ParsedFilter{ast: ast, schema: schema}
	if ast != nil && ast.Root != nil {
		f.cost = estimateCost(ast.Root, &defaultCostFactors)
		f.conditions = countConditions(ast.Root)
	}
	return f
}
