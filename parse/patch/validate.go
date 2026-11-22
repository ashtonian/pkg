package patch

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
)

// errNoSchema refuses a parse with no schema to validate against.
var errNoSchema = errors.New("patch: no schema to parse against")

// Validator judges a path's normalized value; nil passes. A declared
// constraint's failure is a *ConstraintError.
type Validator func(value any) error

// ConstraintError identifies a declared constraint and its schema limit: the
// cause of each E2077 an enum, bound or element count refuses. Enum limits
// are the comma-separated allowlist; bounds keep the schema's text.
type ConstraintError struct {
	// Constraint is the tag that declares it: core.ConstraintEnum, …
	Constraint string
	Limit      string
	detail     string
}

// NewConstraintError is a constraint's failure with detail, the
// value-specific diagnostic, as its text.
func NewConstraintError(constraint, limit, detail string) *ConstraintError {
	return &ConstraintError{Constraint: constraint, Limit: limit, detail: detail}
}

func (e *ConstraintError) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return e.Constraint + " constraint: " + e.Limit
}

// The Details keys of E2119, a relation operation naming more values than
// its path's limit (PatchSchema.MaxRelationValues).
const (
	DetailValueCount = "count"
	DetailValueLimit = "limit"
)

// stepFailure is why a valueChecker refuses an operation, before the parser
// that asked frames it with the operation's path and, for JSON Patch, its
// index: ECodeOpNotAllowed, or another code with its cause and details.
type stepFailure struct {
	code    core.Code
	cause   error
	details map[string]any
}

// valueChecker is what checks values against a schema's paths: the codec
// values decode with and the schema's relation value limit.
type valueChecker struct {
	codec             core.Codec
	maxRelationValues int
}

func newValueChecker(schema *PatchSchema, codec core.Codec) valueChecker {
	return valueChecker{codec: codec, maxRelationValues: schema.MaxRelationValues}
}

// check is the check both wire formats run on one operation's value: the
// operation is allowed on the path, a clear is nullable, the value
// normalizes (a relation's keys once each, within the limit), its validators
// pass, and an array set whole holds the path's element counts. value is what the operation writes, at a path depth tokens
// deep. It returns the normalized value, or the failures: the first of the
// first three checks, else every validator's and the count's, in order.
//
// merge marks an RFC 7396 member: an object merges into the document rather
// than replacing it, and a null reports that the field is not nullable
// before that its remove is not allowed.
func (c valueChecker) check(ps *PathSchema, op PatchOp, value any, depth int, merge bool) (any, []stepFailure) {
	invalid := func(err error) []stepFailure { return []stepFailure{{code: ECodePatchValueInvalid, cause: err}} }
	notAllowed := []stepFailure{{code: ECodeOpNotAllowed}}
	if merge {
		if err := checkNullability(ps, op, value, depth); err != nil {
			return nil, invalid(err)
		}
		if !ps.AllowsOp(op) {
			return nil, notAllowed
		}
	} else {
		if !ps.AllowsOp(op) {
			return nil, notAllowed
		}
		if err := checkNullability(ps, op, value, depth); err != nil {
			return nil, invalid(err)
		}
	}

	var failures []stepFailure
	relation := ps.Relation != nil
	switch {
	case op == PatchOpRemove && !relation:
		failures = validate(ps, nil)
	case opTakesValue(op) || relation && op == PatchOpRemove:
		// A relation's remove names members, normalized like the other
		// membership verbs'.
		var (
			normalized any
			err        error
		)
		if merge {
			normalized, err = normalizeMergeValue(ps, op, value, c.codec)
		} else {
			normalized, err = normalizeOpValue(ps, op, value, depth, c.codec)
		}
		if err != nil {
			return nil, invalid(err)
		}
		if relation {
			var tooMany *stepFailure
			if normalized, tooMany = c.relationValues(normalized); tooMany != nil {
				return nil, []stepFailure{*tooMany}
			}
		}
		checked := normalized
		if op == PatchOpTest {
			// A test keeps the document it compares; validators judge the
			// value as a replace normalizes it.
			if checked, err = normalizeOpValue(ps, PatchOpReplace, value, depth, c.codec); err != nil {
				return nil, invalid(err)
			}
		}
		value = normalized
		failures = validate(ps, checked)
	}
	if err := checkItems(ps, op, value, depth); err != nil {
		failures = append(failures, splitFailures(err)...)
	}
	if len(failures) > 0 {
		return nil, failures
	}
	return value, nil
}

// relationValues are a relation operation's keys once each, in the order
// first named, refused with E2119 beyond the limit: every key is a pair
// written per owner, so the count bounds the statements' rows.
func (c valueChecker) relationValues(value any) (any, *stepFailure) {
	items, ok := value.([]any)
	if !ok {
		return value, nil
	}
	seen := make(map[any]struct{}, len(items))
	unique := make([]any, 0, len(items))
	for _, v := range items {
		if v != nil && reflect.TypeOf(v).Comparable() {
			if _, repeated := seen[v]; repeated {
				continue
			}
			seen[v] = struct{}{}
		}
		unique = append(unique, v)
	}
	if c.maxRelationValues > 0 && len(unique) > c.maxRelationValues {
		return nil, &stepFailure{
			code:    ECodeRelationPatchTooLarge,
			cause:   fmt.Errorf("%d values exceed the maximum %d", len(unique), c.maxRelationValues),
			details: map[string]any{DetailValueCount: len(unique), DetailValueLimit: c.maxRelationValues},
		}
	}
	return unique, nil
}

// validate runs the path's validators on value.
func validate(ps *PathSchema, value any) []stepFailure {
	var failures []stepFailure
	for _, validator := range ps.Validators {
		if err := validator(value); err != nil {
			failures = append(failures, splitFailures(err)...)
		}
	}
	return failures
}

// splitFailures keeps each invalid value its own failure, even when a
// validator checked several array elements or several declared constraints
// and joined them.
func splitFailures(cause error) []stepFailure {
	if joined, ok := cause.(interface{ Unwrap() []error }); ok {
		var failures []stepFailure
		for _, child := range joined.Unwrap() {
			failures = append(failures, splitFailures(child)...)
		}
		return failures
	}
	return []stepFailure{{code: ECodePatchValueInvalid, cause: cause}}
}
