// Package parsehttp writes parse's request errors as HTTP responses
// , for servers that do not have an error envelope of their own.
package parsehttp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ashtonian/pkg/parse/core"
)

// Body is the JSON a parse error is answered with.
type Body struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// WriteError answers err when it is a parse error — anywhere in its chain —
// with the status its code declares and Body, and reports whether it did. Any
// other error is the caller's to answer.
func WriteError(w http.ResponseWriter, err error) bool {
	var pe *core.ParseError
	if !errors.As(err, &pe) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(pe.ErrorHTTPStatus())
	// The status line is written; an encode failure can only truncate.
	_ = json.NewEncoder(w).Encode(Body{Code: pe.Code, Message: pe.Message, Details: pe.Details})
	return true
}
