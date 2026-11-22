package filter

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// reservedParams are the list request parameters (params.go), never
// field filters.
var reservedParams = map[string]bool{
	ParamFilter: true, ParamSort: true, ParamCursor: true, ParamPageSize: true,
	ParamIncludeTotal: true, ParamIncludeDeleted: true, ParamSearch: true,
	ParamFacets: true, ParamFields: true, ParamInclude: true,
}

// IsListParam reports whether name is one of the list request parameters
// (Param*), which a query string never carries as a field filter.
func IsListParam(name string) bool { return reservedParams[name] }

// QueryParamOption configures a QueryParamParser.
type QueryParamOption func(*queryParamConfig)

type queryParamConfig struct {
	lenient    bool
	fieldsOnly bool
	accepted   []string
	parser     *core.ParserConfig
}

// QueryParamAccept names parameters the endpoint reads itself. The parser
// leaves them alone, as it does the list parameters (Param*); any other
// parameter that names no filterable field is E2071.
func QueryParamAccept(names ...string) QueryParamOption {
	return func(c *queryParamConfig) { c.accepted = append(c.accepted, names...) }
}

// QueryParamLenient ignores parameters that name no filterable field instead
// of refusing them. A misspelt filter then widens the result instead of
// failing, so use it only on an endpoint whose parameters are not all known
// in advance; QueryParamAccept is the narrower choice.
func QueryParamLenient() QueryParamOption {
	return func(c *queryParamConfig) { c.lenient = true }
}

// QueryParamFieldsOnly reads every parameter as a field filter, for values
// that hold nothing else: the list parameter names (Param*) are not skipped,
// so a field named like one is filtered on, and any other name that is no
// filterable field is E2071.
func QueryParamFieldsOnly() QueryParamOption {
	return func(c *queryParamConfig) { c.fieldsOnly = true }
}

// QueryParamConfig applies parser limits (MaxArraySize, MaxConditions,
// MaxCost, MaxDepth), the relative-time clock and the codec to query-param
// filters. Default: core.DefaultConfig().
func QueryParamConfig(cfg *core.ParserConfig) QueryParamOption {
	return func(c *queryParamConfig) { c.parser = cfg }
}

// QueryParamParser translates flat URL query params into a FilterAST. Params
// name the fields the JSON grammar does ([ResourceSchema.RequestField]),
// compile to the same operator map and run through [FilterParser.ParseMap],
// so operator allowlists, enums, custom validators, array limits and cost
// limits apply identically on both ingresses.
type QueryParamParser struct {
	schema     *ResourceSchema
	lenient    bool
	fieldsOnly bool
	accepted   map[string]bool
	parser     *FilterParser
}

// NewQueryParamParser creates a QueryParamParser for the given schema. A
// parameter that is neither a list parameter (Param*), a name QueryParamAccept
// lists, nor a filterable field is E2071, so a misspelt filter cannot widen a
// list; QueryParamLenient ignores it instead.
func NewQueryParamParser(schema *ResourceSchema, opts ...QueryParamOption) *QueryParamParser {
	cfg := &queryParamConfig{}
	for _, o := range opts {
		o(cfg)
	}
	accepted := make(map[string]bool, len(cfg.accepted))
	for _, name := range cfg.accepted {
		accepted[name] = true
	}
	return &QueryParamParser{
		schema: schema, lenient: cfg.lenient, fieldsOnly: cfg.fieldsOnly,
		accepted: accepted, parser: NewFilterParser(schema, cfg.parser),
	}
}

// ParseHTTP extracts query params from an *http.Request and parses them.
func (p *QueryParamParser) ParseHTTP(r *http.Request) (*FilterAST, error) {
	return p.Parse(r.URL.Query())
}

// Parse converts url.Values to a FilterAST.
func (p *QueryParamParser) Parse(params url.Values) (*FilterAST, error) {
	operands := flatOperands{}
	for _, key := range slices.Sorted(maps.Keys(params)) { // deterministic SQL text
		values := params[key]
		if len(values) == 0 || (reservedParams[key] && !p.fieldsOnly) || p.accepted[key] {
			continue
		}
		fieldName, op := p.splitFieldOp(key)
		field, ok := p.schema.RequestField(fieldName)
		if !ok {
			if !p.lenient {
				return nil, &core.ParseError{
					Kind:    core.ParseErrorKindFilter,
					Code:    ECodeUnknownField.String(),
					Message: "unknown filter field '" + fieldName + "'",
					Details: map[string]any{core.DetailField: fieldName},
				}
			}
			continue
		}
		// Repeated values on an eq key coalesce into $in; on
		// any other operator they are ambiguous.
		if len(values) > 1 && op != OpEq {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidOperator.String(),
				Message: fmt.Sprintf("parameter '%s' repeated %d times; repeated values are only supported for equality (coalesced to $in)", key, len(values)),
				Details: map[string]any{core.DetailField: fieldName},
			}
		}
		opKey, value, err := queryParamOperand(field, op, values)
		if err != nil {
			return nil, err
		}
		if err := operands.add(fieldName, opKey, key, value); err != nil {
			return nil, err
		}
	}
	return p.parser.ParseMap(operands.operatorMap())
}

// flatOperands are the operator objects flat parameters build, by field and
// operator key, each with the parameter that set it.
type flatOperands map[string]map[string]flatOperand

type flatOperand struct {
	param string
	value any
}

// add sets field's operator opKey from param. Two parameters setting one
// operator (`status=a&status=b` and `status_in=c` are both `$in`) are
// refused rather than one dropping the other's predicate.
func (o flatOperands) add(field, opKey, param string, value any) error {
	ops := o[field]
	if ops == nil {
		ops = map[string]flatOperand{}
		o[field] = ops
	}
	if prev, dup := ops[opKey]; dup {
		return &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidOperator.String(),
			Message: fmt.Sprintf("parameters '%s' and '%s' both set %s on field '%s'", prev.param, param, opKey, field),
			Details: map[string]any{core.DetailField: field},
		}
	}
	ops[opKey] = flatOperand{param: param, value: value}
	return nil
}

// operatorMap is the operands in the JSON grammar's operator-map form.
func (o flatOperands) operatorMap() map[string]any {
	raw := make(map[string]any, len(o))
	for field, ops := range o {
		m := make(map[string]any, len(ops))
		for key, operand := range ops {
			m[key] = operand.value
		}
		raw[field] = m
	}
	return raw
}

func (p *QueryParamParser) splitFieldOp(key string) (string, FilterOperator) {
	// A real field named e.g. "email_like" must not be split into
	// (email, $like) — exact schema match takes precedence.
	if _, ok := p.schema.RequestField(key); ok {
		return key, OpEq
	}
	// Prefer the split that names a real field: "a_not_null" is (a, not_null)
	// when a exists, (a_not, null) when only a_not does.
	first, firstOp := "", OpEq
	for _, s := range querySuffixes {
		if !strings.HasSuffix(key, s.suffix) {
			continue
		}
		fieldName := key[:len(key)-len(s.suffix)]
		if fieldName == "" {
			continue
		}
		if _, ok := p.schema.RequestField(fieldName); ok {
			return fieldName, s.op
		}
		if first == "" {
			first, firstOp = fieldName, s.op
		}
	}
	if first != "" {
		return first, firstOp
	}
	return key, OpEq
}

// queryParamOperand converts a param's text values into the JSON-grammar
// operator key and operand. Numbers stay textual (json.Number) so integers
// reach normalization exactly; type validation is ParseMap's job.
func queryParamOperand(field *FieldSchema, op FilterOperator, values []string) (string, any, error) {
	switch {
	case op == OpNull || op == OpNotNull:
		b, err := strconv.ParseBool(values[0])
		if err != nil {
			return "", nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeValueTypeMismatch.String(),
				Message: "field '" + field.ExternalName + "': _" + string(op) + " requires true or false",
			}
		}
		return "$" + string(op), b, nil
	case len(values) > 1:
		items, err := queryParamValues(field, values)
		return "$" + string(OpIn), items, err
	case op == OpIn || op == OpNin:
		items, err := queryParamValues(field, strings.Split(values[0], ","))
		return "$" + string(op), items, err
	}
	v, err := queryParamValue(field, values[0])
	return "$" + string(op), v, err
}

func queryParamValues(field *FieldSchema, raws []string) ([]any, error) {
	out := make([]any, len(raws))
	for i, raw := range raws {
		v, err := queryParamValue(field, strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func queryParamValue(field *FieldSchema, raw string) (any, error) {
	switch field.DataType {
	case core.FieldTypeInt, core.FieldTypeFloat:
		if field.Wire == core.WireString {
			return raw, nil // the text is the wire form; FromWire reads it exactly
		}
		return json.Number(raw), nil
	case core.FieldTypeBool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeValueTypeMismatch.String(),
				Message: "field '" + field.ExternalName + "': expected true or false",
			}
		}
		return b, nil
	}
	return raw, nil
}
