# parse/core

Configuration, schema generation, value normalization, JSON Pointers, and errors shared by the parser packages. Use this package to configure a typed parser or implement a custom schema adapter.

## Usage and configuration

| Guide | Contents |
|---|---|
| [Schema, tags, options, and value model](../docs/schema-configuration.md) | Consumer guide |
| [Error handling and codes](../docs/http-errors.md) | Consumer guide |
| [Concurrency and trust boundaries](../docs/operations.md) | Consumer guide |

A parser is configured by a `ParserConfig` built from `Option`s, and a schema generator by `GeneratorOption`s; both are tabled with their defaults in [parser and generator options](../docs/schema-configuration.md#parser-and-generator-options).

Start with the [runnable package example](../README.md#quick-start). The focused [core example](example_test.go) is compiled and output-checked in the unit suite.

## API reference

- Config: `DefaultConfig`, `ParserConfig` (one rule for its limits: zero is the default for depth, cost, conditions and array size, zero or less is unlimited for input size and copy bytes, zero derives the list body size), `Option`, `(*ParserConfig).Effective` (the configuration every filter and patch parser reads, defaults filled in; nil is `DefaultConfig`), `(*ParserConfig).Validate` (refuses a negative limit, naming the option), `(*ParserConfig).GetCodec`, `(*ParserConfig).ListBodyLimit` (the bound a POST list body takes: `MaxListBodySize`, or one derived from `MaxInputSize`; nil-safe), `(*ParserConfig).PatchBodyLimit` (the bound a patch body takes: `MaxInputSize`, or 1 MiB when it is unlimited; nil-safe), and the options `WithMaxDepth`, `WithMaxCost`, `WithMaxInputSize`, `WithMaxListBodySize`, `WithMaxConditions`, `WithMaxArraySize`, `WithMaxPatchCopyBytes`, `WithGeneratorOptions`, `WithClock`, `WithCodec` ([options and defaults](../docs/schema-configuration.md))
- Generation: `NewSchemaGenerator`, `SchemaGenerator` (`TableName`, `MappedExpr`, `ColumnName`, `FieldColumn`, `FieldNameTag`, `ColumnTagKey`, `StructFields`; its options are copied at construction and read through `DefaultFilterOps`, `DefaultPatchOps`, `SortMaxFields`, `SearchParam`, `SearchLanguage`, `MapsColumns`), `JSONTagKey` (the tag encoding/json names members by; the default field-name tag), `DefaultColumnTagKey`, `GeneratorOptions`, `GeneratorOption` (`WithSQLTableName`, `WithColumnTypes`, `WithColumnMapper`, `WithColumnConvention`, `WithTableConvention`, `WithFieldNameTag`, `WithColumnTag`, `WithSortMaxFields`, `WithSearchParam`, `WithSearchLanguage`, `WithForeignTagKeys`), `SQLColumnMapper`, `NamingConvention` (`SnakeCase`, the default, `CamelCase`, `PascalCase`, `LowerCase`), `TableConvention` (`PluralSnake`, the default, `Singular`, `PluralCamel`)
- Types: `FieldType` (the `FieldType*` constants, `FieldTypeString` … `FieldTypeGeo`), `SQLType` (`SQLInet`, `SQLCIDR`, `SQLInterval` and their arrays `SQLInetArray`, `SQLCIDRArray`, `SQLIntervalArray`: database types a resource declares for its dialect to read as text; `Valid`, `IsArray`, `IsJSON`), `IsValueType` / `CheckValueStorage` / `ImpliedSQLType` (the declarations a value type's column takes), `TypeOf`, `TypeInfo`, `IsDocument`, `NumericKind`, `IsUUIDType` / `UUIDText` (any UUID type: a named 16-byte array whose zero value writes the nil UUID's text), `Decimal` / `IsDecimalType` (any decimal type with the `Coefficient` / `Exponent` method set, recognized by shape), `CustomJSON` / `MarshalsJSON` (a type that codes itself as JSON through encoding/json or encoding/json/v2 methods on T or *T: the one test every reader of a document applies)
- Value types, each with one canonical text in JSON, text, SQL parameters and cursors: `Date` / `ParseDate`, `LocalTime` / `ParseLocalTime`, `Interval` / `ParseInterval`, `Inet` / `ParseInet` / `InetFromAddr` / `InetFromPrefix`, `CIDR` / `ParseCIDR` / `CIDRFromPrefix`, `MACAddr` / `ParseMACAddr`
- Stored values: `StoredValue` (the value the database holds for a Go value, and whether it is NULL: pointers followed, a `driver.Valuer` read, a type parse types by its Go type kept as it is; the one reduction cursors, facets, memberships and relation keys share), `Deref` (pointers and interfaces followed, nil for a nil one)
- Values: `NormalizeInt`, `NormalizeUint`, `NormalizeFloat`, `NormalizeScalar`, `NormalizeJSON`, `JSONEqual` (document equality as jsonb compares), `CompareNumbers`, `IsNumber`, `DecimalText` / `CompareDecimals` / `MaxDecimalDigits` (canonical exact decimal text and its order), `PortableDecimal` (a JSON number literal every backend compares as the same value), `AnySlice`, `FormatTimestamp` / `TimestampLayout` (canonical timestamp text: UTC, nine fractional digits, fixed width), `CheckText` / `CheckValueText` / `ErrInvalidText` (request text holding U+0000 or invalid UTF-8, which no backend compares as written, refused where request values are read — `CheckValueText` reaches every string of a normalized value, member names included; stored values are not checked)
- Pointers: `ParsePointer`, `FormatPointer`, `EscapePointerToken`
- Relations: `RelationSchema` (`SourceColumns`, `TargetColumns`, `OwnerKey` and `TargetKeys`: column lists that pair by position, one column each for a one-column key; `SourceTypes` and `TargetTypes`: the scalar type of each source and target column, which every statement compares it as; `OwnerColumns`: its `OwnerKey`, or `DefaultPrimaryKey` alone; `Check`: refuses lists that cannot pair and columns not typed by a scalar, naming them; `Clone`; `RelatedTable`: the table its nested predicates test), `RelationKind` (`ManyToMany`, the zero kind, `HasMany`, `BelongsTo`), `DefaultPrimaryKey` (`"id"`, a resource's or relation owner's key when none is declared), `ParseRelationTag` (a key's columns joined with `+`; its columns untyped), `SchemaGenerator.RelationTag` (a field's tag, its source columns typed by the struct's fields holding the owner key and its target by the slice's element, or, for a target key of several columns, by the element struct's fields holding them: `SchemaGenerator.RelationTargetFields`), `IsIdentifier`
- Keys: `Key` (a row's key: a value per key column, in key order), `NormalizeKey` (the one key rule: a value per column, each read as the database holds it and normalized as its column's type; the root package's targets, owners and parsed keys, patch's relation keys and filter's key sets all bind through it), `KeyValueError` (the position of the value `NormalizeKey` refused), `KeyText` (a key's text, as includes and facets name a row: a one-column key's value, a longer key's compact JSON array of its values' texts, which PostgreSQL's `array_to_json` and SQLite's `json_array` write for the same row), `KeyTextOf` / `KeyValueText` (a key's canonical text, each value normalized as its column's type: equal exactly when the database holds two keys equal, and the text includes and facets name a row by), `IsKeyType` (a scalar: the types a key, a relation's columns and a membership compare; a document, array, geometry or opaque value is none)
- Errors: `Code` and its constants `Code*` (`CodeUnknownField` = E2071, …: each code's one definition, with its status and default message; filter, patch and parse alias them as `ECode*`), `Codes` (the table, sorted: the catalog), `ParseError` (`ErrorCode`, `ErrorMessage`, `ErrorHTTPStatus`, `IsErrorUserFacing`), `ParseErrorKind` (`ParseErrorKind*`: the grammar that refused the request), `DetailField` / `DetailParameter` (the `Details` keys)
- Documents: `JSONFieldsOf` (a struct type's members as encoding/json names them, no other tag read; the module's internal key validation of stored documents reads it)
- Fields: `StructFieldsOf`, `StructFields` (`Type`, `Len`, `Fields`: an iterator in declaration order, `External`, `ColumnTagged`, `FoldedMatch`: the field encoding/json decodes a member into by case folding), `Field` (`GoName`, `Type`, `Route`, `External`, `Tags`, `Enum`, `Bounds`, `Value`, `Settable`, `SettableThrough`: `Settable` with the caller pointing each embedded pointer on the route, as the patch applier points it at a copy it owns, `CheckWritable`: refuses a field promoted through an unexported embedded pointer, which neither a row scan filling a fresh row nor a writer that must not write the caller's parent can set, `Hidden`, `HasFieldNameOption`, `Quoted`, `Wire`), `Tags`, `Capabilities` (`Has`), `Capability` (the `Capability*` constants, `CapabilityFilter`, … — the `parse` tag's values, which the `paths` tag's member capabilities share), `Bounds` (`IsZero`, `BoundsValues`, `CountsItems`, `Check`: a field's `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems`, the tags the `Constraint*` constants name) — one walk of a struct type shared by every schema; `ParseEnumTag` (an `enum` tag's values: comma-separated, or single-quoted to hold a comma, a leading or trailing space or an empty value), `EnumAllows` (whether a value is one of them, compared exactly: the one rule filter and patch both apply)
- Codec: `Codec`, `StdlibJSON`; naming: `Pluralize`; the declared-constraint names `Constraint*` (`ConstraintEnum`, …), which `patch.ConstraintError` reports

A field walk (`StructFieldsOf`, `JSONFieldsOf`, `SchemaGenerator.StructFields`) is compiled once per struct type and tag pair and shared by every generator, resource and stored-document check in the process, so it is read-only. A `*StructFields` and each `*Field` it yields are shared handles; what their methods return is either another shared handle or a value the caller owns. `Route`, `Enum` and `Bounds` allocate the caller a copy (`Enum` and `Bounds` only for a field that declares them); `Tags` is a plain value with no references, so the copy `Field.Tags` returns is the caller's; and `slices.Collect(walk.Fields())` gives the caller its own slice. `Fields`, `Len`, `External`, `ColumnTagged`, `FoldedMatch`, `Tags`, `Value`, `Settable` and the other accessors allocate nothing: per-request lookups by name and column are map reads on the shared walk.

## Code map

| File | Responsibility |
|------|----------------|
| `codec.go` | `Codec`, `StdlibJSON` and its strict request decoder |
| `types.go` | `FieldType`, `SQLType` |
| `errors.go` | `ParseError`, `ParseErrorKind`, the `Detail*` keys |
| `code.go` | The error codes and their status and message table; no application catalog dependency |
| `config.go` | `ParserConfig` and its limit rule (`Effective`, `Validate`), `DefaultConfig`, the parser options, `ListBodyLimit` and `PatchBodyLimit` (one body-limit rule) |
| `naming.go` | Column and table naming conventions, each a function, `Pluralize` |
| `generator.go` | `SchemaGenerator`, `GeneratorOptions` and their options, `SQLColumnMapper` |
| `tag_keys.go` | The tag keys parse reads, and the walked fields' keys that misspell one, which `SchemaGenerator.StructFields` refuses |
| `fields.go` | `StructFields` — one gowalker walk per struct type and tag pair, on `WithJSONFields` under the field-name tag: encoding/json field names with embedded structs promoted, hidden fields kept unnamed for columns, every tag parsed once (`Tags`); read-only handles whose accessors copy what a caller could change |
| `constraint.go` | The `Constraint*` names (each also the tag that declares it, and the constraint `patch.ConstraintError` names) |
| `bounds.go` | `Bounds` — the `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems` tags, read and checked against the field's type |
| `typeof.go` | `TypeOf` — Go type → storage model; `IsUUIDType` / `UUIDText` |
| `number.go` | Exact integer / float normalization and comparison |
| `number_literal.go` | The JSON number grammar (`scanNumber`), the one reader of number text; exact integer reading in any spelling |
| `wire.go` | `Wire`, `WireString`, `Field.Quoted` (encoding/json quotes the field), `Field.Wire` (its request form), `SchemaGenerator.MemberWire` (a document member's); `FromWire` / `ToWire` between the wire form and the canonical value |
| `decimal.go` | `Decimal`, `IsDecimalType`, canonical decimal text (`DecimalText`), exact decimal order (`CompareDecimals`) |
| `value.go` | `NormalizeScalar`, canonical timestamps, `AnySlice`, `CheckText` / `CheckValueText` |
| `stored.go` | `StoredValue`, `Deref`: a Go value as the database holds it |
| `date.go` | `Date`: SQL DATE, `YYYY-MM-DD` |
| `localtime.go` | `LocalTime`: SQL TIME, `HH:MM:SS[.ffffff]` without trailing zeros |
| `interval.go` | `Interval`: SQL INTERVAL as months, days and microseconds; ISO 8601 text as PostgreSQL's `iso_8601` style writes it; the postgres-style and pgx text drivers return |
| `inet.go` | `Inet` and `CIDR`: SQL INET and CIDR over `netip.Prefix` |
| `macaddr.go` | `MACAddr`: SQL MACADDR, lowercase colon-separated octets |
| `column_type.go` | `WithColumnTypes`, `FieldType` (a field's Go type with its declared storage), value-type storage checks |
| `json.go` | `NormalizeJSON` — canonical JSON documents |
| `custom_json.go` | `CustomJSON` / `MarshalsJSON`: whether a type codes itself as JSON, by either encoding/json version's methods |
| `json_equal.go` | `JSONEqual` — document equality, numbers by value |
| `json_fields.go` | `JSONFieldsOf` — encoding/json's member naming for document types, on gowalker's `WithJSONFields` with ties dropped; no row tag read |
| `pointer.go` | RFC 6901 pointers |
| `relation.go` | `RelationSchema` and its column and type check, relation tag grammar and its typing from a struct (`SchemaGenerator.RelationTag`), `IsIdentifier` |
| `key.go` | `Key`, `NormalizeKey`, `KeyValueError`, `KeyText`, `KeyTextOf`, `KeyValueText` |
| `doc.go` | Package documentation |

## Build, run and test

From the module directory (`parse/`):

```bash
go test -tags=unit ./core/...          # normalization, generation, pointers, codes; the core example
go test -tags=unit -race ./core/...    # as the nightly run does
```

## Operations

core does no I/O and starts no goroutines, so it has no health check, metrics or logs of its own. Its failures are values:

- A `*core.ParseError` is the request's fault: `Code` names it, `ErrorHTTPStatus()` gives its status and `Message` is written for the client. Every code is in the [error catalog](../docs/http-errors.md#error-codes).
- A plain error from schema generation or `(*ParserConfig).Validate` is the server's configuration, raised at startup by `NewResource` or the generator, naming the field or option to fix.
- Every request is bounded by the [parser limits](../docs/schema-configuration.md#parser-and-generator-options); one over a limit is refused with its code (E2061, E2063, E2074, E2088, E2089), never parsed in part.

## Security

Struct tags, column mappers, naming conventions and `WithColumnTypes` are trusted server configuration: they name the tables and columns SQL is built from, and generation refuses a name that is not an identifier. Request text is untrusted and reaches the parsers only through the limits above. `Code.IsErrorUserFacing` reports whether a code's message may be shown to a client.

## Dependencies

- *Imports*: stdlib (`uuid`, `encoding/json/v2`, `net/netip`, `net`), `github.com/ashtonian/pkg/gowalker`
- *Imported by*: `parse`, `parse/filter`, `parse/patch`, `parse/parsehttp`, `parse/parsepgx`, `parse/filter/filtertest`, `parse/internal/decimals`, the example server, and applications that use parse's types
- *Package boundary*: shared Parse types; no sibling Parse package imports

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)

Civil calendar values use `Date` / `ParseDate` and `FieldTypeDate`, distinct from timestamps. JSON, text, SQL parameters and cursors use exactly `YYYY-MM-DD`; invalid dates, instant strings and a non-nullable zero/null value fail. Use `*Date` for nullable storage. `date.go` implements validation, atomic decoding and SQL scanning/values without applying a viewing timezone; the [schema guide](../docs/schema-configuration.md#civil-dates) covers resource declarations.

`LocalTime`, `Interval`, `Inet`, `CIDR` and `MACAddr` follow `Date`'s shape for SQL TIME, INTERVAL, INET, CIDR and MACADDR: `FieldTypeTime`, `FieldTypeInterval`, `FieldTypeInet`, `FieldTypeCIDR` and `FieldTypeMACAddr`. Each reads its accepted input forms, holds and writes one canonical text, and scans the text PostgreSQL, pgx and SQLite return. The [schema guide](../docs/schema-configuration.md#time-interval-and-network-values) lists each grammar and the filters each type offers.
