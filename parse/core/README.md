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
- Generation: `NewSchemaGenerator`, `SchemaGenerator` (`TableName`, `ColumnExpr`, `ColumnName`, `FieldColumn`, `FieldNameTag`, `ColumnTagKey`, `StructFields`), `JSONTagKey` (the tag encoding/json names members by; the default field-name tag), `DefaultColumnTagKey`, `GeneratorOptions`, `GeneratorOption`, `SQLColumnMapper`, `NamingConvention`, `TableConvention`, `SnakeCase`, `PluralSnake`
- Types: `FieldType` (`FieldTypeString` … `FieldTypeGeo`), `SQLType` (`SQLInet`, `SQLCIDR`, `SQLInterval` and their arrays: database types a resource declares for its dialect to read as text), `IsValueType` / `CheckValueStorage` / `ImpliedSQLType` (the declarations a value type's column takes), `TypeOf`, `TypeInfo`, `IsDocument`, `NumericKind`, `IsUUIDType` / `UUIDText` (any UUID type, recognized by shape), `Decimal` / `IsDecimalType` (any decimal type with the `Coefficient` / `Exponent` method set, recognized by shape)
- Value types, each with one canonical text in JSON, text, SQL parameters and cursors: `Date` / `ParseDate`, `LocalTime` / `ParseLocalTime`, `Interval` / `ParseInterval`, `Inet` / `ParseInet` / `InetFromAddr` / `InetFromPrefix`, `CIDR` / `ParseCIDR` / `CIDRFromPrefix`, `MACAddr` / `ParseMACAddr`
- Values: `NormalizeInt`, `NormalizeUint`, `NormalizeFloat`, `NormalizeScalar`, `NormalizeJSON`, `CompareNumbers`, `IsNumber`, `DecimalText` / `CompareDecimals` / `MaxDecimalDigits` (canonical exact decimal text and its order), `AnySlice`, `FormatTimestamp` / `TimestampLayout` (canonical timestamp text: UTC, nine fractional digits, fixed width)
- Pointers: `ParsePointer`, `FormatPointer`, `EscapePointerToken`, `MatchWildcard`
- Relations: `RelationSchema` (`OwnerKey`: its `PrimaryKey`, or `DefaultPrimaryKey`), `DefaultPrimaryKey` (`"id"`, a resource's or relation owner's key when none is declared), `ParseRelationTag`, `IsIdentifier`
- Errors: `Code` and its constants `Code*` (`CodeUnknownField` = E2071, …: each code's one definition, with its status and default message; filter, patch and parse alias them as `ECode*`), `ParseError` (`ErrorCode`, `ErrorHTTPStatus`, `IsErrorUserFacing`), `ParseErrorKind`, `DetailField` / `DetailParameter` (the `Details` keys); policy: `PolicySubject`
- Documents: `JSONFieldsOf` (a struct type's members as encoding/json names them, no other tag read), `ValidateJSONKeyRepresentation` (a stored document's member names against its Go type, in one pass), `CanonicalJSONMapKey` (a member name as a map key and back)
- Fields: `StructFieldsOf`, `StructFields` (`External`, `ColumnTagged`), `Field` (`Value`, `Settable`, `Hidden`), `Tags`, `Capabilities` (`Has`), `Capability` (`CapabilityFilter`, … — the `parse` tag's values, which the `paths` tag's member capabilities share), `Bounds` (`IsZero`, `BoundsValues`, `CountsItems`, `Check`: a field's `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems`, the tags the `Constraint*` constants name) — one walk of a struct type shared by every schema
- Codec: `Codec`, `StdlibJSON`; naming: `ToSnakeCase`, `Pluralize`; `Validator`

## Code map

| File | Responsibility |
|------|----------------|
| `codec.go` | `Codec`, `StdlibJSON` and its strict request decoder |
| `types.go` | `FieldType`, `SQLType` |
| `errors.go` | `ParseError`, `ParseErrorKind`, the `Detail*` keys |
| `code.go` | The error codes and their status and message table; no application catalog dependency |
| `config.go` | `ParserConfig`, `DefaultConfig`, the parser options, `ListBodyLimit` |
| `naming.go` | Column and table naming conventions, `Pluralize`, `ToSnakeCase` |
| `generator.go` | `SchemaGenerator`, `GeneratorOptions` and their options, `SQLColumnMapper` |
| `core.go` | `Validator`, `PolicySubject`, `MatchWildcard` |
| `fields.go` | `StructFields` — one gowalker walk per struct type: encoding/json field names with embedded structs promoted, hidden fields kept for columns, every tag parsed once (`Tags`) |
| `constraint.go` | The `Constraint*` names (each also the tag that declares it), `ConstraintError` |
| `bounds.go` | `Bounds` — the `minlen` / `maxlen` / `min` / `max` / `minitems` / `maxitems` tags, read and checked against the field's type |
| `typeof.go` | `TypeOf` — Go type → storage model; `IsUUIDType` / `UUIDText` |
| `number.go` | Exact integer / float normalization and comparison |
| `number_literal.go` | The JSON number grammar (`scanNumber`), the one reader of number text; exact integer reading in any spelling |
| `wire.go` | `Wire`, `WireString`, `Field.Quoted` (encoding/json quotes the field), `Field.Wire` (its request form), `SchemaGenerator.MemberWire` (a document member's); `FromWire` / `ToWire` between the wire form and the canonical value |
| `decimal.go` | `Decimal`, `IsDecimalType`, canonical decimal text (`DecimalText`), exact decimal order (`CompareDecimals`) |
| `value.go` | `NormalizeScalar`, canonical timestamps, `AnySlice` |
| `date.go` | `Date`: SQL DATE, `YYYY-MM-DD` |
| `localtime.go` | `LocalTime`: SQL TIME, `HH:MM:SS[.ffffff]` without trailing zeros |
| `interval.go` | `Interval`: SQL INTERVAL as months, days and microseconds; ISO 8601 text as PostgreSQL's `iso_8601` style writes it; the postgres-style and pgx text drivers return |
| `inet.go` | `Inet` and `CIDR`: SQL INET and CIDR over `netip.Prefix` |
| `macaddr.go` | `MACAddr`: SQL MACADDR, lowercase colon-separated octets |
| `column_type.go` | `WithColumnTypes`, `FieldType` (a field's Go type with its declared storage), value-type storage checks |
| `json.go` | `NormalizeJSON` — canonical JSON documents |
| `json_fields.go` | `JSONFieldsOf` — encoding/json's member naming for document types: embedded structs promoted, ties dropped, no row tag read |
| `json_keys.go` | `ValidateJSONKeyRepresentation`, `CanonicalJSONMapKey` — a stored document's member names keep their JSON spelling; each type's shape and map key rule found once |
| `pointer.go` | RFC 6901 pointers |
| `relation.go` | Relation tag grammar, `IsIdentifier` |
| `doc.go` | Package documentation |

## Dependencies

- *Imports*: stdlib (`uuid`, `encoding/json/v2`, `net/netip`, `net`), `github.com/ashtonian/pkg/gowalker`
- *Imported by*: `parse`, `parse/filter`, `parse/patch`, `parse/parsehttp`, `parse/parsepgx`, `parse/filter/filtertest`, `parse/internal/decimals`, the example server, and applications that use parse's types
- *Package boundary*: shared Parse types; no sibling Parse package imports

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)

Civil calendar values use `Date` / `ParseDate` and `FieldTypeDate`, distinct from timestamps. JSON, text, SQL parameters and cursors use exactly `YYYY-MM-DD`; invalid dates, instant strings and a non-nullable zero/null value fail. Use `*Date` for nullable storage. `date.go` implements validation, atomic decoding and SQL scanning/values without applying a viewing timezone; the [schema guide](../docs/schema-configuration.md#civil-dates) covers resource declarations.

`LocalTime`, `Interval`, `Inet`, `CIDR` and `MACAddr` follow `Date`'s shape for SQL TIME, INTERVAL, INET, CIDR and MACADDR: `FieldTypeTime`, `FieldTypeInterval`, `FieldTypeInet`, `FieldTypeCIDR` and `FieldTypeMACAddr`. Each reads its accepted input forms, holds and writes one canonical text, and scans the text PostgreSQL, pgx and SQLite return. The [schema guide](../docs/schema-configuration.md#time-interval-and-network-values) lists each grammar and the filters each type offers.
