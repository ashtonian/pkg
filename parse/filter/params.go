package filter

// List request query parameters: the HTTP contract of a list endpoint built
// on this package. HTTP handlers read them, the TypeScript SDK sends them,
// and QueryParamParser skips them, so a flat
// query-param filter never mistakes one for a field.
const (
	ParamFilter         = "filter"          // JSON filter expression
	ParamSort           = "sort"            // "-price,name"
	ParamCursor         = "cursor"          // page_info cursor
	ParamPageSize       = "page_size"       // items per page
	ParamIncludeTotal   = "include_total"   // "true": populate page_info.total_count
	ParamIncludeDeleted = "include_deleted" // "true": include soft-deleted rows
	ParamSearch         = "search"          // free-text search
	ParamFacets         = "facets"          // comma-separated facet fields (OPTIONS)
	ParamFields         = "fields"          // comma-separated response field mask
	ParamInclude        = "include"         // comma-separated relation fields to project as _relations
)
