// Package parsehttp is parse's net/http adapter. It reads list requests from
// either transport (ParseList), serves the schema discovery routes
// (SchemaHandler, SchemaListHandler), and answers parse's request errors
// (WriteError) and a handler's own JSON (WriteJSON), each encoded before
// anything is written and sent with its Content-Length. One WithErrorHandler
// hook receives what any of them could not put in a response.
package parsehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

// Body is the JSON a parse error is answered with, as the TypeScript SDK's
// ErrorBody reads it. A *core.ParseError marshals to the same members.
type Body struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Path is the JSON Pointer of the patch member a patch error is about.
	Path string `json:"path,omitempty"`
	// Index is the position of the JSON Patch operation a patch error is
	// about; a pointer, so operation 0 is written.
	Index   *int           `json:"index,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrUnencodableDetails reports Details entries of a parse error that cannot be
// encoded as JSON. WriteError answers with the entries that can.
var ErrUnencodableDetails = errors.New("parse error details cannot be encoded")

// Option configures WriteError, WriteJSON and the schema handlers.
type Option func(*options)

type options struct {
	onError func(*http.Request, error)
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithErrorHandler reports what a response could not carry, with the request
// it answered:
//   - Details entries of a parse error that cannot be encoded, left out of
//     WriteError's body, as an error matching ErrUnencodableDetails
//     (errors.Is) that names each one and wraps its encoding error;
//   - a schema document that cannot be encoded, answered 500/E2120, as an
//     error matching parse.ECodeSchemaEncodingFailed that wraps the encoding
//     error;
//   - a Write the ResponseWriter fails once the status is sent, as that
//     error. net/http buffers a small response and sends it after the handler
//     returns, so a client that disconnects in that window is not reported.
//
// fn runs on the request's goroutine, so it must be safe for concurrent use.
// Without it, all of these are dropped; the responses are the same.
func WithErrorHandler(fn func(*http.Request, error)) Option {
	return func(o *options) { o.onError = fn }
}

func (o options) report(r *http.Request, err error) {
	if o.onError != nil {
		o.onError(r, err)
	}
}

// WriteError answers err when it is a parse error — anywhere in its chain —
// with the status its code declares and Body, and reports whether it did. Any
// other error is the caller's to answer.
func WriteError(w http.ResponseWriter, r *http.Request, err error, opts ...Option) bool {
	pe, ok := errors.AsType[*core.ParseError](err)
	if !ok {
		return false
	}
	newOptions(opts).writeError(w, r, pe)
	return true
}

// writeError answers pe with the status its code declares and Body.
func (o options) writeError(w http.ResponseWriter, r *http.Request, pe *core.ParseError) {
	answer := Body{Code: pe.Code, Message: pe.Message, Path: pe.Path, Index: pe.Index, Details: pe.Details}
	body, encErr := httpjson.Encode(answer)
	if encErr != nil {
		// Code, Message, Path and Index always encode, so only Details can fail.
		details, dropped := encodableDetails(pe.Details)
		answer.Details = details
		var keptErr error
		body, keptErr = httpjson.Encode(answer)
		switch {
		case keptErr != nil:
			// Each entry encoded alone but not together: a MarshalJSON that
			// fails only sometimes. Answer without any.
			dropped = errors.Join(dropped, fmt.Errorf("every other entry: %w", keptErr))
			answer.Details = nil
			body = mustEncode(answer)
		case dropped == nil:
			// Every entry encoded on the second try; the first failure is the cause.
			dropped = encErr
		}
		o.report(r, fmt.Errorf("parsehttp: answering %s: %w: %w", pe.Code, ErrUnencodableDetails, dropped))
	}
	o.send(w, r, pe.ErrorHTTPStatus(), body)
}

// WriteJSON answers v as an application/json response with status: encoded
// before anything is written, then sent with its Content-Length. A v that
// cannot be encoded is returned with nothing written, for the caller to
// answer as its own failure; a Write that fails once the status is sent goes
// to the WithErrorHandler hook.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any, opts ...Option) error {
	body, err := httpjson.Encode(v)
	if err != nil {
		return fmt.Errorf("parsehttp: encoding %T: %w", v, err)
	}
	newOptions(opts).send(w, r, status, body)
	return nil
}

// send writes body as an application/json response with status and its
// Content-Length, and reports a Write that fails once the status is sent.
// net/http buffers a small response and sends it after the handler returns,
// so a client that disconnects in that window fails no Write.
func (o options) send(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	w.Header().Set("Content-Type", httpjson.MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		o.report(r, fmt.Errorf("parsehttp: writing %d response: %w", status, err))
	}
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

// mustEncode encodes a Body without Details, which holds only strings and an
// int and always encodes.
func mustEncode(b Body) []byte {
	body, err := httpjson.Encode(b)
	if err != nil {
		panic("parsehttp: encoding an error body: " + err.Error())
	}
	return body
}
