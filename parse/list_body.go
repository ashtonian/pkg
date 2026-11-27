package parse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/url"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

// ParseListBody validates a list request sent as one JSON document, a POST
// body beside the query string ParseList reads. Its members are the list
// parameters (filter.Param*): filter is the filter document itself, an
// object; sort, cursor and search are strings in their grammars; page_size
// is a positive integer; include_total and include_deleted are booleans;
// facets, include and fields are arrays of names. It is validated as
// ParseListInput validates, with the same limits and error codes. The
// search is the member search whatever query parameter the search schema
// advertises (core.WithSearchParam); the schema document states both.
//
// The filter member is bounded by MaxInputSize, as the filter parameter is.
// The whole body is bounded by the parser's list body limit
// (core.WithMaxListBodySize; by default MaxInputSize plus room for the
// other members), and a body over it is E2063, read no further. A body that
// is not one JSON object, or names a member twice, is E2086. An unknown
// member, or a member of the wrong JSON type, is E2067, so a misspelt member
// cannot widen a list. Flat field filters do not apply: a body has filter.
// A read that fails is a *BodyReadError, not a request error.
func (r *Resource[T]) ParseListBody(body io.Reader) (*ListRequest, error) {
	data, err := r.readBody(body, r.cfg.ListBodyLimit(), "list")
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := r.codec.Decode(data, &members); err != nil || members == nil {
		return nil, core.NewError(filter.ECodeInvalidJSON, "a list body is one JSON object, each member named once",
			core.WithCause(err))
	}
	var in ListInput
	// Sorted, so a body with several bad members reports the same one each
	// time.
	for _, name := range slices.Sorted(maps.Keys(members)) {
		if err := r.listMember(&in, name, members[name]); err != nil {
			return nil, err
		}
	}
	return r.ParseListInput(in)
}

// listMember decodes one member of a list body into in; null is an absent
// member.
func (r *Resource[T]) listMember(in *ListInput, name string, raw json.RawMessage) error {
	if !filter.IsListParam(name) {
		return listParamError(name, fmt.Sprintf("a list body has no member %q", name))
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	decode := func(v any, want string) error {
		if err := r.codec.Decode(raw, v); err != nil {
			return listParamError(name, fmt.Sprintf("%s must be %s", name, want))
		}
		return nil
	}
	switch name {
	case filter.ParamFilter:
		var doc map[string]json.RawMessage
		if err := decode(&doc, "a filter document (an object)"); err != nil {
			return err
		}
		in.Filter = string(raw)
		return nil
	case filter.ParamSort:
		return decode(&in.Sort, "a string")
	case filter.ParamCursor:
		return decode(&in.Cursor, "a string")
	case filter.ParamSearch:
		return decode(&in.Search, "a string")
	case filter.ParamPageSize:
		var size int64
		if err := decode(&size, "a positive integer"); err != nil {
			return err
		}
		if size <= 0 || int64(int(size)) != size {
			return listParamError(name, fmt.Sprintf("page_size must be a positive integer, got %d", size))
		}
		in.PageSize = int(size)
		return nil
	case filter.ParamIncludeTotal:
		return decode(&in.IncludeTotal, "a boolean")
	case filter.ParamIncludeDeleted:
		return decode(&in.IncludeDeleted, "a boolean")
	case filter.ParamFacets:
		return decode(&in.Facets, "an array of field names")
	case filter.ParamInclude:
		return decode(&in.Include, "an array of field names")
	case filter.ParamFields:
		return decode(&in.Fields, "an array of field names")
	}
	return nil
}

// ParseListPost validates a list request a POST sends: its query string,
// the value of its Content-Type header and its body, for a transport that
// hands a handler those parts rather than an *http.Request
// (parsehttp.ParseList is this over one). The body is read with
// ParseListBody. The query string carries only the parameters the endpoint
// reads itself (AcceptParams), so one request cannot carry two filters, and
// the body is application/json; anything else is E2067.
func (r *Resource[T]) ParseListPost(query url.Values, contentType string, body io.Reader) (*ListRequest, error) {
	for _, name := range slices.Sorted(maps.Keys(query)) {
		switch {
		case r.Accepts(name):
			continue
		case name == r.searchParam:
			return nil, listParamError(name, fmt.Sprintf("a POST list takes its search in the body member %q, not the query parameter %q", filter.ParamSearch, name))
		}
		return nil, listParamError(name, fmt.Sprintf("a POST list takes %q in its body, not its query string", name))
	}
	if media, _, err := mime.ParseMediaType(contentType); err != nil || media != httpjson.MediaType {
		return nil, listParamError("content-type", "a POST list body is application/json")
	}
	return r.ParseListBody(body)
}

// Accepts reports whether the endpoint reads query parameter name itself
// (AcceptParams), so ParseList leaves it alone.
func (r *Resource[T]) Accepts(name string) bool { return slices.Contains(r.accepted, name) }
