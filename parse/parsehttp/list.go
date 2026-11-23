package parsehttp

import (
	"fmt"
	"net/http"
	"net/url"

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
	case http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindFilter, Code: filter.ECodeInvalidListParam.String(),
			Message: fmt.Sprintf("a list is read with GET, HEAD or POST, not %s", r.Method),
			Details: map[string]any{core.DetailParameter: "method"},
		}
	}
	q, err := Query(r)
	if err != nil {
		return nil, err
	}
	if r.Method == http.MethodPost {
		return res.ParseListPost(q, r.Header.Get("Content-Type"), r.Body)
	}
	return res.ParseList(q)
}

// Query decodes r's query string, refusing it whole, as E2067, when any of
// it does not decode: an invalid percent-escape or a semicolon. url.URL.Query
// drops such a pair and keeps the rest, so a filter the request named would
// vanish and the list widen, or an update reach rows the client did not
// name. Pass Query's values, never r.URL.Query(), to a parse entry point.
func Query(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindFilter, Code: filter.ECodeInvalidListParam.String(),
			Message: fmt.Sprintf("the query string does not decode: %v", err),
			Cause:   err,
		}
	}
	return q, nil
}

// ParseFacets reads a facets request from r's query string, decoded by
// Query (Resource.ParseFacets): facets, the filters, search and
// include_deleted; a list parameter that shapes a page is E2067. The SDK
// sends it as OPTIONS /{resource}?facets=…, so the route must reach an
// OPTIONS that is not a CORS preflight, and it has no body form.
func ParseFacets[T any](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error) {
	q, err := Query(r)
	if err != nil {
		return nil, err
	}
	return res.ParseFacets(q)
}
