// Package parsehttp writes parse's request errors as HTTP responses
// , for servers that do not have an error envelope of their own.
package parsehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

// Body is the JSON a parse error is answered with.
type Body struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrUnencodableDetails reports Details entries of a parse error that cannot be
// encoded as JSON. WriteError answers with the entries that can.
var ErrUnencodableDetails = errors.New("parse error details cannot be encoded")

// Option configures WriteError.
type Option func(*options)

type options struct {
	onError func(error)
}

// WithErrorHandler reports what WriteError could not put in its response.
// Details entries that cannot be encoded are left out of the body and
// reported as an error matching ErrUnencodableDetails (errors.Is) that names
// each one and wraps its encoding error. A Write the ResponseWriter fails once the status is sent is reported
// as that error; net/http buffers a small response and sends it after the
// handler returns, so a client that disconnects in that window is not
// reported. Without it, both are dropped.
func WithErrorHandler(fn func(error)) Option {
	return func(o *options) { o.onError = fn }
}

// WriteError answers err when it is a parse error — anywhere in its chain —
// with the status its code declares and Body, and reports whether it did. Any
// other error is the caller's to answer.
func WriteError(w http.ResponseWriter, err error, opts ...Option) bool {
	var pe *core.ParseError
	if !errors.As(err, &pe) {
		return false
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	body, encErr := httpjson.Encode(Body{Code: pe.Code, Message: pe.Message, Details: pe.Details})
	if encErr != nil {
		// Code and Message are strings, so only Details can fail.
		details, dropped := encodableDetails(pe.Details)
		var keptErr error
		body, keptErr = httpjson.Encode(Body{Code: pe.Code, Message: pe.Message, Details: details})
		switch {
		case keptErr != nil:
			// Each entry encoded alone but not together: a MarshalJSON that
			// fails only sometimes. Answer without any.
			dropped = errors.Join(dropped, fmt.Errorf("every other entry: %w", keptErr))
			body = httpjson.ErrorBody(pe.Code, pe.Message)
		case dropped == nil:
			// Every entry encoded on the second try; the first failure is the cause.
			dropped = encErr
		}
		o.report(fmt.Errorf("answering %s: %w: %w", pe.Code, ErrUnencodableDetails, dropped))
	}
	if werr := httpjson.Send(w, pe.ErrorHTTPStatus(), body); werr != nil {
		o.report(fmt.Errorf("writing %s response: %w", pe.Code, werr))
	}
	return true
}

// encodableDetails returns the entries of details that encode, and an error
// naming each one that does not.
func encodableDetails(details map[string]any) (map[string]any, error) {
	kept := make(map[string]any, len(details))
	var dropped []error
	for _, key := range slices.Sorted(maps.Keys(details)) {
		if _, err := json.Marshal(details[key]); err != nil {
			dropped = append(dropped, fmt.Errorf("details[%q]: %w", key, err))
			continue
		}
		kept[key] = details[key]
	}
	return kept, errors.Join(dropped...)
}

func (o options) report(err error) {
	if o.onError != nil {
		o.onError(err)
	}
}
