package parse

import (
	"fmt"
	"net/http"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

// SchemaHandlerOption configures SchemaHandler and SchemaListHandler.
type SchemaHandlerOption func(*schemaHandler)

// WithErrorHandler reports the failures a schema handler cannot put in its
// response. A document that cannot be encoded is answered 500/E2120 and
// reported as an error matching ECodeSchemaEncodingFailed (errors.Is) that
// wraps the encoding error. A Write the ResponseWriter fails once the status
// is sent is reported as that error. net/http buffers a small response and
// sends it after the handler returns, so a client that disconnects in that
// window is not reported. fn runs on the request's goroutine, so it must be
// safe for concurrent use. Without it, both are dropped.
func WithErrorHandler(fn func(*http.Request, error)) SchemaHandlerOption {
	return func(h *schemaHandler) { h.onError = fn }
}

type schemaHandler struct {
	onError func(*http.Request, error)
}

func newSchemaHandler(opts []SchemaHandlerOption) *schemaHandler {
	h := &schemaHandler{}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// errorBody is the HTTP error body ({code, message, details}), as
// parsehttp.Body writes it and the TS SDK's ApiError reads it.
type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type resourceList struct {
	Resources []string `json:"resources"`
}

type schemaList struct {
	Schemas []*SchemaResponse `json:"schemas"`
}

// SchemaHandler returns an http.HandlerFunc that serves the schema document
// of the resource its {resource} path value names, 404/E2078 for a resource
// not registered, or the registered names when the route has no {resource}.
func (r *SchemaRegistry) SchemaHandler(opts ...SchemaHandlerOption) http.HandlerFunc {
	h := newSchemaHandler(opts)
	return func(w http.ResponseWriter, req *http.Request) {
		resource := req.PathValue("resource")
		if resource == "" {
			h.respond(w, req, http.StatusOK, resourceList{Resources: r.ResourceNames()})
			return
		}
		doc := r.SchemaFor(resource)
		if doc == nil {
			h.respond(w, req, http.StatusNotFound, errorBody{
				Code:    filter.ECodeSchemaNotFound.String(),
				Message: "no schema registered for resource " + resource,
				Details: map[string]string{"resource": resource},
			})
			return
		}
		h.respond(w, req, http.StatusOK, doc)
	}
}

// SchemaListHandler returns an http.HandlerFunc that serves every registered
// resource's schema document.
func (r *SchemaRegistry) SchemaListHandler(opts ...SchemaHandlerOption) http.HandlerFunc {
	h := newSchemaHandler(opts)
	return func(w http.ResponseWriter, req *http.Request) {
		names := r.ResourceNames()
		schemas := make([]*SchemaResponse, 0, len(names))
		for _, name := range names {
			if s := r.SchemaFor(name); s != nil {
				schemas = append(schemas, s)
			}
		}
		h.respond(w, req, http.StatusOK, schemaList{Schemas: schemas})
	}
}

// respond encodes doc before writing anything, so a document that cannot be
// encoded can still be answered with a status of its own.
func (h *schemaHandler) respond(w http.ResponseWriter, req *http.Request, status int, doc any) {
	body, err := httpjson.Encode(doc)
	if err != nil {
		code := ECodeSchemaEncodingFailed
		h.report(req, fmt.Errorf("%w: %w", code, err))
		// The cause names Go types; the client gets the catalog message only.
		status, body = code.ErrorHTTPStatus(), httpjson.ErrorBody(code.String(), code.ErrorMessage())
	}
	if err := httpjson.Send(w, status, body); err != nil {
		h.report(req, fmt.Errorf("writing schema response: %w", err))
	}
}

func (h *schemaHandler) report(req *http.Request, err error) {
	if h.onError != nil {
		h.onError(req, err)
	}
}
