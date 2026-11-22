package patch

import (
	"errors"

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

// stepFailure is why checkValue refuses an operation, before the parser that
// asked frames it with the operation's path and, for JSON Patch, its index:
// ECodeOpNotAllowed, or ECodePatchValueInvalid and its cause.
type stepFailure struct {
	code  core.Code
	cause error
}

// checkValue is the check both wire formats run on one operation's value:
// the operation is allowed on the path, a clear is nullable, the value
// normalizes, its validators pass, and an array set whole holds the path's
// element counts. value is what the operation writes, at a path depth tokens
// deep. It returns the normalized value, or the failures: the first of the
// first three checks, else every validator's and the count's, in order.
//
// merge marks an RFC 7396 member: an object merges into the document rather
// than replacing it, and a null reports that the field is not nullable
// before that its remove is not allowed.
func checkValue(ps *PathSchema, op PatchOp, value any, depth int, merge bool, codec core.Codec) (any, []stepFailure) {
	invalid := func(err error) []stepFailure { return []stepFailure{{ECodePatchValueInvalid, err}} }
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
			normalized, err = normalizeMergeValue(ps, op, value, codec)
		} else {
			normalized, err = normalizeOpValue(ps, op, value, depth, codec)
		}
		if err != nil {
			return nil, invalid(err)
		}
		checked := normalized
		if op == PatchOpTest {
			// A test keeps the document it compares; validators judge the
			// value as a replace normalizes it.
			if checked, err = normalizeOpValue(ps, PatchOpReplace, value, depth, codec); err != nil {
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
	return []stepFailure{{ECodePatchValueInvalid, cause}}
}
