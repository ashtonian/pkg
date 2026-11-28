package sqlplan

import (
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// List is a composed list compiled for a dialect: the statements a list
// runs before its includes, which need the page's keys (Includes).
type List struct {
	// Page selects the resource's Columns for the page: up to the list's
	// limit plus one rows, in keyset order.
	Page Statement
	// Fragment is Page's `WHERE … ORDER BY … LIMIT n` alone, with
	// Page.Args.
	Fragment string
	// Count is the matched population's size (filter, policy and search,
	// no cursor) when the list counts.
	Count *Statement
	// Facets counts the same population by the list's facets.
	Facets *filter.FacetQuery
}

// CompileList compiles req's composed list for d (filter.ComposedList.Compile)
// over the resource's projection.
func CompileList(d filter.Dialect, req backend.ListRequest) (*List, error) {
	sql, err := sqlOf(req.Resource)
	if err != nil {
		return nil, err
	}
	res, err := req.List.Compile(d)
	if err != nil {
		return nil, err
	}
	list := &List{
		Page:     Statement{SQL: "SELECT " + sql.Projection + " FROM " + d.QuoteIdent(req.Resource.Table) + " " + res.Fragment, Args: res.Args},
		Fragment: res.Fragment,
		Facets:   res.Facets,
	}
	if req.List.Count() {
		list.Count = &Statement{SQL: res.CountSQL, Args: res.CountArgs}
	}
	return list, nil
}

// Includes are the statements reading, for each relation field req
// includes, its related keys for owners, the page's rows' keys normalized
// (backend.ListSink.Keys), scoped as the list was. A field with nothing to
// read compiles to no statement, and no owners need none.
func Includes(d filter.Dialect, req backend.ListRequest, owners []core.Key) ([]filter.IncludeQuery, error) {
	if len(req.Include) == 0 || len(owners) == 0 {
		return nil, nil
	}
	schema := req.Resource.Schema
	opts := []filter.IncludeQueryOption{filter.WithIncludeRelationScope(req.List.RelationScope()), filter.WithIncludeOwner(schema)}
	out := make([]filter.IncludeQuery, 0, len(req.Include))
	for _, name := range req.Include {
		q, err := filter.BuildIncludeQuery(schema.Fields[name], d, owners, opts...)
		if err != nil {
			return nil, err
		}
		if q.SQL != "" {
			out = append(out, q)
		}
	}
	return out, nil
}
