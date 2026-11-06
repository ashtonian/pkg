package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// UsageBounds describes one conjunction of translated bounds. Target is
// value, duration (integer nanoseconds), length (Unicode characters), items or
// properties. ZeroAllowed means
// this clause also admits the field's zero value, including nil for pointers
// and nullable containers. Other Bounds clauses still apply.
type UsageBounds struct {
	Target           string      `json:"target"`
	Minimum          json.Number `json:"minimum,omitempty"`
	Maximum          json.Number `json:"maximum,omitempty"`
	ExclusiveMinimum bool        `json:"exclusive_minimum,omitempty"`
	ExclusiveMaximum bool        `json:"exclusive_maximum,omitempty"`
	ZeroAllowed      bool        `json:"zero_allowed,omitempty"`
}

func requiredBeforeOmission(tag string) bool {
	for rule := range strings.SplitSeq(tag, ",") {
		switch rule {
		case "required":
			return true
		case "omitempty", "dive", "keys", "endkeys":
			return false
		}
	}
	return false
}

func usageBoundsText(bounds []UsageBounds) string {
	var clauses []string
	for _, b := range bounds {
		var parts []string
		if b.Minimum != "" {
			op := ">="
			if b.ExclusiveMinimum {
				op = ">"
			}
			parts = append(parts, b.Target+" "+op+" "+usageBoundText(b.Target, b.Minimum))
		}
		if b.Maximum != "" {
			op := "<="
			if b.ExclusiveMaximum {
				op = "<"
			}
			parts = append(parts, b.Target+" "+op+" "+usageBoundText(b.Target, b.Maximum))
		}
		text := strings.Join(parts, ", ")
		if b.ZeroAllowed {
			text = "zero or (" + text + ")"
		}
		clauses = append(clauses, text)
	}
	return strings.Join(clauses, "; ")
}

func usageBoundText(target string, bound json.Number) string {
	if target == "duration" {
		if nanos, err := bound.Int64(); err == nil {
			return time.Duration(nanos).String()
		}
	}
	return bound.String()
}

func omissionSchema(t reflect.Type) (map[string]any, bool) {
	w := &valueTree{raw: true, visiting: map[visit]bool{}}
	n := w.value(reflect.Zero(t), site{})
	if w.err != nil {
		return nil, false
	}
	return map[string]any{"const": n.plain()}, true
}

func usageConstraints(f Field) ([]string, []UsageBounds, error) {
	n, err := metadataOf(f.Type)
	if err != nil {
		return nil, nil, err
	}
	var s map[string]any
	switch n.kind {
	case nodeMap, nodeBranch:
		s = map[string]any{"type": "object"}
	case nodeList:
		s = map[string]any{"type": "array"}
	default:
		s = leafSchema(derefType(f.Type))
	}
	s = orNull(s, f.Type)
	visible := !f.Sensitive && !mayHoldSensitive(f.Type, map[reflect.Type]bool{})
	rules(s, n, f.Tag.Get("validate"), visible, f.Type)
	var allowed []string
	if visible {
		values, _ := constraintEnums(s)
		for _, v := range values {
			if v == nil {
				allowed = append(allowed, "null")
			} else {
				allowed = append(allowed, fmt.Sprint(v))
			}
		}
	}
	zero, ok := omissionSchema(f.Type)
	var bounds []UsageBounds
	collectBounds(s, zero["const"], ok, false, &bounds)
	return allowed, bounds, nil
}

// constraintEnums finds a finite set, then intersects it with the other
// translated constraints. An unbounded alternative cannot become an enum.
func constraintEnums(s map[string]any) ([]any, bool) {
	var values []any
	finite := false
	if e, ok := s["enum"].([]any); ok {
		values = slices.Clone(e)
		finite = true
	}
	if v, ok := s["const"]; ok {
		values = []any{v}
		finite = true
	}
	if branches, ok := s["anyOf"].([]any); ok {
		var union []any
		bounded := true
		for _, b := range branches {
			candidates, ok := constraintEnums(b.(map[string]any))
			if !ok {
				bounded = false
				break
			}
			for _, v := range candidates {
				if !slices.ContainsFunc(union, func(existing any) bool { return constraintEqual(existing, v) }) {
					union = append(union, v)
				}
			}
		}
		if bounded && !finite {
			values, finite = union, true
		}
	}
	if clauses, ok := s["allOf"].([]any); ok {
		for _, c := range clauses {
			if candidates, ok := constraintEnums(c.(map[string]any)); ok && !finite {
				values, finite = candidates, true
			}
		}
	}
	if finite {
		values = slices.DeleteFunc(values, func(v any) bool { return !matchesConstraint(s, v) })
	}
	return values, finite
}

func constraintEqual(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	x, xok := constraintNumber(a)
	y, yok := constraintNumber(b)
	return xok && yok && x.Cmp(y) == 0
}

func constraintNumber(v any) (*big.Rat, bool) {
	switch v.(type) {
	case int, int64, uint64, float64, json.Number:
		return new(big.Rat).SetString(fmt.Sprint(v))
	}
	return nil, false
}

func setBound(s map[string]any, key string, value any, minimum bool) {
	before, had := constraintNumber(s[key])
	after, ok := constraintNumber(value)
	if !ok {
		return
	}
	if !had || minimum && after.Cmp(before) > 0 || !minimum && after.Cmp(before) < 0 {
		s[key] = value
	}
}

func matchesConstraint(s map[string]any, v any) bool {
	if bounds, ok := s["x-config-duration-bounds"].(map[string]any); ok {
		text, ok := v.(string)
		if !ok {
			return false
		}
		duration, err := time.ParseDuration(text)
		if err != nil || !matchesConstraint(bounds, int64(duration)) {
			return false
		}
	}
	if constant, ok := s["const"]; ok && !constraintEqual(constant, v) {
		return false
	}
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return constraintEqual(e, v) }) {
		return false
	}
	if not, ok := s["not"].(map[string]any); ok && matchesConstraint(not, v) {
		return false
	}
	if branches, ok := s["anyOf"].([]any); ok && !slices.ContainsFunc(branches, func(b any) bool { return matchesConstraint(b.(map[string]any), v) }) {
		return false
	}
	if clauses, ok := s["allOf"].([]any); ok && slices.ContainsFunc(clauses, func(c any) bool { return !matchesConstraint(c.(map[string]any), v) }) {
		return false
	}
	var size *big.Rat
	minKey, maxKey := "minimum", "maximum"
	switch s["type"] {
	case "null":
		return v == nil
	case "string":
		text, ok := v.(string)
		if !ok {
			return false
		}
		size = new(big.Rat).SetInt64(int64(utf8.RuneCountInString(text)))
		minKey, maxKey = "minLength", "maxLength"
	case "array":
		list, ok := v.([]any)
		if !ok {
			return false
		}
		size = new(big.Rat).SetInt64(int64(len(list)))
		minKey, maxKey = "minItems", "maxItems"
	case "object":
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		size = new(big.Rat).SetInt64(int64(len(object)))
		minKey, maxKey = "minProperties", "maxProperties"
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer", "number":
		var ok bool
		size, ok = constraintNumber(v)
		if !ok {
			return false
		}
	default:
		return true
	}
	if n, ok := constraintNumber(s[minKey]); ok && size.Cmp(n) < 0 {
		return false
	}
	if n, ok := constraintNumber(s[maxKey]); ok && size.Cmp(n) > 0 {
		return false
	}
	if minKey == "minimum" {
		if n, ok := constraintNumber(s["exclusiveMinimum"]); ok && size.Cmp(n) <= 0 {
			return false
		}
		if n, ok := constraintNumber(s["exclusiveMaximum"]); ok && size.Cmp(n) >= 0 {
			return false
		}
	}
	return true
}

func collectBounds(s map[string]any, zero any, hasZero, optional bool, out *[]UsageBounds) {
	if not, ok := s["not"].(map[string]any); ok && matchesConstraint(not, zero) {
		hasZero = false
	}
	if bounds, ok := s["x-config-duration-bounds"].(map[string]any); ok {
		start := len(*out)
		collectBounds(bounds, int64(0), hasZero, optional, out)
		for i := start; i < len(*out); i++ {
			(*out)[i].Target = "duration"
		}
	}
	if s["type"] == "integer" || s["type"] == "number" {
		if v, ok := s["const"]; ok {
			n := json.Number(fmt.Sprint(v))
			*out = append(*out, UsageBounds{Target: "value", Minimum: n, Maximum: n, ZeroAllowed: optional})
		}
	}
	for _, axis := range []struct{ target, minimum, maximum string }{
		{"value", "minimum", "maximum"}, {"length", "minLength", "maxLength"}, {"items", "minItems", "maxItems"}, {"properties", "minProperties", "maxProperties"},
	} {
		b := UsageBounds{Target: axis.target, ZeroAllowed: optional}
		if v, ok := s[axis.minimum]; ok {
			b.Minimum = json.Number(fmt.Sprint(v))
		}
		if v, ok := s[axis.maximum]; ok {
			b.Maximum = json.Number(fmt.Sprint(v))
		}
		if axis.target == "value" {
			if v, ok := s["exclusiveMinimum"]; ok && tighterBound(v, b.Minimum, true) {
				b.Minimum = json.Number(fmt.Sprint(v))
				b.ExclusiveMinimum = true
			}
			if v, ok := s["exclusiveMaximum"]; ok && tighterBound(v, b.Maximum, false) {
				b.Maximum = json.Number(fmt.Sprint(v))
				b.ExclusiveMaximum = true
			}
		}
		if b.Minimum != "" || b.Maximum != "" {
			*out = append(*out, b)
		}
	}
	if clauses, ok := s["allOf"].([]any); ok {
		for _, c := range clauses {
			collectBounds(c.(map[string]any), zero, hasZero, optional, out)
		}
	}
	if branches, ok := s["anyOf"].([]any); ok {
		allowsZero := hasZero && slices.ContainsFunc(branches, func(b any) bool { return matchesConstraint(b.(map[string]any), zero) })
		for _, b := range branches {
			collectBounds(b.(map[string]any), zero, hasZero, optional || allowsZero, out)
		}
	}
}

func tighterBound(value any, current json.Number, minimum bool) bool {
	if current == "" {
		return true
	}
	a, ok := constraintNumber(value)
	b, had := constraintNumber(current)
	if !ok || !had {
		return false
	}
	return minimum && a.Cmp(b) >= 0 || !minimum && a.Cmp(b) <= 0
}
