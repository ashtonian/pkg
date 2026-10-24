// Package core provides shared types for the parse/filter and parse/patch
// sub-packages: field types, error types, parser configuration, and the
// schema generator with configurable naming conventions.
//
// Most users import parse or its sub-packages (filter, patch) directly.
// Import core when you need shared types for custom schema construction or
// when configuring the schema generator.
//
// # Naming Conventions
//
// The schema generator converts Go struct field names to SQL column names
// and type names to table names. Override the defaults with convention
// functions:
//
//	gen := core.NewSchemaGenerator(
//	    core.WithColumnConvention(core.CamelCase),     // productType instead of product_type
//	    core.WithTableConvention(core.Singular),        // product instead of products
//	    core.WithFieldNameTag("bson"),                   // read field names from bson tag
//	    core.WithColumnTag("sql"),                       // read column names from sql tag
//	)
//
// # Tag Resolution Priority
//
// Column name: [SQLColumnMapper] → column tag value (default "db") → [NamingConvention] → snake_case.
//
// Table name: [WithSQLTableName] → [TableConvention] → plural snake_case.
//
// Field name: configured tag (default "json") → skip if empty.
//
// # Error Types
//
// [ParseError] is the single error type returned by all parse operations. It
// carries an E-code, a message, and
// the offending path or operation index; errors.Is matches it against any
// error with the same code.
//
// # Values
//
// [NormalizeScalar], [NormalizeInt], [NormalizeFloat] and [NormalizeJSON]
// give request values their canonical form — exact integers range-checked
// against the destination kind, canonical UUIDs and UTC timestamps, generic
// JSON documents — and [TypeOf] classifies Go field types into the storage
// model schemas are built from.
package core
