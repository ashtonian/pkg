// Package parse provides the Resource and SchemaRegistry that
// unify filter and patch parsing for a single resource type.
//
// For filter-only or patch-only usage, import the sub-packages directly:
//
//	import "github.com/ashtonian/pkg/parse/filter"
//	import "github.com/ashtonian/pkg/parse/patch"
//
// Shared types (FieldType, ParseError, ParserConfig, SchemaGenerator) live in:
//
//	import "github.com/ashtonian/pkg/parse/core"
//
// # Resource
//
// Resource combines filter, patch, sort, and search parsing for a resource:
//
//	tp, err := parse.NewResource[Product]("products")
//	result, err := tp.ParseFilter(`{"status":"active"}`)
//	patched, err := tp.ApplyPatch(body, existing)
//	lq := tp.ListQuery(filter.PostgresDialect{})
//
// # SchemaRegistry
//
// SchemaRegistry stores all schemas and serves them via HTTP for frontend
// auto-generation of filter sidebars, search UIs, and edit forms:
//
//	reg := parse.NewSchemaRegistry()
//	parse.MustRegister[Product](reg)
//	mux.HandleFunc("GET /api/{resource}/schema", reg.SchemaHandler())
//
// # Relationship to public-pkg/timeseries filters
//
// parse and timeseries both expose a filter/sort vocabulary, but they are
// intentionally separate and share no code. parse validates
// filters against a schema and compiles them to SQL for relational stores;
// timeseries evaluates filters against dynamic in-memory point payloads. The
// two are aligned at the vocabulary seam (e.g. the null-check operator is
// named the same — timeseries.OpNull matches parse's $null) but are NOT a
// dedup target: merging would couple timeseries to parse's schema machinery
// for no benefit.
package parse
