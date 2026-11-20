package parsehttp

import (
	"fmt"
	"maps"
	"mime"
	"net/http"
	"slices"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ParseList reads a list request from either of its transports: the query
// string of a GET or HEAD (Resource.ParseList), or the JSON body of a POST
// (Resource.ParseListBody), for a filter too large for a URL. A POST's
// query string carries only the parameters the endpoint reads itself
// (parse.AcceptParams), so one request cannot carry two filters; any other
// parameter, or a body that is not application/json, is E2067. Another
// method is E2067 too: route only GET, HEAD and POST here.
func ParseList[T any](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return res.ParseList(r.URL.Query())
	case http.MethodPost:
	default:
		return nil, listError("method", fmt.Sprintf("a list is read with GET, HEAD or POST, not %s", r.Method))
	}
	query := r.URL.Query()
	for _, name := range slices.Sorted(maps.Keys(query)) {
		if !res.Accepts(name) {
			return nil, listError(name, fmt.Sprintf("a POST list takes %q in its body, not its query string", name))
		}
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		return nil, listError("content-type", "a POST list body is application/json")
	}
	return res.ParseListBody(r.Body)
}

func listError(param, msg string) *core.ParseError {
	return &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: filter.ECodeInvalidListParam.String(),
		Message: msg, Details: map[string]any{"parameter": param},
	}
}
