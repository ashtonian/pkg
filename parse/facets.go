package parse

import (
	"fmt"
	"maps"
	"net/url"
	"slices"

	"github.com/ashtonian/pkg/parse/filter"
)

// facetParams are the list parameters a facets request reads. The others
// shape a page, and a facets request has none.
var facetParams = []string{filter.ParamFacets, filter.ParamFilter, filter.ParamSearch, filter.ParamIncludeDeleted}

// ParseFacets reads a facets request's query parameters: facets, the JSON
// filter and the flat field filters, search and include_deleted, read and
// refused as ParseList reads and refuses them. A list parameter that shapes
// a page (cursor, sort, page_size, include_total, include, fields) is
// E2067, never accepted and ignored. BuildFacets compiles the request.
func (r *resourceCore) ParseFacets(q url.Values) (*ListRequest, error) {
	for _, name := range slices.Sorted(maps.Keys(q)) {
		if filter.IsListParam(name) && !slices.Contains(facetParams, name) {
			return nil, listParamError(name, fmt.Sprintf("a facets request takes no %s: it counts the filtered rows and has no page", name))
		}
	}
	return r.ParseList(q)
}

// BuildFacets compiles a facets request (ParseFacets) under scope s into
// the facet statement Facets runs, the one BuildList compiles for a page
// asking for the same facets: the same filter, search, tenant floor,
// soft-delete masking and policy. A request naming no facets compiles to
// nil.
func (r *resourceCore) BuildFacets(req *ListRequest, s Scope) (*filter.FacetQuery, error) {
	plan, err := r.BuildList(req, s)
	if err != nil {
		return nil, err
	}
	return plan.Facets, nil
}
