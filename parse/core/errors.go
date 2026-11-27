package core

import "fmt"

// ParseErrorKind identifies the subsystem that produced the error.
type ParseErrorKind string

const (
	ParseErrorKindFilter ParseErrorKind = "filter"
	ParseErrorKindPatch  ParseErrorKind = "patch"
	ParseErrorKindSort   ParseErrorKind = "sort"
	ParseErrorKindSchema ParseErrorKind = "schema"
)

// The Details keys a ParseError carries: DetailField names the request
// field a refusal is about, DetailParameter the list parameter.
const (
	DetailField     = "field"
	DetailParameter = "parameter"
)

// ParseError is the error a request is refused with, its Kind naming the
// subsystem that refused it. Configuration errors are plain errors (see the
// package documentation).
type ParseError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Kind    ParseErrorKind `json:"-"` // the code names it; not on the wire
	Path    string         `json:"path,omitempty"`
	Index   *int           `json:"index,omitempty"`   // pointer so op index 0 survives the wire
	Details map[string]any `json:"details,omitempty"` // keyed by the Detail constants
	Cause   error          `json:"-"`
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause for errors.Is/As compatibility.
func (e *ParseError) Unwrap() error {
	return e.Cause
}

// Is reports whether target carries the same E-code — any error with an
// ErrorCode() string method, including Code — so a ParseError matches its
// code's sentinel across wrap chains. An empty code matches nothing; Cause
// stays reachable through Unwrap.
func (e *ParseError) Is(target error) bool {
	c, ok := target.(interface{ ErrorCode() string })
	return ok && e.Code != "" && c.ErrorCode() == e.Code
}

// ErrorCode returns the E-code string for middleware introspection.
func (e *ParseError) ErrorCode() string { return e.Code }

// ErrorMessage returns the human-readable message.
func (e *ParseError) ErrorMessage() string { return e.Message }

// ErrorHTTPStatus returns the code's status, or 422 for unknown codes.
func (e *ParseError) ErrorHTTPStatus() int { return Code(e.Code).ErrorHTTPStatus() }

// IsErrorUserFacing reports whether the message is safe to show the caller,
// as its code's catalog row says: parse raises a ParseError only for what
// the request did, so its message describes the request, never the
// database. A code outside the catalog vouches for nothing.
func (e *ParseError) IsErrorUserFacing() bool { return Code(e.Code).IsErrorUserFacing() }
