package patch

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sync"

	"github.com/ashtonian/pkg/parse/core"
)

// PatchPolicyConstraints holds policy restrictions for patch operations.
type PatchPolicyConstraints struct {
	ForbiddenPaths   []string
	ValueConstraints map[string]*ValueConstraint
	Validators       []PatchValidator
}

// ValueConstraint restricts values for a specific patch path.
type ValueConstraint struct {
	AllowedValues []any
	DeniedValues  []any
	Pattern       string
	Min           *float64
	Max           *float64

	compiledPattern *regexp.Regexp
	patternErr      error
	patternOnce     sync.Once
}

// NewValueConstraint creates a ValueConstraint and eagerly compiles the regex
// pattern (if set), returning an error for invalid patterns. Prefer this over
// direct struct construction to catch regex errors at startup.
func NewValueConstraint(pattern string, allowed, denied []any, min, max *float64) (*ValueConstraint, error) {
	vc := &ValueConstraint{
		AllowedValues: allowed,
		DeniedValues:  denied,
		Pattern:       pattern,
		Min:           min,
		Max:           max,
	}
	if pattern != "" {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid value constraint pattern %q: %w", pattern, err)
		}
		vc.compiledPattern = compiled
		vc.patternOnce.Do(func() {}) // mark as done so lazy path is a no-op
	}
	return vc, nil
}

// PatchValidator is a function that validates a patch operation against policy.
type PatchValidator func(op *PatchOperation, subject *core.PolicySubject) error

// ApplyPatchPolicyConstraints validates a ParsedPatch against policy constraints.
func ApplyPatchPolicyConstraints(patch *ParsedPatch, pc *PatchPolicyConstraints, subject *core.PolicySubject) error {
	if pc == nil {
		return nil
	}
	for i, op := range patch.Operations {
		if isForbiddenPath(op.Path, pc.ForbiddenPaths) {
			return &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyForbidden.String(),
				Message: fmt.Sprintf("path '%s' is forbidden by policy at index %d", op.Path, i),
				Path:    op.Path, Index: new(i),
			}
		}
		// From is checked for move AND copy: a copy with a
		// forbidden from exfiltrates the protected value into a readable field.
		if (op.Op == PatchOpMove || op.Op == PatchOpCopy) && op.From != "" {
			if isForbiddenPath(op.From, pc.ForbiddenPaths) {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyForbidden.String(),
					Message: fmt.Sprintf("%s source path '%s' is forbidden by policy at index %d", op.Op, op.From, i),
					Path:    op.From, Index: new(i),
				}
			}
		}
		if vc := matchValueConstraint(op.Path, pc.ValueConstraints); vc != nil {
			// move/copy carry Value == nil, so checkValueConstraint's value
			// inspection is a no-op — any value could be staged in an
			// unconstrained field and moved over a constrained one. Rejecting
			// outright is the only sound rule: the from-value cannot be
			// resolved at parse time (no target document). .
			if op.Op == PatchOpMove || op.Op == PatchOpCopy {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
					Message: fmt.Sprintf("path '%s' has value constraints; only add/replace are allowed (index %d)", op.Path, i),
					Path:    op.Path, Index: new(i),
				}
			}
			if err := checkValueConstraint(op, vc, i); err != nil {
				return err
			}
		}
		for _, v := range pc.Validators {
			if err := v(op, subject); err != nil {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
					Message: fmt.Sprintf("policy validator failed for path '%s' at index %d: %v", op.Path, i, err),
					Path:    op.Path, Index: new(i), Cause: err,
				}
			}
		}
	}
	return nil
}

func isForbiddenPath(path string, forbidden []string) bool {
	for _, pattern := range forbidden {
		if pattern == path || core.MatchWildcard(pattern, path) {
			return true
		}
	}
	return false
}

func matchValueConstraint(path string, constraints map[string]*ValueConstraint) *ValueConstraint {
	if constraints == nil {
		return nil
	}
	if vc, ok := constraints[path]; ok {
		return vc
	}
	// Deterministic wildcard resolution, most-specific first.
	// Constraint sets are small (a handful of patterns), so sorting per call
	// beats threading a memoization seam through consumer-built structs.
	for _, pattern := range sortPatternsBySpecificity(slices.Collect(maps.Keys(constraints))) {
		if core.MatchWildcard(pattern, path) {
			return constraints[pattern]
		}
	}
	return nil
}

func checkValueConstraint(op *PatchOperation, vc *ValueConstraint, index int) error {
	decimal := op.Schema != nil && op.Schema.DataType == core.FieldTypeDecimal
	if len(vc.AllowedValues) > 0 && !valueInList(op.Value, vc.AllowedValues, decimal) {
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
			Message: fmt.Sprintf("value for path '%s' is not in allowed values at index %d", op.Path, index),
			Path:    op.Path, Index: new(index),
		}
	}
	if len(vc.DeniedValues) > 0 && valueInList(op.Value, vc.DeniedValues, decimal) {
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
			Message: fmt.Sprintf("value for path '%s' is denied by policy at index %d", op.Path, index),
			Path:    op.Path, Index: new(index),
		}
	}
	if vc.Pattern != "" {
		if s, ok := op.Value.(string); ok {
			vc.patternOnce.Do(func() {
				vc.compiledPattern, vc.patternErr = regexp.Compile(vc.Pattern)
			})
			if vc.patternErr != nil {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
					Message: fmt.Sprintf("invalid regex pattern '%s' for path '%s': %v", vc.Pattern, op.Path, vc.patternErr),
					Path:    op.Path, Index: new(index), Cause: vc.patternErr,
				}
			}
			if !vc.compiledPattern.MatchString(s) {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
					Message: fmt.Sprintf("value for path '%s' does not match pattern '%s' at index %d", op.Path, vc.Pattern, index),
					Path:    op.Path, Index: new(index),
				}
			}
		}
	}
	if core.IsNumber(op.Value) || (decimal && op.Value != nil) {
		if vc.Min != nil {
			if c, ok := compareNumeric(op.Value, *vc.Min, decimal); ok && c < 0 {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
					Message: fmt.Sprintf("value for path '%s' is below minimum %v at index %d", op.Path, *vc.Min, index),
					Path:    op.Path, Index: new(index),
				}
			}
		}
		if vc.Max != nil {
			if c, ok := compareNumeric(op.Value, *vc.Max, decimal); ok && c > 0 {
				return &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
					Message: fmt.Sprintf("value for path '%s' exceeds maximum %v at index %d", op.Path, *vc.Max, index),
					Path:    op.Path, Index: new(index),
				}
			}
		}
	}
	return nil
}

// compareNumeric compares a patch value with a policy's number exactly:
// as decimals for a decimal path, whose values are canonical text.
func compareNumeric(value, bound any, decimal bool) (int, bool) {
	if decimal {
		return core.CompareDecimals(value, bound)
	}
	return core.CompareNumbers(value, bound)
}

// valueInList reports whether value is in list. Numbers compare by exact
// value, not Go type — a normalized int64 matches a policy's float64 or int
// literal, and a decimal path's value matches a list number or decimal
// string of equal value.
func valueInList(value any, list []any, decimal bool) bool {
	for _, v := range list {
		if c, ok := compareNumeric(value, v, decimal); ok {
			if c == 0 {
				return true
			}
			continue
		}
		if reflect.DeepEqual(value, v) {
			return true
		}
	}
	return false
}

// PatchPolicyFromWritableFields builds PatchPolicyConstraints with ForbiddenPaths
// for every path NOT in the writable map.
func PatchPolicyFromWritableFields(allPaths []string, writable map[string]bool) *PatchPolicyConstraints {
	var forbidden []string
	for _, p := range allPaths {
		if !writable[p] {
			forbidden = append(forbidden, p)
		}
	}
	return &PatchPolicyConstraints{ForbiddenPaths: forbidden}
}
