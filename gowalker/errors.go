package gowalker

import (
	"errors"
	"reflect"

	"github.com/ashtonian/pkg/gowalker/path"
)

var (
	// ErrSchema identifies invalid field rules, ambiguous names, or invalid engine settings.
	ErrSchema = errors.New("invalid schema")
	// ErrPath identifies an invalid path or a step that cannot select a location.
	ErrPath = errors.New("invalid path")
	// ErrType identifies an incompatible root, input or requested result type.
	ErrType = errors.New("incompatible type")
	// ErrMissing identifies an absent location required by a typed read or write.
	ErrMissing = errors.New("missing value")
	// ErrNil identifies a nil intermediate that a write cannot traverse or allocate.
	ErrNil = errors.New("nil intermediate")
	// ErrAccess identifies a value that safe reflection cannot read or write.
	ErrAccess = errors.New("inaccessible value")
	// ErrIndex identifies an index outside an existing slice or array.
	ErrIndex = errors.New("index out of bounds")
	// ErrKey identifies an incompatible, non-comparable or non-reflexive map key.
	ErrKey = errors.New("unsupported map key")
	// ErrLimit identifies an exceeded compilation, access or walk budget.
	ErrLimit = errors.New("limit exceeded")
	// ErrConversion identifies a numeric conversion that is unsupported or loses information.
	ErrConversion = errors.New("lossy or unsupported conversion")
	// ErrCallback identifies an error returned by a walk visitor.
	ErrCallback = errors.New("callback failed")
)

// Error describes a failed operation. Path, Type and Cause are available for
// caller-controlled diagnostics; Error deliberately omits names, keys and values.
// Step is the zero-based path step that failed, or -1 for a root/schema error.
// TypeRoute identifies a schema location for type walks, without inventing a
// live map key or collection index.
type Error struct {
	Op        string
	Kind      error
	Path      path.Path
	Step      int
	Type      reflect.Type
	Cause     error
	TypeRoute []TypeEdge
}

// NameConflict provides structured schema diagnostics without exposing names in
// default error text. A consumer can distinguish canonical/alias conflicts using
// the two field handles and apply its own diagnostic or redaction policy.
type NameConflict struct {
	Owner         reflect.Type
	Name          string
	First, Second *Field
}

func (e *NameConflict) Error() string { return "ambiguous field name" }

func (e *Error) Error() string        { return "gowalker: " + e.Op + ": " + e.Kind.Error() }
func (e *Error) Unwrap() error        { return e.Cause }
func (e *Error) Is(target error) bool { return target == e.Kind }

func failure(op string, kind error, t reflect.Type, p path.Path, step int, cause error) error {
	if nested, ok := kind.(*Error); ok {
		if cause == nil {
			cause = nested
		}
		kind = nested.Kind
	}
	return &Error{Op: op, Kind: kind, Type: t, Path: p, Step: step, Cause: cause}
}
