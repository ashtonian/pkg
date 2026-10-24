package filter

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// FilterParser parses JSON filter expressions into a FilterAST.
type FilterParser struct {
	schema *ResourceSchema
	config *core.ParserConfig
	codec  core.Codec
	// now returns the anchor for relative-time token resolution
	// (`now-7d` etc., ). Zero value defaults to time.Now.UTC.
	now func() time.Time
	// relDepth is how many relation predicates enclose the filter being
	// parsed: a nested filter is parsed against the target's schema.
	relDepth int
}

// NewFilterParser creates a new FilterParser for the given schema and config.
func NewFilterParser(schema *ResourceSchema, config *core.ParserConfig) *FilterParser {
	if config == nil {
		config = core.DefaultConfig()
	}
	now := config.Clock
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &FilterParser{
		schema: schema,
		config: config,
		codec:  config.GetCodec(),
		now:    now,
	}
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

func (p *FilterParser) parseNode(raw map[string]any, depth int) (*FilterNode, error) {
	if depth > p.config.MaxDepth {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("filter depth exceeds maximum %d", p.config.MaxDepth),
		}
	}

	var children []*FilterNode

	// Sorted walk: map iteration produced nondeterministic child order →
	// unstable SQL text across runs (pg_stat_statements bloat, flaky
	// snapshots). .
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		value := raw[key]
		op, isLogical := OperatorFromString[key]
		if isLogical && (op == OpOr || op == OpAnd || op == OpNot) {
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
	// User-facing fields take precedence; fall back to the system-fields
	// registry (id, tenant_id, deleted_at) so callers can filter by
	// primary key without a per-resource opt-in tag.
	field, ok := p.schema.Fields[fieldName]
	if !ok && p.schema.SystemFields != nil {
		field, ok = p.schema.SystemFields[fieldName]
	}
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeUnknownField.String(),
			Message: fmt.Sprintf("field '%s' is not filterable", fieldName),
			Details: map[string]any{"field": fieldName},
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
	normalized, err := p.normalizeValue(field, OpEq, value)
	if err != nil {
		return nil, err
	}
	return relatedShorthand(&FilterNode{Field: field, Op: OpEq, Value: normalized})
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
		op, ok := OperatorFromString[key]
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
			node, err := relatedShorthand(&FilterNode{Field: field, Op: resolved})
			if err != nil {
				return nil, err
			}
			children = append(children, node)
			continue
		}

		normalized, err := p.normalizeValue(field, op, val)
		if err != nil {
			return nil, err
		}
		node, err := relatedShorthand(&FilterNode{Field: field, Op: op, Value: normalized})
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

// normalizeValue validates value for field under op and returns the
// canonical form the AST carries: exact int64/uint64 for
// integers, finite float64 for floats, canonical strings for UUIDs and
// timestamps (relative tokens resolved), generic documents for JSON fields,
// typed geo values for geo operators. SQL builders and in-memory evaluators
// consume this value as-is — nothing downstream re-interprets raw decoder
// output.
func (p *FilterParser) normalizeValue(field *FieldSchema, op FilterOperator, value any) (any, error) {
	switch {
	case op == OpHasKey:
		return p.normalizeText(field, value, false)
	case core.IsDocument(field.DataType):
		return p.normalizeDocument(field, value)
	case field.DataType == core.FieldTypeArray:
		return p.normalizeElements(field, value)
	case op == OpContains:
		return p.normalizeText(field, value, false)
	case op == OpLike || op == OpILike:
		return p.normalizeText(field, value, true)
	case op == OpIn || op == OpNin:
		arr, ok := core.AnySlice(value)
		if !ok {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidArray.String(),
				Message: fmt.Sprintf("field '%s': $%s requires an array value", field.ExternalName, op),
			}
		}
		if len(arr) > p.config.MaxArraySize {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeArrayTooLarge.String(),
				Message: fmt.Sprintf("array size %d exceeds maximum %d", len(arr), p.config.MaxArraySize),
			}
		}
		out := make([]any, len(arr))
		for i, item := range arr {
			v, err := p.normalizeMember(field, item)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case isGeoOp(op):
		return parseGeoValue(field, op, value)
	}
	return p.normalizeMember(field, value)
}

// normalizeText returns a `$contains` substring or a `$like` / `$ilike`
// pattern. Enum and custom validators do not apply: the value is a
// fragment, not a field value.
func (p *FilterParser) normalizeText(field *FieldSchema, value any, pattern bool) (any, error) {
	v, err := core.NormalizeScalar(core.FieldTypeString, reflect.Invalid, value)
	if err == nil && pattern {
		s, _ := v.(string) // FieldTypeString normalizes to a string
		_, err = likeTokens(s)
	}
	if err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
			Cause:   err,
		}
	}
	return v, nil
}

// normalizeDocument returns a JSON field's `$eq` / `$ne` / `$contains`
// value as a generic document, bounded like the filter itself: nesting by
// MaxDepth, values by MaxArraySize. Null is not a document comparison —
// `$null` tests for a missing value.
func (p *FilterParser) normalizeDocument(field *FieldSchema, value any) (any, error) {
	if value == nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': null is not a comparable document; use $null", field.ExternalName),
			Details: map[string]any{"field": field.ExternalName},
		}
	}
	if err := portableNumbers(value); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
		}
	}
	doc, err := core.NormalizeJSON(value)
	if err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
			Cause:   err,
		}
	}
	if err := portableNumbers(doc); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
		}
	}
	depth, nodes := jsonShape(doc)
	if depth > p.config.MaxDepth {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeDepthExceeded.String(),
			Message: fmt.Sprintf("field '%s': document depth %d exceeds maximum %d", field.ExternalName, depth, p.config.MaxDepth),
		}
	}
	if nodes > p.config.MaxArraySize {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeArrayTooLarge.String(),
			Message: fmt.Sprintf("field '%s': document has %d values, maximum %d", field.ExternalName, nodes, p.config.MaxArraySize),
		}
	}
	return doc, nil
}

// normalizeElements returns an array field's `$contains` value as the
// elements every match must hold, each normalized to the element type. A
// lone scalar means a one-element set.
func (p *FilterParser) normalizeElements(field *FieldSchema, value any) (any, error) {
	arr, ok := core.AnySlice(value)
	if !ok {
		arr = []any{value}
	}
	if len(arr) > p.config.MaxArraySize {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeArrayTooLarge.String(),
			Message: fmt.Sprintf("array size %d exceeds maximum %d", len(arr), p.config.MaxArraySize),
		}
	}
	// Enum values constrain elements; Validate judges a whole field value.
	elem := &FieldSchema{ExternalName: field.ExternalName, DataType: field.Elem, NumberKind: field.NumberKind, EnumValues: field.EnumValues}
	out := make([]any, len(arr))
	for i, item := range arr {
		v, err := p.normalizeMember(elem, item)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	// SQLite stores arrays as JSON text and compares their elements as JSON
	// numbers, so elements share the document domain.
	if err := portableNumbers(out); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeValueTypeMismatch.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
		}
	}
	return out, nil
}

// portableNumbers rejects JSON numbers backends would compare differently
// an integer above math.MaxInt64 (SQLite reads it as a
// rounded REAL) or a decimal literal a float64 does not hold exactly (see
// core.PortableDecimal). Rejecting the predicate keeps one answer on every
// backend instead of silently rounding it on some.
func portableNumbers(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if err := portableNumbers(e); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	case []any:
		for i, e := range x {
			if err := portableNumbers(e); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	case uint64:
		if x > math.MaxInt64 {
			return fmt.Errorf("%d is outside the portable JSON integer range (int64)", x)
		}
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			if _, err := strconv.ParseInt(s, 10, 64); err != nil {
				return fmt.Errorf("%s is outside the portable JSON integer range (int64)", s)
			}
			return nil
		}
		if !core.PortableDecimal(s) {
			return fmt.Errorf("%s has more precision than a float64 holds; send its shortest float64 form", s)
		}
	}
	return nil
}

// normalizeMember runs one scalar through type normalization, the enum
// allowlist and the field's custom validator, in that order.
func (p *FilterParser) normalizeMember(field *FieldSchema, value any) (any, error) {
	v, err := p.normalizeScalar(field, value)
	if err != nil {
		code := ECodeValueTypeMismatch
		if field.DataType == core.FieldTypeUUID {
			code = ECodeInvalidUUID
		}
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    code.String(),
			Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
			Details: map[string]any{"field": field.ExternalName},
			Cause:   err,
		}
	}
	if len(field.EnumValues) > 0 {
		if err := validateEnum(field.EnumValues, v); err != nil {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidEnumValue.String(),
				Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
				Details: map[string]any{"field": field.ExternalName},
			}
		}
	}
	if field.Validate != nil {
		if err := field.Validate(v); err != nil {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeValueTypeMismatch.String(),
				Message: fmt.Sprintf("field '%s': %v", field.ExternalName, err),
				Details: map[string]any{"field": field.ExternalName},
				Cause:   err,
			}
		}
	}
	return v, nil
}

// normalizeScalar converts one value to the canonical Go type for
// field.DataType via core.NormalizeScalar. Timestamps additionally accept
// relative tokens (`now-7d`), resolved against the parser clock; a
// token-shaped value that cannot resolve is an error, never a literal passed
// through to SQL.
func (p *FilterParser) normalizeScalar(field *FieldSchema, value any) (any, error) {
	if field.DataType == core.FieldTypeTimestamp {
		if s, ok := value.(string); ok {
			resolved, isToken, err := ResolveRelativeTime(s, p.now())
			if err != nil {
				return nil, err
			}
			if isToken {
				return core.FormatTimestamp(resolved), nil
			}
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return nil, fmt.Errorf("invalid timestamp %q, expected RFC 3339 or a relative token such as now-7d", s)
			}
		}
	}
	return core.NormalizeScalar(field.DataType, field.NumberKind, value)
}

func validateEnum(allowed []string, value any) error {
	s, ok := value.(string)
	if !ok {
		return nil
	}
	if slices.Contains(allowed, s) {
		return nil
	}
	return fmt.Errorf("value '%s' not in allowed values %v", s, allowed)
}

func hasOperatorKeys(obj map[string]any) bool {
	for key := range obj {
		if _, ok := OperatorFromString[key]; ok {
			return true
		}
	}
	return false
}

// --- Sort Parser ---

// ParseSort parses a sort parameter string.
func ParseSort(schema *SortSchema, sortParam string) (*SortResult, error) {
	if schema == nil {
		return nil, newSortError(ECodeSortFieldNotAllowed, "no sort schema provided")
	}

	maxFields := schema.MaxFields
	if maxFields == 0 {
		maxFields = 3
	}

	if sortParam == "" {
		if schema.DefaultField == "" {
			return &SortResult{}, nil
		}
		fieldSchema, ok := schema.Fields[schema.DefaultField]
		if !ok {
			return &SortResult{}, nil
		}
		dir := schema.DefaultDir
		if dir == "" {
			dir = SortAsc
		}
		return &SortResult{
			Fields: []SortField{{Name: schema.DefaultField, Schema: fieldSchema, Direction: dir}},
		}, nil
	}

	parts := strings.Split(sortParam, ",")
	if len(parts) > maxFields {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindSort,
			Code:    ECodeSortFieldLimit.String(),
			Message: fmt.Sprintf("sort field count %d exceeds maximum %d", len(parts), maxFields),
		}
	}

	fields := make([]SortField, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dir := SortAsc
		name := part
		if strings.HasPrefix(part, "-") {
			dir = SortDesc
			name = part[1:]
		} else if strings.HasPrefix(part, "+") {
			name = part[1:]
		}
		fieldSchema, ok := schema.Fields[name]
		if !ok {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindSort,
				Code:    ECodeSortFieldNotAllowed.String(),
				Message: fmt.Sprintf("sort field '%s' is not allowed", name),
				Details: map[string]any{"field": name},
			}
		}
		fields = append(fields, SortField{Name: name, Schema: fieldSchema, Direction: dir})
	}

	return &SortResult{Fields: fields}, nil
}

// ToSQL renders the sort's ORDER BY in dialect d, as a keyset orders it:
// code-point text, NULL greatest on nullable keys, paths through
// Dialect.PathExpr. A list query appends its tie-breaker itself.
func (s *SortResult) ToSQL(d Dialect) string {
	if s == nil || len(s.Fields) == 0 {
		return ""
	}
	return orderByKeys(d, Keyset(s, CursorKey{}), false)
}

// NewParsedFilter wraps a FilterAST with computed cost and condition count.
func NewParsedFilter(ast *FilterAST) *ParsedFilter {
	cost := 0
	conditions := 0
	if ast != nil && ast.Root != nil {
		cost = estimateCost(ast.Root, &defaultCostFactors)
		conditions = countConditions(ast.Root)
	}
	return &ParsedFilter{AST: ast, EstimatedCost: cost, ConditionCount: conditions}
}

// --- Cost Estimation ---

// costFactors weights the cost estimate MaxCost bounds: each condition
// costs BaseCondition, times UnindexedMultiplier on an unindexed field,
// DocumentMultiplier on a JSON / object / array field and
// RelationMultiplier on a relation; geo conditions use the geo multipliers
// instead; unanchored patterns add LeadingWildcardCost and membership lists
// PerArrayItemCost per member; an $or multiplies its children's sum.
type costFactors struct {
	BaseCondition          int
	UnindexedMultiplier    int
	DocumentMultiplier     int
	OrMultiplier           int
	LeadingWildcardCost    int
	PerArrayItemCost       int
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
	PerArrayItemCost:       1,
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
	case OpIn, OpNin:
		if arr, ok := node.Value.([]any); ok {
			cost += len(arr) * factors.PerArrayItemCost
		}
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
