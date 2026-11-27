package core

import "fmt"

// The Details keys a ParseError carries: DetailField names the request
// field a refusal is about, DetailParameter the list parameter.
const (
	DetailField     = "field"
	DetailParameter = "parameter"
)

// ParseError is the error a request is refused with. Build one with
// NewError. Configuration errors are plain errors (see the package
// documentation).
type ParseError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Path    string         `json:"path,omitempty"`
	Index   *int           `json:"index,omitempty"`   // pointer so op index 0 survives the wire
	Details map[string]any `json:"details,omitempty"` // keyed by the Detail constants
	Cause   error          `json:"-"`
}

// ErrorOption sets what a ParseError carries beside its code and message.
type ErrorOption func(*ParseError)

// NewError is the ParseError refusing a request with code and message, and
// what opts set: WithPath, WithIndex, WithField, WithParameter, WithDetail
// and WithCause.
func NewError(code Code, message string, opts ...ErrorOption) *ParseError {
	e := &ParseError{Code: string(code), Message: message}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithPath sets Path, the JSON Pointer of the patch member the error is
// about, and Details[DetailField] to the resource field its first token
// names, so the two cannot disagree. A pointer that does not parse, or
// whose first token is not valid text (CheckText), names no field; neither
// does the whole document.
func WithPath(pointer string) ErrorOption {
	return func(e *ParseError) {
		e.Path = pointer
		tokens, err := ParsePointer(pointer)
		if err != nil || len(tokens) == 0 || CheckText(tokens[0]) != nil {
			return
		}
		e.setDetail(DetailField, tokens[0])
	}
}

// WithIndex sets Index, the position of the JSON Patch operation the error
// is about.
func WithIndex(index int) ErrorOption {
	return func(e *ParseError) { e.Index = &index }
}

// WithField sets Details[DetailField], the request field the error is
// about, as the request named it.
func WithField(name string) ErrorOption { return WithDetail(DetailField, name) }

// WithParameter sets Details[DetailParameter], the list parameter the error
// is about.
func WithParameter(name string) ErrorOption { return WithDetail(DetailParameter, name) }

// WithDetail sets Details[key].
func WithDetail(key string, value any) ErrorOption {
	return func(e *ParseError) { e.setDetail(key, value) }
}

// WithCause sets Cause, the error Unwrap returns.
func WithCause(err error) ErrorOption {
	return func(e *ParseError) { e.Cause = err }
}

// setDetail sets Details[key], allocating the map for the first entry so an
// error without details carries none.
func (e *ParseError) setDetail(key string, value any) {
	if e.Details == nil {
		e.Details = make(map[string]any, 1)
	}
	e.Details[key] = value
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
