package parsehttp

import (
	"fmt"
	"net/http"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

type resourceList struct {
	Resources []string `json:"resources"`
}

type schemaList struct {
	Schemas []*parse.SchemaResponse `json:"schemas"`
}

// SchemaHandler returns a handler that serves the schema document of the
// resource its {resource} path value names, 404/E2078 for a resource reg has
// not registered, or {"resources": [...]}, reg's names, on a route without
// {resource}.
func SchemaHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc {
	o := newOptions(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		resource := r.PathValue("resource")
		if resource == "" {
			o.serveSchema(w, r, http.StatusOK, resourceList{Resources: reg.ResourceNames()})
			return
		}
		doc := reg.SchemaFor(resource)
		if doc == nil {
			o.writeError(w, r, &parse.ParseError{
				Code:    parse.ECodeSchemaNotFound.String(),
				Message: "no schema registered for resource " + resource,
				Details: map[string]any{"resource": resource},
			})
			return
		}
		o.serveSchema(w, r, http.StatusOK, doc)
	}
}

// SchemaListHandler returns a handler that serves {"schemas": [...]}, the
// schema document of every resource reg has registered.
func SchemaListHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc {
	o := newOptions(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		names := reg.ResourceNames()
		schemas := make([]*parse.SchemaResponse, 0, len(names))
		for _, name := range names {
			if s := reg.SchemaFor(name); s != nil {
				schemas = append(schemas, s)
			}
		}
		o.serveSchema(w, r, http.StatusOK, schemaList{Schemas: schemas})
	}
}

// serveSchema answers doc, or 500/E2120 with the catalog message only when
// doc cannot be encoded: the encoding error names Go types, so it goes to the
// hook alone.
func (o options) serveSchema(w http.ResponseWriter, r *http.Request, status int, doc any) {
	body, err := httpjson.Encode(doc)
	if err != nil {
		code := parse.ECodeSchemaEncodingFailed
		o.report(r, fmt.Errorf("parsehttp: encoding schema document: %w: %w", code, err))
		status, body = code.ErrorHTTPStatus(), mustEncode(Body{Code: code.String(), Message: code.ErrorMessage()})
	}
	o.send(w, r, status, body)
}
