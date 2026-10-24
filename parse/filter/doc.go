// Package filter provides secure parsing of query filters with multi-dialect
// SQL generation, faceted aggregation, cursor pagination, full-text search,
// and query param parsing.
//
// # Struct Tags
//
// Filter schema generation is driven by struct tags. The following tags are
// recognized by [GenerateFilterSchema], [GenerateSortSchema], and
// [GenerateSearchSchema]:
//
//	parse:"filter"          — include in filter schema
//	parse:"filter,facet"    — include in filter + facet schemas
//	parse:"filter,search"   — include in filter + search schemas
//	filter_ops:"eq,ne,gt"   — allowed filter operators (each must apply to the type: [OperatorsFor])
//	enum:"active,inactive"  — restrict to these values
//	indexed:"true"          — cost estimation hint (indexed = cheaper)
//	parse:"…,sort"          — mark as sortable
//	sort_default:"desc"     — default sort field + direction
//	relation:"tbl,src,tgt[,pk[,tenant_col]]" — M2M relation (correlated EXISTS)
//	db:"column_name"        — SQL column name (falls back to snake_case of json name)
//	label:"Display Name"    — UI label for schema response
//	description:"Help text" — UI description
//	group:"section"         — UI sidebar section grouping
//	order:"1"               — UI display order within group
//	facet:"terms"           — facet type: "terms", "terms:20", "range:0-50,50-100,100+"
//	search_weight:"A"       — full-text search weight: A (highest) through D (lowest)
//	parse:"filter,include"  — relation field projectable with the include parameter
//	geo:"jsonb"             — geo field (jsonb | geography | geometry); geo_srid, geo_lat, geo_lon refine it
//
// # Filter Operators
//
// Supported operators: eq, ne, gt, gte, lt, lte, in, nin, like, ilike, null,
// not_null, exists, contains, near, within, intersects. Logical operators:
// $and, $or, $not. [OperatorsFor] lists the operators each field type
// admits; each executes with one meaning on every dialect (see the
// filtertest conformance fixture).
//
// # Query Param Parsing
//
// [QueryParamParser] translates flat URL query parameters into a [FilterAST]
// using suffix conventions: _gt, _gte, _lt, _lte, _ne, _in, _nin, _like,
// _ilike, _null, _contains. No suffix means equality.
//
// # SQL Dialects
//
// [PostgresDialect] generates PostgreSQL SQL ($1 placeholders, jsonb
// documents, native arrays, PostGIS geo). [SQLiteDialect] generates SQLite
// SQL (?1 placeholders, JSON text documents and arrays matched through
// json_each). A dialect's Condition builds a predicate's SQL and arguments
// together.
//
// # ListResult Composition
//
// [ListResult] returns all SQL parts separately (Where, OrderBy, Args,
// CountSQL, Facets) so callers can compose with custom SQL fragments.
package filter
