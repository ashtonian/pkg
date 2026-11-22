# Schema and configuration

[Package overview](../README.md)

Construct one `parse.Resource[T]` for each resource at startup. Schema generation validates tags before requests arrive; explicit tags expose only the operations the endpoint supports. Customize schemas before serving requests, then treat them as immutable.

## Struct tags

| Tag | Example | Used by | Meaning |
|-----|---------|---------|---------|
| `parse` | `"filter,sort,patch,facet,search,include"` | all | Subsystems the field joins; `"-"` excludes it. `server` admits the field to `ParseServerMergePatch` only: a column only the server sets. Any other value fails schema generation |
| `json` | `"name"` | all | External field name, as Go 1.27's encoding/json reads it: the tag's name, else the Go name; `"-"` hides the field from requests; `"-,"` names it `-` (`core.WithFieldNameTag` picks another tag) |
| `db` | `"name"` | all | SQL column; must be an identifier; options after `,` are ignored; `"-"` is not a column. Default: snake_case of the external name, or of the Go name for a field `json:"-"` hides (`core.WithColumnTag` picks another tag) |
| `filter_ops` | `"eq,ne,in"` | filter | Allowed operators; each must apply to the field's type (`filter.OperatorsFor`) |
| `patch_ops` | `"replace,remove"` | patch | Allowed operations; `remove` requires a nullable Go type, and not a slice `minitems` keeps non-empty |
| `enum` | `"active,offline"` | filter, patch | Allowed string values (per element on array fields) |
| `minlen`, `maxlen` | `"3"` | patch | Fewest and most characters of a string, inclusive (per element on array fields). Characters are Unicode code points, as a `CHAR(n)` / `VARCHAR(n)` column and `char_length` count them. A non-negative integer; `minlen` above `maxlen`, or either on a field that is not a string or an array of strings, fails schema generation |
| `min`, `max` | `"0.01"`, `"1e6"` | patch | Least and greatest number, inclusive (per element on array fields). A decimal literal compared exactly with an int or decimal value, and at the field's precision with a float (`max:"0.1"` admits a `float32` field's 0.1). `min` above `max`, or either on a field that is not a number or an array of numbers, fails schema generation |
| `minitems`, `maxitems` | `"1"`, `"8"` | patch | Fewest and most elements of an array, inclusive, as PostgreSQL's `cardinality` counts them. Checked on an array a `replace` or `add` sets whole; an element added, appended or removed is left to the store. A slice field with `minitems` above zero is not nullable: null and `remove` would empty it, so the schema drops `remove` and refuses null (E2077), while a pointer to a slice still clears to NULL. A non-negative integer; `minitems` above `maxitems`, either on a field that is not an array, or on a relation, fails schema generation |
| `indexed` | `"true"` | filter | Cost hint: the field is indexed |
| `immutable` | `"true"` | filter | Advertised in the schema response |
| `sort_default` | `"desc"` | sort | The default sort field and direction |
| `relation` | `"device_tags,device_id,tag_id[,pk[,tenant_col]]"` | filter, patch, facet, include | A virtual relation field: the table holding the pairs, its owner and target columns (see Relations); every part must be an identifier |
| `relation_name` | `"tag_ids"` | hidden relation fields | Explicit external name when a virtual relation is hidden by the field-name tag (for example `json:"-"`); preserves acronym spellings. Ordinary visible field names are unchanged |
| `facet` | `"terms:20"`, `"range:0-50,50-100,100+"` | facet | Facet aggregation |
| `search_weight` | `"A"` … `"D"` | search | Full-text weight |
| `elements` | `"by:string;priority:int"` | filter | Typed members of each element of a JSON array, filtered together with `$some` / `$none` ([array elements](queries.md#array-elements)); a slice-of-struct document derives them from its element type's members tagged `parse:"filter"` |
| `paths` | `"color:string:filter+sort+facet;size:int:filter+indexed;dims.w:float;tags:[]string"` | filter, sort, facet | Typed members of a map / `any` document, filterable (with `sort` sortable, with `facet` a terms facet) as `<field>.<member>` ([JSON paths](queries.md#json-paths)); `[]<type>` declares an array of that type, filtered by `$contains` and its elements by `$some` / `$none` ([array elements](queries.md#array-elements)), never sorted, faceted by terms only; struct documents derive theirs from members tagged `parse:"filter"` / `parse:"sort"` / `parse:"facet"` |
| `storage` | `"jsonb"` (default), `"json"`, `"text"`, `"scalar:decimal"` | filter, sort | How a document column holds its JSON; `scalar:<type>` marks a struct the database holds as that scalar — `string`, `int`, `float`, `decimal`, `bool`, `timestamp` or `uuid` — which filters and sorts as one (no paths) |
| `geo` | `"jsonb"`, `"geography"`, `"geometry"` | filter | Geo field storage |
| `geo_srid` | `"3857"` | filter | SRID of a native `geometry` column (jsonb / geography are 4326) |
| `geo_lat`, `geo_lon` | `"$.latitude"` | filter | Members of a JSONB point |
| `label`, `description`, `group`, `order` | `"Name"` | schema response | UI metadata |
| `json:",string"` | `json:"id,string"` | filter, patch | A bool, integer or float column travels as a JSON string holding its value, as encoding/json writes it ([quoted values](#quoted-values)) |

### Fields and embedding

Every schema, the resource's columns, its cursors and its patch paths come from one walk of the type (`core.StructFields`), so they always agree. The walk sees the fields encoding/json writes. A struct embedded without a field-name tag has its fields promoted, through any depth, through pointers and through unexported struct types, so a server type can embed its wire type and keep every filter, sort and patch field. Fields `json:"-"` hides stay in the walk for columns and server-only paths.

A name two fields claim goes to the field encoding/json writes: the shallower one, and at one depth the one whose tag names it. So a server type can shadow a field of the wire type it embeds, with another column or a narrower type, and requests reach the outer field.

Schema generation refuses the shapes encoding/json resolves silently:

- two promoted fields with one name at the same depth, both tagged or both untagged (encoding/json drops both);
- an embedded struct hidden whole by `json:"-"` (hide its fields one by one);
- an unexported embedded struct named by the field-name tag;
- two fields with one column.

A stored document's members are named as encoding/json names them, by the `json` tag alone (`core.JSONFieldsOf`): the document type's other tags are not read, a struct it embeds with `json:"-"` holds nothing, and a name two of its fields claim at one depth belongs to neither. Reading a row checks each JSON column's member names against its Go type in one pass (`core.ValidateJSONKeyRepresentation`): a struct member spelled in another case, a map key that does not read back as written (`"01"` for an integer key, `"0:0:0:0:0:0:0:1"` for a `netip.Addr`) and a member given twice are refused. Merge patches name a document's members the same way, so a document type held in a map, a pointer, a slice or an array works wherever it sits. A document column's own struct type and element type are also walked with the rules above when the resource is built, for the member capabilities their `parse` tags grant.

A field's data type comes from its Go type (`core.TypeOf`): string / int / float / decimal / bool / timestamp / date / uuid scalars, the [time, interval and network value types](#time-interval-and-network-values), a UUID being any named 16-byte array whose zero value marshals to the nil UUID's text (the standard library's `uuid.UUID`, `github.com/google/uuid`, `github.com/gofrs/uuid`), while a named 16-byte array that marshals to other text, such as a ULID or a hexadecimal hash, is a string compared, sorted and paged by that text, stored as text, and a bare `[16]byte` is binary and a decimal any named type with the `core.Decimal` method set ([decimals](#decimals)); slices of those other than decimals are native `array`s (PostgreSQL arrays, JSON text on SQLite); maps, `any`, `json.RawMessage` and other slices are `json` documents (`jsonb`); structs are `object` documents.

Declare physical storage with `parse.ColumnType(column, core.SQLJSON)` / `core.SQLJSONB`, or `core.WithColumnTypes(map[string]core.SQLType{...})` for a lower-level parser. Column names follow the configured column tag/mapper, including hidden and unread fields. Declared JSON strings, bytes and scalar slices use document operators and JSON binding; typed objects retain their member schemas. JSON columns cannot be sorted or searched. `core.SQLTextArray`, `core.SQLInt32Array` and `core.SQLUUIDArray` retain native array semantics. Each schema generator owns a copy of its declarations, so resources over the same Go type can use different storage profiles.

### Quoted values

A bool, integer or float field tagged with encoding/json's `string` option, `json:"id,string"`, reads in responses as a JSON string holding its value: `"9007199254740993"`, not `9007199254740993`. A 64-bit integer then survives a client whose numbers are doubles, as JavaScript's are. Requests give the field in the same form:

- filter operands of `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in` and `$nin` are JSON strings: `{"id":"9007199254740993"}`, `{"id":{"$in":["1","2"]}}`. A flat query parameter's text is that string already (`?id=9007199254740993`). This holds for the primary key and tenant columns even when they are not tagged `parse:"filter"`, and a relation's shorthands (`{"author":"7"}`) compare the target's key in its form;
- merge-patch members and JSON Patch values are JSON strings: `{"id":"9007199254740993"}`, `{"op":"replace","path":"/id","value":"42"}`;
- the string holds the value's JSON text in JSON's own syntax — stricter than encoding/json's quoted decoding, so no `"+1"`, `"01"`, `" 1"` or `"NaN"` — and true or false for a bool. An integer may be any integral spelling (`"1.0"`, `"1e3"`), as parse reads integers anywhere, where encoding/json refuses one. The number then normalizes as any of the field's numbers do: exactly, range-checked against its Go kind and its `min` / `max` bounds;
- a bare JSON number is refused with E2073 (filters) or E2077 (patches), naming the expected form, as encoding/json refuses to decode one into the field. A bool field also takes a bare JSON boolean, which is the same value;
- a JSON Patch `test` compares canonical text, so `"5.0"` tests equal to a stored 5, and its value meets the field's bounds as a replace's does. A `copy` or `move` between a quoted and an unquoted field is refused: the value would not decode into the other.

Code passes Go values: `ParseFilterMap`, `MergePatchMap`, `ServerMergePatchMap` and `ValidateOperations` take the field's Go number (`int64(5)`) as well as its quoted text, while a `json.Number` there is a decoded bare JSON number and is refused. Policy constraints and resolved memberships take Go values too. SQL binds the number and `filter.Evaluate` compares it, so PostgreSQL, SQLite and memory backends select the same rows (`filtertest.RunQuoted`). The schema document marks each such field and patch path `"wire": "string"`; `parse/client` sends them as `client.Quoted[T]` and the TypeScript SDK validates them as strings, checking a patch value's integer kind and bounds exactly.

encoding/json ignores the option on a type that marshals itself (`json.Marshaler`, `encoding.TextMarshaler`, on the value or, for the addressable values a page encodes, its pointer), and so does parse. On a string field, which encoding/json quotes a second time, the option changes nothing parse reads. Inside a JSON document a quoted member is stored as text that a number or bool path cannot compare, so a path onto one is refused: one derived from struct tags, one declared in the `paths` or `elements` tag, a sort path, and one `Resource.WithPaths` adds. Drop the option, or declare the member a string. `filter.ResourceSchema.WithPaths`, which has no Go types, cannot tell.

## Parser and generator options

`Option`s build a `*ParserConfig` (start from `DefaultConfig()`); `parse.NewResource` accepts them through `parse.ParserOptions`.

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxDepth(n)` | int | `5` | Filter boolean nesting, JSON filter document nesting, patch JSON Pointer segments |
| `WithMaxCost(n)` | int | `50` | Estimated filter cost: about 1 per indexed condition, 10 per unindexed one, more for documents, relations, unanchored patterns and `$or`; a `$in` / `$nin` set costs one condition whatever its size |
| `WithMaxInputSize(n)` | int | `65536` | Filter / patch body bytes; `0` or less is unlimited. A filter is bounded by it in a query string and in a POST list body alike |
| `WithMaxListBodySize(n)` | int | `0`: derived, `MaxInputSize` + `16384` (`81920` at the default input size), or `1048576` when `MaxInputSize` is unlimited | Bytes of a list request sent as a POST body ([`Resource.ParseListBody`](http-errors.md#the-same-request-as-a-post-body)); a body over it is E2063, read no further. A list body is always bounded, since nothing in front of a handler bounds a body as header limits bound a URL; `NewResource` refuses a negative size. The derived headroom holds the members beside the filter: a cursor (at most 4096 bytes), sort, search, name lists and the JSON around them. Set it explicitly to at least `MaxInputSize` plus that room, or a filter a GET carries is refused in a body. `(*ParserConfig).ListBodyLimit` returns the effective bound |
| `WithMaxConditions(n)` | int | `20` | Filter leaf conditions |
| `WithMaxArraySize(n)` | int | `100` | `$in` / `$nin` members, array `$contains` elements, values in a JSON filter document. A set or an array `$contains` list binds as one parameter whatever its size, so this alone bounds it; raise it with `WithMaxInputSize` for endpoints filtering by large sets |
| `WithMaxPatchCopyBytes(n)` | int64 | `1048576` | Bytes a patch's `copy` operations may add during typed apply |
| `WithCodec(c)` | `Codec` | `StdlibJSON` (numbers decode as `json.Number`, trailing data rejected) | Decodes request bodies and undeclared documents in typed apply; declared JSON storage uses fresh Go JSON decoding. Cursors use Parse's internal bounded MessagePack envelope |
| `WithClock(fn)` | `func() time.Time` | `time.Now().UTC` | Anchor for relative-time tokens (`now-7d`) |
| `WithGeneratorOptions(o...)` | `...GeneratorOption` | none | Schema generation options used by typed parsers |

`GeneratorOption`s configure a `SchemaGenerator` (`NewSchemaGenerator`, `parse.NewSchemaRegistry`, or `WithGeneratorOptions`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithSQLTableName(name)` | string | `PluralSnake(type name)` | Table name for the type |
| `WithTableConvention(fn)` | `TableConvention` | `PluralSnake` | Table naming |
| `WithColumnConvention(fn)` | `NamingConvention` | `SnakeCase` | Column naming when a field has no column tag |
| `WithColumnMapper(m)` | `SQLColumnMapper` | none | Maps a field's column to the SQL expression that reads it. `MapColumn(table, column, type)` gets the column the `db` tag or the column convention names (the one patch writes), the same in every view of the field; the mapper's SQL is trusted as written |
| `WithColumnTypes(types)` | `map[string]SQLType` | none | Copied physical column declarations; authoritative JSON/native-array profile for filter, patch, sort and search schemas |
| `WithFieldNameTag(tag)` | string | `"json"` | Tag holding external field names |
| `WithColumnTag(tag)` | string | `"db"` | Tag holding column names |
| `WithSortMaxFields(n)` | int | `3` | Sort fields a request may name |
| `WithSearchParam(p)` | string | `"search"` | Search parameter a search schema advertises (`filter.ParamSearch`) |
| `GeneratorOptions.DefaultFilterOps` | `map[FieldType][]string` | filter package defaults | Default operators per type; each must apply to the type (`filter.OperatorsFor`) |
| `GeneratorOptions.DefaultPatchOps` | `map[FieldType][]string` | patch package defaults | Default patch operations per type |

Table names and columns must be SQL identifiers; `TableName`, `ColumnExpr` and `ColumnName` return an error otherwise, so a bad tag fails at schema generation. `ColumnExpr` leaves the column unchecked when a `ColumnMapper` renders it.

`WithFieldNameTag` changes schema field names. Direct typed application uses those names. Document operations also require a codec whose field naming agrees: using a non-JSON field tag with the built-in JSON codec is rejected when names disagree. The option does not teach `encoding/json` a different struct-tag convention.

## Value model

Request documents (JSON filters, JSON Patch and merge patch bodies) decode with `core.StdlibJSON`: the standard library's JSON on `encoding/json/v2` with encoding/json's data model, refusing a member named twice, invalid UTF-8 and data after the document (E2086), and keeping every number's literal (`json.Number`).

| Function | Canonical result |
|---|---|
| `NormalizeInt(v, kind)` | `int64` in the signed kind's range (`reflect.Invalid`: the int64 range); `json.Number`, every Go integer kind, and integral floats within ±2^53 accepted; an unsigned kind is an error |
| `NormalizeUint(v, kind)` | `uint64` in the unsigned kind's range (`reflect.Invalid`: the uint64 range), negatives refused; otherwise as `NormalizeInt` |
| `NormalizeFloat(v, kind)` | Finite `float64`; float32 values are range-checked and rounded to the Go destination precision before binding |
| `NormalizeScalar(type, kind, v)` | Per `FieldType`: exact numbers, canonical decimal text, canonical lowercase UUID string, RFC 3339 UTC timestamp of fixed width (UTC years 0001 to 9999), string, bool, and the canonical text of a date, time, interval, inet, cidr or macaddr |
| `NormalizeJSON(v)` | A generic document (`map[string]any`, `[]any`, scalars) with `int64` / `uint64` / `float64` numbers; integers outside the supported range and decimal literals that change on float64 decimal round-trip are rejected |
| `CompareNumbers(a, b)` | Comparison by value without allocating: an integer exactly however it is written (`9007199254740993`, `"9007199254740993.0"`, `"9.007199254740993e15"`), against a float as the float's exact value; a fractional literal, or an integer literal beyond 64 bits, as its nearest float64. `IsNumber(v)` is true for the same types: `json.Number` and every Go integer or float kind, named types included |
| `DecimalText(v)` | A decimal's canonical text: plain notation, no exponent, no leading or trailing zeros, `"0"` for either zero; from a JSON number, a numeric string, a Go number, a `core.Decimal` or a decimal text marshaler; at most `MaxDecimalDigits` (1000) digits |
| `CompareDecimals(a, b)` | Exact decimal order of any two values `DecimalText` accepts |
| `ParsePointer` / `FormatPointer` / `EscapePointerToken` | RFC 6901 paths (`~0`, `~1` escapes) |
| `TypeOf(t)` | `TypeInfo{Type, Elem, NumberKind, SQLType}` — Go-only classification; `SQLType` is empty, slices of scalars are arrays, maps/`any`/`json.RawMessage` and other slices are JSON, structs are objects |
| `SchemaGenerator.FieldType(field)` | Go classification combined with declared physical `SQLType`; JSON scalar/slice fields become documents, typed objects retain members, and native arrays remain arrays |

## Schema discovery

`parse.NewSchemaRegistry` generates schemas for a set of resources. Register each type with `parse.Register[T](registry)` or `parse.MustRegister[T](registry)`. `SchemaFor(name)` returns an independent snapshot of discovery metadata; `ResourceNames` lists registrations. The registry does not execute queries or install your resource handlers. See [HTTP and errors](http-errors.md) for routing and [TypeScript integration](integrations.md#typescript-client) for consuming the schema.

## Request-specific options

Parser limits, the codec, the clock and schema-generation options are `core.Option` / `core.GeneratorOption` values, listed above; `NewResource` passes its resolved parser config to every sub-parser. The root package adds one option:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `parse.WithPatchPolicy(pc, subject)` | `parse.PatchOption` | none | Applies `patch.PatchPolicyConstraints` inside `ParsePatch`, `ParseMergePatch` and `ApplyPatch` |

Builder options (`filter.List*`, `filter.With*`, `patch.With*`) are in [queries](queries.md#configuration) and [patches](patches.md#configuration).

## Per-call filter cost

`Resource.ParseFilterWithCost(query, maxCost)` applies a positive server-selected budget to one filter parse. It preserves the Resource's schema, codec, clock and other limits without mutating shared configuration. Use `ParseFilter` and `ParseRequestFilter` for untrusted requests under the configured request budget; never accept a larger cost limit from request input. Stored filters can use a separately approved server-policy budget.

## Civil dates

Use `core.Date` (or `*core.Date` for a nullable column) for SQL DATE values. It is a calendar value with exactly `YYYY-MM-DD` text, JSON, filter and cursor representations. It supports equality and ordering without converting the date to an instant. TIMESTAMPTZ fields continue to use `time.Time` and RFC3339 timestamps ([timestamps](#timestamps)).

```go
type Task struct {
    ID string `json:"id" db:"id" parse:"filter,sort"`
    DueOn *core.Date `json:"due_on" db:"due_on" parse:"filter,patch,sort"`
    DueAt *time.Time `json:"due_at" db:"due_at" parse:"filter,patch,sort"`
}
```

`core.ParseDate("2028-02-29")` validates a value. Invalid calendar dates, timestamp strings, relative-time tokens and null for a non-nullable date are refused. A nullable patch can explicitly clear a date; omission leaves it untouched. `Date.Scan` and `Date.Value` provide native database/sql and pgx interoperability; native DATE values retain their calendar components in every session timezone. The zero Date is invalid. `Date.Time()` is an explicit conversion for an internal API that requires UTC-midnight `time.Time`; it does not change the public date representation.

`core.Date` infers the SQL DATE read rule; an explicit `ColumnType("day", core.SQLDate)` requires the same scalar. SQLite reads and compares calendar components of both canonical dates and legacy midnight-timestamp storage without rewriting rows or applying offsets. Its date expressions may require expression indexes for large date-range workloads. Request values and JSON responses remain date-only.

## Timestamps

A `time.Time` field (`*time.Time` for a nullable column) holds an instant. Its canonical text is `core.FormatTimestamp`: RFC 3339 in UTC with exactly nine fractional digits (`2026-01-02T01:04:05.500000000Z`, `core.TimestampLayout`). Filter values normalize to it, whatever offset a request writes (`2026-01-02T03:04:05.5+02:00` is the same instant), and cursors, patch values, update stamps and facet keys carry it. A request writes a timestamp in RFC 3339's form exactly: two-digit fields, a fraction of at most nine digits after a dot, and `Z` or an offset within ±23:59; and its instant falls in the UTC years 0001 to 9999, which keeps the canonical text's width fixed. Anything else, a `time.Time` outside those years included, is E2073 in a filter and E2077 in a patch.

- **PostgreSQL**: declare the column `TIMESTAMPTZ`, which compares instants. A terms facet keys it as its canonical text through `to_char(… AT TIME ZONE 'UTC', …)`, so the keys do not depend on the session's `DateStyle` or `TimeZone`. PostgreSQL holds microseconds, so a key's last three fractional digits are zero, and an infinite timestamp has no bucket. A `timestamp` column without a time zone holds no instant: its facet keys would follow the session's `TimeZone`.
- **SQLite** has no timestamp type, so a timestamp column holds the canonical text. The zone is UTC and the width fixed, so text order is time order: ranges, sorts, keyset seeks and facets on the column agree with PostgreSQL, and filters and sorts compare the column itself, with no expression to index. Declare it `DATETIME` or `TIMESTAMP` to scan it into a `time.Time`: modernc.org/sqlite parses the canonical text in a column of either type, and returns a `TEXT` column's as a string. `SQLiteDialect.MarshalValue` writes that text from a `time.Time`, a pointer to one, a `sql.NullTime` or RFC 3339 text, and each element of a list of timestamps (`[]time.Time`, stored as JSON text) the same way; filters, cursors, patches and stamps bind it. Rows the application writes itself must hold it too: bind `core.FormatTimestamp(t)`, or `MarshalValue`'s result. modernc.org/sqlite stores a `time.Time` bound directly as `t.String()` in the value's own offset, which neither compares nor facets as an instant.

To migrate a SQLite database whose timestamps were written another way, rewrite each one as the canonical text of its instant. In Go: read it into a `time.Time` (modernc.org/sqlite parses its own text in a column declared `DATE`, `DATETIME` or `TIMESTAMP`) and write back `core.FormatTimestamp(t)`. Until a column is rewritten, rows written in different offsets compare, sort and facet by their text, not their instant.

## Time, interval and network values

Five value types give SQL types that Go would otherwise hold as a `string` one canonical text each, as `core.Date` does for DATE. Request values, patches, JSON, SQL parameters and cursors use the canonical text, so both backends hold the same text and a filter compares the same values on each. Use a pointer for a nullable column.

| SQL type | Go type | Canonical text | Input accepted | Default operators |
|---|---|---|---|---|
| `time` | `core.LocalTime` | `HH:MM:SS`, then `.` and 1–6 digits without trailing zeros (PostgreSQL's output); text order is time order | The same, with up to 9 fractional digits when those past the sixth are zeros. `24:00:00`, a leap second and an offset are refused | `$eq` `$ne` `$gt` `$gte` `$lt` `$lte`; sortable |
| `interval` | `core.Interval` (`Months`, `Days`, `Microseconds`, as PostgreSQL holds it) | ISO 8601, as PostgreSQL's `iso_8601` IntervalStyle writes it: `P1Y2M3DT4H5M6.5S`, `P1DT-1H`, `PT0S` | ISO 8601 designators `P[nY][nM][nW][nD][T[nH][nM][nS]]`, each field signed, a fraction on seconds only | `$eq` `$ne` `$in` `$nin` |
| `inet` | `core.Inet` (a `netip.Prefix`, host bits kept) | The address as `net/netip` writes it, with `/bits` only when shorter than the address | Any address or address/bits `netip` parses; a zone is refused | `$eq` `$ne` `$in` `$nin` |
| `cidr` | `core.CIDR` | `address/bits` | address/bits with no host bits set | `$eq` `$ne` `$in` `$nin` |
| `macaddr` | `core.MACAddr` | Six lowercase colon-separated octets | The six-octet forms `net.ParseMAC` reads | `$eq` `$ne` `$in` `$nin` |

```go
type Session struct {
    ID         string          `json:"id" db:"id" parse:"filter,sort"`
    StartsAt   *core.LocalTime `json:"starts_at" db:"starts_at" parse:"filter,patch,sort"`
    Idle       *core.Interval  `json:"idle" db:"idle" parse:"filter,patch"`
    ClientIP   *core.Inet      `json:"client_ip" db:"client_ip" parse:"filter,patch"`
    AllowedIPs []core.Inet     `json:"allowed_ips" db:"allowed_ips" parse:"filter,patch"`
}
```

Equality and sets compare values, which the canonical text names one-to-one: `192.168.0.5` and `192.168.0.5/24` are different inet values, as in PostgreSQL. PostgreSQL's own interval equality treats a month as 30 days and a day as 24 hours, so an interval filter compares the canonical text there, field by field, as SQLite does: `P30D` does not match `P1M`. Text order is not the order of an interval or an address, so interval, inet, cidr and macaddr offer no range operator and cannot be sorted. An interval, inet or cidr field cannot be a facet: PostgreSQL's text of those values is not the canonical text, so the backends would key its buckets differently. A time or macaddr field can. A slice of a value type is a native array (`INET[]`); its element conditions take the element's operators.

Each type's `Scan` reads the text drivers return: PostgreSQL's output, pgx's text (`10:30:00.500000`, an inet host as `/32`, an interval as `14 mon 3 day 04:05:06.500000`), the server's default `postgres` IntervalStyle and `abbrev()`'s IPv4-compatible dotted form, and the canonical text SQLite holds. An `Interval` column reads as its ISO 8601 text on PostgreSQL under any IntervalStyle: it implies `ColumnType(column, core.SQLInterval)`. A column of a value type may declare only the storage that type reads through (`SQLInet` for an `Inet`, `SQLCIDR` for a `CIDR`, `SQLInterval` for an `Interval`, the array types for their slices) or a JSON type. An infinite PostgreSQL interval has no ISO 8601 form and is refused on read.

## Decimals

A decimal field holds an exact number: PostgreSQL `numeric`, SQLite `NUMERIC` or `REAL`. Its Go type is any named type whose value or pointer has the method set `Coefficient() *big.Int` and `Exponent() int32` (`core.Decimal`) — `github.com/shopspring/decimal`'s `Decimal` and `*Decimal` among them. The type is recognized by that shape, so parse's core depends on no decimal library. A decimal library type with another method set declares its column `storage:"scalar:decimal"`; a document member declares itself in `paths` (`price:decimal`).

```go
type Invoice struct {
    ID    string           `json:"id"    db:"id"    parse:"filter,sort"`
    Total decimal.Decimal  `json:"total" db:"total" parse:"filter,patch,sort"`
    Tip   *decimal.Decimal `json:"tip"   db:"tip"   parse:"filter,patch,sort"`
}
```

A request writes a decimal as a JSON number or as a string in the JSON number grammar (`"10.50"`, `"1.5e2"`; shopspring marshals a string). Hexadecimal, a leading `+` or `.`, a trailing `.`, leading zeros, NaN and infinities are refused: E2073 in a filter, E2077 in a patch. Every accepted value normalizes to one canonical text (`core.DecimalText`), so `"10.50"`, `10.5` and `"1.05e1"` are all `"10.5"`. A value with more than `core.MaxDecimalDigits` (1000, PostgreSQL's limit on a declared precision) digits in plain notation is refused, so a short exponent literal cannot expand into a million-digit bound value.

Filters take the numeric operators (default `eq ne gt gte lt lte in nin`; the NULL tests when declared in `filter_ops`). Comparison is exact and by value on every backend, never by text: SQL compares it as the dialect's exact decimal (`Dialect.DecimalColumn`, `DecimalParam`, `DecimalOrder`: `numeric` on PostgreSQL; on SQLite the functions [`parsesqlite`](../parsesqlite/README.md) registers), a memory backend compares with `core.CompareDecimals`, and a keyset cursor carries the canonical text, so `9.5` sorts and seeks before `10.25`. A patch replaces a decimal with its canonical text, which the column parses; patch policy `Min`, `Max` and allowed and denied values compare a decimal path by value.

| Backend | Storage and precision |
|---|---|
| PostgreSQL | `numeric` holds every decimal exactly; a declared scale (`numeric(12,4)`) rounds what it stores, as PostgreSQL does. Facet terms count by `trim_scale(value)::text` (PostgreSQL 13+), so `2` and `2.00` share a bucket |
| SQLite | No decimal type, so parse reads a decimal through two deterministic functions: `parse_decimal`, its canonical text, for equality, sets and facet terms; `parse_decimal_key`, a BLOB that orders as the value, for ranges, sorts, keyset seeks and range facets. Register them with [`parsesqlite.Register`](../parsesqlite/README.md) before the database opens; a connection without them fails the statement (`no such function: parse_decimal`) rather than rounding. Declare a decimal column `TEXT` to hold every decimal exactly: parse writes the canonical text, and reads a JSON number another writer stored (`10.50`, `1.05e1`) by value. A `NUMERIC` or `REAL` column converts text to REAL beyond 64-bit integers, keeping 15 significant digits: parse compares exactly what it holds, which the column has already rounded |
| Memory | `core.CompareDecimals` over `core.DecimalText`; `filter.KeyValue` canonicalizes a decimal key from any form its column scans to (text, INTEGER, REAL, a `core.Decimal`) |

A slice of decimals stays a `json` document: its elements' wire form is a string each, which no native array column holds as numbers. A decimal path member is a JSON number, or a JSON string in plain notation (`"9.50"`, not `"1e3"`) of at most 1000 characters; anything else is absent ([JSON paths](queries.md#json-paths)).
