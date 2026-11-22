// Package parsehttp writes parse's request errors as HTTP responses
// , for servers that do not have an error envelope of their own.
package parsehttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

// Body is the JSON a parse error is answered with.
type Body struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrUnencodableDetails reports a parse error whose Details cannot be
// encoded. The answer keeps its code and status and carries no details; the
// hook receives this sentinel wrapping the encoding error, as errors.Is sees
// it.
var ErrUnencodableDetails = errors.New("parsehttp: details cannot be encoded")

// Option configures WriteError.
type Option func(*errorWriter)

// WithErrorHandler reports the failures WriteError cannot put in its
// response: details that do not encode, and a Write the ResponseWriter fails
// once the status is sent. One answer can report twice — the details that
// were dropped, then a fallback body that could not be written — so fn must
// expect more than one call per request. It runs on the request's goroutine,
// so it must be safe for concurrent use. Without it, both are dropped.
func WithErrorHandler(fn func(error)) Option {
	return func(h *errorWriter) { h.onError = fn }
}

type errorWriter struct {
	onError func(error)
}

func newErrorWriter(opts []Option) *errorWriter {
	h := &errorWriter{}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// WriteError answers err when it is a parse error — anywhere in its chain —
// with the status its code declares and Body, and reports whether it did. Any
// other error is the caller's to answer.
//
// Body is encoded before anything is written, so details that do not encode
// are answered with the code's status and message alone instead of a status
// with no body. A Write that fails after the status is sent is reported, not
// answered: the response is already gone.
func WriteError(w http.ResponseWriter, err error, opts ...Option) bool {
	var pe *core.ParseError
	if !errors.As(err, &pe) {
		return false
	}
	newErrorWriter(opts).write(w, pe)
	return true
}

func (h *errorWriter) write(w http.ResponseWriter, pe *core.ParseError) {
	status := pe.ErrorHTTPStatus()
	body, err := httpjson.Encode(Body{Code: pe.Code, Message: pe.Message, Details: pe.Details})
	if err != nil {
		h.report(fmt.Errorf("%w: %w", ErrUnencodableDetails, err))
		// The cause names Go values; the client gets the code's message only.
		body, err = httpjson.Encode(Body{Code: pe.Code, Message: pe.Message})
		if err != nil {
			h.report(fmt.Errorf("encoding parse error body: %w", err))
			w.WriteHeader(status)
			return
		}
	}
	if err := httpjson.Send(w, status, body); err != nil {
		h.report(fmt.Errorf("writing parse error response: %w", err))
	}
}

func (h *errorWriter) report(err error) {
	if h.onError != nil {
		h.onError(err)
	}
}
