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
//	relation:"tbl,src,tgt[,pk[,tenant_col]]" — M2M relation (correlated EXISTS); a+b names a key of several columns in src, tgt and pk
//	db:"column_name"        — SQL column name (falls back to snake_case of json name)
//	label:"Display Name"    — UI label for schema response
//	description:"Help text" — UI description
//	group:"section"         — UI sidebar section grouping
//	order:"1"               — UI display order within group
//	facet:"terms"           — facet type: "terms", "terms:20", "range:0-50,50-100,100+" (half-open, disjoint)
//	parse:"filter,include"  — relation field projectable with the include parameter
//	geo:"jsonb"             — geo field (jsonb | geography | geometry); geo_srid, geo_lat, geo_lon refine it
//
// # Filter Operators
//
// Field operators ([Operators]): eq, ne, gt, gte, lt, lte, in, nin, like,
// ilike, contains, has_key, null, not_null, exists, near, within,
// intersects, some, none. [OperatorsFor] lists the operators each field
// type admits; each executes with one meaning on every dialect (see the
// filtertest conformance fixture). Logical operators: $and, $or, $not.
// Relation predicates: $some, $none, $is, $is_not.
//
// # Query Param Parsing
//
// [QueryParamParser] translates flat URL query parameters into a [FilterAST]
// using suffix conventions: _ne, _gt, _gte, _lt, _lte, _in, _nin, _like,
// _ilike, _contains, _null, _not_null. No suffix means equality.
//
// # SQL Dialects
//
// [PostgresDialect] generates PostgreSQL SQL ($1 placeholders, jsonb
// documents, native arrays, PostGIS geo). [SQLiteDialect] generates SQLite
// SQL (?1 placeholders, JSON text documents and arrays matched through
// json_each). These are the only dialects, and the set is sealed: [Dialect]
// has unexported methods, so no type outside the module implements it of its
// own. A type that embeds one of the two to override a method is not a
// dialect parse supports, whatever it compiles to: the built-in methods call
// their own receiver's (a statement quotes and binds as the embedded dialect
// does, whatever the outer type declares), and a plan is matched to its
// executor by Name alone. Every piece of SQL whose form differs between
// databases is a [Dialect] method, the SQL a patch writes and what a row scan
// decodes included, so a new SQL provider is one type in this package
// implementing it, and [DialectFromDSN] maps its connection strings to it. A
// dialect's Condition builds a predicate's SQL and arguments together. A
// `$in` / `$nin` set binds as one array parameter whatever its size:
// PostgreSQL compares a column with `= ANY`, typed by the column, and joins
// a path's set unnested; SQLite reads the set with json_each. SQLite has no
// exact decimal type and no geometry, so it compares and orders decimals and
// decides geo filters through deterministic functions the database must
// have: [github.com/ashtonian/pkg/parse/parsesqlite.Register] registers
// them, and [github.com/ashtonian/pkg/parse/parsesqlite.Functions] lists
// them for another driver.
//
// A dialect's TwoValuedCondition renders some predicates a second way that
// an index serves, used where an even number of $not (usually none) enclose
// them: PostgreSQL compares a jsonb path's `$eq` and small `$in` sets by
// containment, which a GIN index on the document serves. [SQLResult].Where
// is therefore a selection predicate.
//
// # Evaluating Outside SQL
//
// [Evaluate] is the reference evaluator: the three-valued [Truth] SQL gives a
// filter on one [Row], for backends that hold rows in memory. [ColumnRow]
// reads a row of Go values by column and [ColumnValue] one value; resolve
// memberships and relation predicates first with [ResolveMemberships].
//
// # ListResult Composition
//
// [ListResult] returns all SQL parts separately (Where, OrderBy, Args,
// CountSQL, Facets) so callers can compose with custom SQL fragments.
package filter
