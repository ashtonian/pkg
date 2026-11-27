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
// # Provider and Resource
//
// A Provider is the database every resource built on it runs on: SQLite
// (parsesql) or PostgreSQL (parsepgx), one API on either, which
// parseprovider opens from the application's configuration at startup.
// A Resource combines filter, patch, sort, and search parsing for a table
// on its provider, and runs what it parses there:
//
//	db, err := parseprovider.Open(ctx, settings.DB)
//	products, err := parse.NewResource[Product](db.Provider(), "products")
//	req, err := products.ParseList(r.URL.Query())
//	page, err := products.List(ctx, req, parse.Scope{Tenant: tenant})
//	parsed, err := products.ParseMergePatch(body)
//	res, err := products.Update(ctx, parsed, parse.ByID(id), parse.Scope{Tenant: tenant})
//	patched, err := products.ApplyPatch(body, existing)
//
// In runs a resource in a transaction the provider began (Provider.Begin)
// or the application's own (parsesql.Tx, parsepgx.Tx), so parse's writes
// commit with the application's.
//
// # SchemaRegistry
//
// SchemaRegistry holds each registered resource's schema document, which
// parsehttp serves for frontend auto-generation of filter sidebars, search
// UIs, and edit forms. A name is registered once; Register[T] generates
// schemas for a type no Resource serves:
//
//	reg := parse.NewSchemaRegistry()
//	parse.MustRegisterResource(reg, products)
//	report := parsehttp.WithErrorHandler(func(r *http.Request, err error) {
//		logger.ErrorContext(r.Context(), "serving schema", slog.Any("error", err))
//	})
//	mux.HandleFunc("GET /api/{resource}/schema", parsehttp.SchemaHandler(reg, report))
//	mux.HandleFunc("GET /api/schemas", parsehttp.SchemaListHandler(reg, report))
//
// A schema document that cannot be encoded is answered 500/E2120 and handed
// to the WithErrorHandler hook; without one, the 500 is its only trace.
package parse
