package parsehttp

import (
	"fmt"
	"net/http"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ParseList reads a list request from either of its transports: the query
// string of a GET or HEAD (Resource.ParseList), or the JSON body of a POST
// (Resource.ParseListPost), for a filter too large for a URL. A POST's
// query string carries only the parameters the endpoint reads itself
// (parse.AcceptParams), so one request cannot carry two filters; any other
// parameter, or a body that is not application/json, is E2067. Another
// method is E2067 too: route only GET, HEAD and POST here.
func ParseList[T any](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return res.ParseList(r.URL.Query())
	case http.MethodPost:
		return res.ParseListPost(r.URL.Query(), r.Header.Get("Content-Type"), r.Body)
	}
	return nil, &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: filter.ECodeInvalidListParam.String(),
		Message: fmt.Sprintf("a list is read with GET, HEAD or POST, not %s", r.Method),
		Details: map[string]any{core.DetailParameter: "method"},
	}
}
