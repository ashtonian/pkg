# parse/core

Configuration, schema generation, value normalization, JSON Pointers, and errors shared by the parser packages. Use this package to configure a typed parser or implement a custom schema adapter.

## Usage and configuration

| Guide | Contents |
|---|---|
| [Schema, tags, options, and value model](../docs/schema-configuration.md) | Consumer guide |
| [Error handling and codes](../docs/http-errors.md) | Consumer guide |
| [Concurrency and trust boundaries](../docs/operations.md) | Consumer guide |

Start with the [runnable package example](../README.md#quick-start). The focused [core example](example_test.go) is compiled and output-checked in the unit suite.

## API reference

- Config: `DefaultConfig`, `ParserConfig`, `Option`, `(*ParserConfig).GetCodec`, `(*ParserConfig).ListBodyLimit` (the bound a POST list body takes: `MaxListBodySize`, or one derived from `MaxInputSize`)
- Generation: `NewSchemaGenerator`, `SchemaGenerator` (`TableName`, `ColumnExpr`, `ColumnName`, `FieldColumn`, `FieldNameTag`, `ColumnTagKey`, `StructFields`), `GeneratorOptions`, `GeneratorOption`, `SQLColumnMapper`, `NamingConvention`, `TableConvention`, `SnakeCase`, `PluralSnake`
- Types: `FieldType` (`FieldTypeString` … `FieldTypeGeo`), `SQLType` (`SQLInet`, `SQLCIDR`, `SQLInterval` and their arrays: database types a resource declares for its dialect to read as text), `IsValueType` / `CheckValueStorage` / `ImpliedSQLType` (the declarations a value type's column takes), `TypeOf`, `TypeInfo`, `IsDocument`, `NumericKind`, `IsUUIDType` / `UUIDText` (any UUID type, recognized by shape), `Decimal` / `IsDecimalType` (any decimal type with the `Coefficient` / `Exponent` method set, recognized by shape)
- Value types, each with one canonical text in JSON, text, SQL parameters and cursors: `Date` / `ParseDate`, `LocalTime` / `ParseLocalTime`, `Interval` / `ParseInterval`, `Inet` / `ParseInet` / `InetFromAddr` / `InetFromPrefix`, `CIDR` / `ParseCIDR` / `CIDRFromPrefix`, `MACAddr` / `ParseMACAddr`
- Values: `NormalizeInt`, `NormalizeUint`, `NormalizeFloat`, `NormalizeScalar`, `NormalizeJSON`, `CompareNumbers`, `IsNumber`, `DecimalText` / `CompareDecimals` / `MaxDecimalDigits` (canonical exact decimal text and its order), `AnySlice`, `FormatTimestamp` / `TimestampLayout` (canonical timestamp text: UTC, nine fractional digits, fixed width)
- Pointers: `ParsePointer`, `FormatPointer`, `EscapePointerToken`, `MatchWildcard`
- Relations: `RelationSchema`, `ParseRelationTag`, `IsIdentifier`
- Errors: `Code` (string constants; error identity, status and default message), `ParseError` (`ErrorCode`, `ErrorHTTPStatus`, `IsErrorUserFacing`), `ParseErrorKind`; policy: `PolicySubject`
- Fields: `StructFieldsOf`, `StructFields` (`External`, `ColumnTagged`), `Field` (`Value`, `Settable`, `Hidden`), `Tags`, `Capabilities`, `Bounds` (`IsZero`, `BoundsValues`, `CountsItems`, `Check`: a field's `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems`) — one walk of a struct type shared by every schema
- Codec: `Codec`, `StdlibJSON`; naming: `ToSnakeCase`, `Pluralize`; `Validator`

## Code map

| File | Responsibility |
|------|----------------|
| `core.go` | `ParserConfig` + options, `Codec` / `StdlibJSON`, `FieldType`, `SQLType`, `ParseError`, `SchemaGenerator` + generator options |
| `code.go` | Immutable error-code metadata; no application catalog dependency |
| `fields.go` | `StructFields` — one gowalker walk per struct type: encoding/json field names with embedded structs promoted, hidden fields kept for columns, every tag parsed once (`Tags`) |
| `bounds.go` | `Bounds` — the `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems` tags, read and checked against the field's type |
| `typeof.go` | `TypeOf` — Go type → storage model; `IsUUIDType` / `UUIDText` |
| `number.go` | Exact integer / float normalization and comparison |
| `decimal.go` | `Decimal`, `IsDecimalType`, canonical decimal text (`DecimalText`), exact decimal order (`CompareDecimals`) |
| `value.go` | `NormalizeScalar`, canonical timestamps, `AnySlice` |
| `date.go` | `Date`: SQL DATE, `YYYY-MM-DD` |
| `localtime.go` | `LocalTime`: SQL TIME, `HH:MM:SS[.ffffff]` without trailing zeros |
| `interval.go` | `Interval`: SQL INTERVAL as months, days and microseconds; ISO 8601 text as PostgreSQL's `iso_8601` style writes it; the postgres-style and pgx text drivers return |
| `inet.go` | `Inet` and `CIDR`: SQL INET and CIDR over `netip.Prefix` |
| `macaddr.go` | `MACAddr`: SQL MACADDR, lowercase colon-separated octets |
| `column_type.go` | `WithColumnTypes`, `FieldType` (a field's Go type with its declared storage), value-type storage checks |
| `json.go` | `NormalizeJSON` — canonical JSON documents |
| `pointer.go` | RFC 6901 pointers |
| `relation.go` | Relation tag grammar, `IsIdentifier` |
| `doc.go` | Package documentation |

## Dependencies

- *Imports*: stdlib (`uuid`, `encoding/json/v2`, `net/netip`, `net`), `github.com/ashtonian/pkg/gowalker`
- *Imported by*: `parse`, `parse/filter`, `parse/patch`, `parse/parsehttp`, `parse/parsepgx`, `parse/filter/filtertest`, the example server, and applications that use parse's types
- *Package boundary*: shared Parse types; no sibling Parse package imports

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)

Civil calendar values use `Date` / `ParseDate` and `FieldTypeDate`, distinct from timestamps. JSON, text, SQL parameters and cursors use exactly `YYYY-MM-DD`; invalid dates, instant strings and a non-nullable zero/null value fail. Use `*Date` for nullable storage. `date.go` implements validation, atomic decoding and SQL scanning/values without applying a viewing timezone; the [schema guide](../docs/schema-configuration.md#civil-dates) covers resource declarations.

`LocalTime`, `Interval`, `Inet`, `CIDR` and `MACAddr` follow `Date`'s shape for SQL TIME, INTERVAL, INET, CIDR and MACADDR: `FieldTypeTime`, `FieldTypeInterval`, `FieldTypeInet`, `FieldTypeCIDR` and `FieldTypeMACAddr`. Each reads its accepted input forms, holds and writes one canonical text, and scans the text PostgreSQL, pgx and SQLite return. The [schema guide](../docs/schema-configuration.md#time-interval-and-network-values) lists each grammar and the filters each type offers.
