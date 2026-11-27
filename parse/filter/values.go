package filter

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// fromWire reads a request's operand for field under op in the field's wire
// form (FieldSchema.Wire): a comparison's value, or each member of a set.
// Policy constraints and resolved memberships carry Go values and skip it.
func fromWire(field *FieldSchema, op FilterOperator, value any) (any, error) {
	if field.Wire == "" {
		return value, nil
	}
	switch op {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte:
		return wireMember(field, value)
	case OpIn, OpNin:
		items, ok := core.AnySlice(value)
		if !ok {
			return value, nil // normalizeValue refuses a set that is no array
		}
		out := make([]any, len(items))
		for i, item := range items {
			v, err := wireMember(field, item)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	return value, nil
}

func wireMember(field *FieldSchema, value any) (any, error) {
	v, err := core.FromWire(field.Wire, field.DataType, value)
	if err != nil {
		return nil, valueMismatch(field, err)
	}
	return v, nil
}

// normalizeValue validates value for field under op and returns the
// canonical form the AST carries: exact int64/uint64 for
// integers, finite float64 for floats, canonical text for decimals, UUIDs
// and timestamps (relative tokens resolved), generic documents for JSON fields,
// typed geo values for geo operators. SQL builders and in-memory evaluators
// consume this value as-is — nothing downstream re-interprets raw decoder
// output. Every string in it, a document's member names included, is valid
// UTF-8 without U+0000 (core.CheckValueText).
func (p *FilterParser) normalizeValue(field *FieldSchema, op FilterOperator, value any) (any, error) {
	v, err := p.normalizeOperand(field, op, value)
	if err != nil {
		return nil, err
	}
	err = core.CheckValueText(v)
	if err != nil {
		return nil, valueMismatch(field, err)
	}
	return v, nil
}

func (p *FilterParser) normalizeOperand(field *FieldSchema, op FilterOperator, value any) (any, error) {
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
			return nil, fieldError(ECodeInvalidArray, field.ExternalName, fmt.Sprintf("$%s requires an array value", op))
		}
		if len(arr) > p.config.MaxArraySize {
			return nil, core.NewError(ECodeArrayTooLarge, fmt.Sprintf("array size %d exceeds maximum %d", len(arr), p.config.MaxArraySize),
				core.WithField(field.ExternalName))
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
	stored, _, err := core.StoredValue(value)
	var v any
	if err == nil {
		v, err = core.NormalizeScalar(core.FieldTypeString, reflect.Invalid, stored)
	}
	if err == nil && pattern {
		s, _ := v.(string) // FieldTypeString normalizes to a string
		_, err = likeTokens(s)
	}
	if err != nil {
		return nil, valueMismatch(field, err)
	}
	return v, nil
}

// normalizeDocument returns a JSON field's `$eq` / `$ne` / `$contains`
// value as a generic document, bounded like the filter itself: nesting by
// MaxDepth, values by MaxArraySize. Null is not a document comparison —
// `$null` tests for a missing value.
func (p *FilterParser) normalizeDocument(field *FieldSchema, value any) (any, error) {
	if value == nil {
		return nil, fieldError(ECodeValueTypeMismatch, field.ExternalName, "null is not a comparable document; use $null")
	}
	if err := portableNumbers(value); err != nil {
		return nil, valueMismatch(field, err)
	}
	doc, err := core.NormalizeJSON(value)
	if err != nil {
		return nil, valueMismatch(field, err)
	}
	if err := portableNumbers(doc); err != nil {
		return nil, valueMismatch(field, err)
	}
	depth, nodes := jsonShape(doc)
	if depth > p.config.MaxDepth {
		return nil, fieldError(ECodeDepthExceeded, field.ExternalName, fmt.Sprintf("document depth %d exceeds maximum %d", depth, p.config.MaxDepth))
	}
	if nodes > p.config.MaxArraySize {
		return nil, fieldError(ECodeArrayTooLarge, field.ExternalName, fmt.Sprintf("document has %d values, maximum %d", nodes, p.config.MaxArraySize))
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
		return nil, core.NewError(ECodeArrayTooLarge, fmt.Sprintf("array size %d exceeds maximum %d", len(arr), p.config.MaxArraySize),
			core.WithField(field.ExternalName))
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
		return nil, valueMismatch(field, err)
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
		return nil, fieldError(code, field.ExternalName, err.Error(), core.WithCause(err))
	}
	if len(field.EnumValues) > 0 {
		if err := validateEnum(field.EnumValues, v); err != nil {
			return nil, fieldError(ECodeInvalidEnumValue, field.ExternalName, err.Error())
		}
	}
	if field.Validate != nil {
		if err := field.Validate(v); err != nil {
			return nil, valueMismatch(field, err)
		}
	}
	return v, nil
}

// normalizeScalar converts one value to the canonical Go type for
// field.DataType via core.NormalizeScalar, after reading it as the
// database holds it (core.StoredValue): a policy's or a caller's Go value
// may be a driver.Valuer, which stands for the value it writes, and a
// decoded request value is its own. Timestamps additionally accept
// relative tokens (`now-7d`), resolved against the parser clock; a
// token-shaped value that cannot resolve is an error, never a literal passed
// through to SQL.
func (p *FilterParser) normalizeScalar(field *FieldSchema, value any) (any, error) {
	value, _, err := core.StoredValue(value)
	if err != nil {
		return nil, err
	}
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
	if core.EnumAllows(allowed, s) {
		return nil
	}
	return fmt.Errorf("value '%s' not in allowed values %v", s, allowed)
}
