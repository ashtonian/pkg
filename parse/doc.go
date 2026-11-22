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
//	products, err := parse.NewResource[Product]("products")
//	result, err := products.ParseFilter(`{"status":"active"}`)
//	patched, err := products.ApplyPatch(body, existing)
//	lq := products.ListQuery(filter.PostgresDialect{})
//
// # SchemaRegistry
//
// SchemaRegistry stores all schemas and serves them via HTTP for frontend
// auto-generation of filter sidebars, search UIs, and edit forms:
//
//	reg := parse.NewSchemaRegistry()
//	parse.MustRegister[Product](reg)
//	report := parse.WithErrorHandler(func(r *http.Request, err error) {
//		logger.ErrorContext(r.Context(), "serving schema", slog.Any("error", err))
//	})
//	mux.HandleFunc("GET /api/{resource}/schema", reg.SchemaHandler(report))
//	mux.HandleFunc("GET /api/schemas", reg.SchemaListHandler(report))
//
// A schema document that cannot be encoded is answered 500/E2120 and handed
// to the WithErrorHandler hook; without one, the 500 is its only trace.
package parse
