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

// ApplyPatchPolicyConstraints judges a ParsedPatch against policy by what
// each operation writes and reads, not by its envelope: a whole value writes
// every path below its own, a merge object the members it names, a remove
// null and a move its source. On success the policy is recorded on patch
// (AuditInfo).
func ApplyPatchPolicyConstraints(patch *ParsedPatch, pc *PatchPolicyConstraints, subject *core.PolicySubject) error {
	if pc == nil {
		return nil
	}
	j := policyJudge{pc: pc, patterns: sortPatternsBySpecificity(slices.Collect(maps.Keys(pc.ValueConstraints)))}
	for i, op := range patch.Operations {
		if err := j.checkForbidden(op, i); err != nil {
			return err
		}
		for _, w := range op.writes() {
			if err := j.checkValues(w, i); err != nil {
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
	patch.policy = pc
	return nil
}

// policyJudge applies one policy to one patch.
type policyJudge struct {
	pc *PatchPolicyConstraints
	// patterns are the value constraints' keys, most specific first.
	patterns []string
}

// checkForbidden refuses an operation that writes or reads a forbidden path:
// one a pattern matches, or one below a path it writes or reads whole.
func (j policyJudge) checkForbidden(op *PatchOperation, index int) error {
	for _, w := range op.writes() {
		if err := j.forbiddenWrite(op, w, index); err != nil {
			return err
		}
	}
	for _, path := range op.reads() {
		if err := j.forbidden(op, path, true, index); err != nil {
			return err
		}
	}
	return nil
}

func (j policyJudge) forbiddenWrite(op *PatchOperation, w write, index int) error {
	if err := j.forbidden(op, w.path, w.kind != writeMerge, index); err != nil {
		return err
	}
	if w.kind == writeMerge {
		for _, member := range w.members() {
			if err := j.forbiddenWrite(op, member, index); err != nil {
				return err
			}
		}
	}
	return nil
}

// forbidden refuses path when a pattern matches it or, for a path written or
// read whole, a path below it.
func (j policyJudge) forbidden(op *PatchOperation, path string, whole bool, index int) error {
	tokens := pointerTokens(path)
	for _, pattern := range j.pc.ForbiddenPaths {
		exact := pattern == path || core.MatchWildcard(pattern, path)
		if !exact && !(whole && matchesBelow(pointerTokens(pattern), tokens)) {
			continue
		}
		var msg string
		switch {
		case path == op.From && exact:
			msg = fmt.Sprintf("%s source path '%s' is forbidden by policy at index %d", op.Op, path, index)
		case path == op.From:
			msg = fmt.Sprintf("%s source path '%s' covers '%s', which policy forbids, at index %d", op.Op, path, pattern, index)
		case exact:
			msg = fmt.Sprintf("path '%s' is forbidden by policy at index %d", path, index)
		default:
			msg = fmt.Sprintf("path '%s' covers '%s', which policy forbids, at index %d", path, pattern, index)
		}
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyForbidden.String(),
			Message: msg, Path: path, Index: new(index),
		}
	}
	return nil
}

// checkValues holds what w writes to the value constraints: the one each
// written path selects judges the value there. Below a whole write an absent
// member reads as null; below a merge it is untouched.
func (j policyJudge) checkValues(w write, index int) error {
	switch w.kind {
	case writeCopy:
		// The value is read from the document when the patch applies, so no
		// constraint at or below the destination can judge it here: anything
		// could be staged in an unconstrained field and moved over this one.
		if j.selectConstraint(w.path) != nil || j.constrainedBelow(w.path) {
			return &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
				Message: fmt.Sprintf("path '%s' has value constraints; only add/replace are allowed (index %d)", w.path, index),
				Path:    w.path, Index: new(index),
			}
		}
		return nil
	case writeMerge:
		if j.selectConstraint(w.path) != nil {
			return &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
				Message: fmt.Sprintf("path '%s' has value constraints, which a merge into it cannot be checked against; replace it whole (index %d)", w.path, index),
				Path:    w.path, Index: new(index),
			}
		}
		for _, member := range w.members() {
			if err := j.checkValues(member, index); err != nil {
				return err
			}
		}
		return nil
	case writeElements:
		items, _ := w.value.([]any)
		if vc := j.selectConstraint(w.path); vc != nil {
			for _, item := range items {
				if err := vc.check(w.path, item, isDecimal(w.schema), index); err != nil {
					return err
				}
			}
		}
		// A new element's own path is the append token.
		for _, item := range items {
			if err := j.checkWhole(w.path+"/-", item, nil, index); err != nil {
				return err
			}
		}
		return nil
	}
	return j.checkWhole(w.path, w.value, w.schema, index)
}

// checkWhole judges value written whole at path, and every path below it.
func (j policyJudge) checkWhole(path string, value any, schema *PathSchema, index int) error {
	if vc := j.selectConstraint(path); vc != nil {
		decimal := isDecimal(schema)
		if schema != nil && schema.DataType == core.FieldTypeArray {
			// An array path's constraint judges each element, as its
			// validators do.
			items, _ := core.AnySlice(value)
			for _, item := range items {
				if err := vc.check(path, item, decimal, index); err != nil {
					return err
				}
			}
		} else if err := vc.check(path, value, decimal, index); err != nil {
			return err
		}
	}
	if len(j.patterns) == 0 {
		return nil
	}
	checked := map[string]bool{}
	for _, member := range presentPaths(path, value) {
		checked[member.path] = true
		if vc := j.selectConstraint(member.path); vc != nil {
			if err := vc.check(member.path, member.value, false, index); err != nil {
				return err
			}
		}
	}
	base := pointerTokens(path)
	for _, pattern := range j.patterns {
		for _, absent := range absentPaths(pointerTokens(pattern), base, value) {
			if checked[absent] {
				continue
			}
			checked[absent] = true
			if vc := j.selectConstraint(absent); vc != nil {
				if err := vc.check(absent, nil, false, index); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// selectConstraint is the constraint that judges path: its exact key, else
// the most specific pattern that matches it.
func (j policyJudge) selectConstraint(path string) *ValueConstraint {
	if vc, ok := j.pc.ValueConstraints[path]; ok {
		return vc
	}
	for _, pattern := range j.patterns {
		if core.MatchWildcard(pattern, path) {
			return j.pc.ValueConstraints[pattern]
		}
	}
	return nil
}

// constrainedBelow reports a value constraint that can match a path below path.
func (j policyJudge) constrainedBelow(path string) bool {
	tokens := pointerTokens(path)
	for _, pattern := range j.patterns {
		if matchesBelow(pointerTokens(pattern), tokens) {
			return true
		}
	}
	return false
}

func isDecimal(schema *PathSchema) bool {
	return schema != nil && schema.DataType == core.FieldTypeDecimal
}

// check judges one value written at path. Null is judged by AllowedValues and
// DeniedValues; a pattern judges strings and the bounds numbers, and any
// other value at a path they constrain is refused rather than passed.
func (vc *ValueConstraint) check(path string, value any, decimal bool, index int) error {
	refuse := func(format string, args ...any) error {
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
			Message: fmt.Sprintf(format, args...), Path: path, Index: new(index),
		}
	}
	if len(vc.AllowedValues) > 0 && !valueInList(value, vc.AllowedValues, decimal) {
		return refuse("value for path '%s' is not in allowed values at index %d", path, index)
	}
	if len(vc.DeniedValues) > 0 && valueInList(value, vc.DeniedValues, decimal) {
		return refuse("value for path '%s' is denied by policy at index %d", path, index)
	}
	if value == nil {
		return nil
	}
	if vc.Pattern != "" {
		vc.patternOnce.Do(func() {
			if vc.compiledPattern == nil {
				vc.compiledPattern, vc.patternErr = regexp.Compile(vc.Pattern)
			}
		})
		if vc.patternErr != nil {
			return &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchPolicyValue.String(),
				Message: fmt.Sprintf("invalid regex pattern '%s' for path '%s': %v", vc.Pattern, path, vc.patternErr),
				Path:    path, Index: new(index), Cause: vc.patternErr,
			}
		}
		s, ok := value.(string)
		if !ok {
			return refuse("value for path '%s' is not a string, which pattern '%s' requires, at index %d", path, vc.Pattern, index)
		}
		if !vc.compiledPattern.MatchString(s) {
			return refuse("value for path '%s' does not match pattern '%s' at index %d", path, vc.Pattern, index)
		}
	}
	if vc.Min == nil && vc.Max == nil {
		return nil
	}
	if !core.IsNumber(value) && !decimal {
		return refuse("value for path '%s' is not a number, which its bounds require, at index %d", path, index)
	}
	if vc.Min != nil {
		if c, ok := compareNumeric(value, *vc.Min, decimal); !ok || c < 0 {
			return refuse("value for path '%s' is below minimum %v at index %d", path, *vc.Min, index)
		}
	}
	if vc.Max != nil {
		if c, ok := compareNumeric(value, *vc.Max, decimal); !ok || c > 0 {
			return refuse("value for path '%s' exceeds maximum %v at index %d", path, *vc.Max, index)
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
