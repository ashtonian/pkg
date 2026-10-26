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
| `patch_ops` | `"replace,remove"` | patch | Allowed operations; `remove` requires a nullable Go type |
| `enum` | `"active,offline"` | filter, patch | Allowed string values (per element on array fields) |
| `indexed` | `"true"` | filter | Cost hint: the field is indexed |
| `immutable` | `"true"` | filter | Advertised in the schema response |
| `sort_default` | `"desc"` | sort | The default sort field and direction |
| `relation` | `"device_tags,device_id,tag_id[,pk[,tenant_col]]"` | filter, patch, facet, include | A virtual relation field: the table holding the pairs, its owner and target columns (see Relations); every part must be an identifier |
| `relation_name` | `"tag_ids"` | hidden relation fields | Explicit external name when a virtual relation is hidden by the field-name tag (for example `json:"-"`); preserves acronym spellings. Ordinary visible field names are unchanged |
| `facet` | `"terms:20"`, `"range:0-50,50-100,100+"` | facet | Facet aggregation |
| `search_weight` | `"A"` … `"D"` | search | Full-text weight |
| `elements` | `"by:string;priority:int"` | filter | Typed members of each element of a JSON array, filtered together with `$some` / `$none` ([array elements](queries.md#array-elements)); a slice-of-struct document derives them from its element type's members tagged `parse:"filter"` |
| `paths` | `"color:string:filter+sort+facet;size:int:filter+indexed;dims.w:float"` | filter, sort, facet | Typed members of a map / `any` document, filterable (with `sort` sortable, with `facet` a terms facet) as `<field>.<member>` ([JSON paths](queries.md#json-paths)); struct documents derive theirs from members tagged `parse:"filter"` / `parse:"sort"` / `parse:"facet"` |
| `storage` | `"jsonb"` (default), `"json"`, `"text"`, `"scalar:float"` | filter | How a document column holds its JSON; `scalar:<type>` marks a struct the database holds as that scalar (no paths) |
| `geo` | `"jsonb"`, `"geography"`, `"geometry"` | filter | Geo field storage |
| `geo_srid` | `"3857"` | filter | SRID of a native `geometry` column (jsonb / geography are 4326) |
| `geo_lat`, `geo_lon` | `"$.latitude"` | filter | Members of a JSONB point |
| `label`, `description`, `group`, `order` | `"Name"` | schema response | UI metadata |

### Fields and embedding

Every schema, the resource's columns, its cursors and its patch paths come from one walk of the type (`core.StructFields`), so they always agree. The walk sees the fields encoding/json writes. A struct embedded without a field-name tag has its fields promoted, through any depth, through pointers and through unexported struct types, so a server type can embed its wire type and keep every filter, sort and patch field. Fields `json:"-"` hides stay in the walk for columns and server-only paths.

A name two fields claim goes to the field encoding/json writes: the shallower one, and at one depth the one whose tag names it. So a server type can shadow a field of the wire type it embeds, with another column or a narrower type, and requests reach the outer field.

Schema generation refuses the shapes encoding/json resolves silently:

- two promoted fields with one name at the same depth, both tagged or both untagged (encoding/json drops both);
- an embedded struct hidden whole by `json:"-"` (hide its fields one by one);
- an unexported embedded struct named by the field-name tag;
- two fields with one column.

A field's data type comes from its Go type (`core.TypeOf`): string / int / float / bool / timestamp / uuid scalars, a UUID being any named 16-byte array that marshals to text (the standard library's `uuid.UUID`, `github.com/google/uuid`, `github.com/gofrs/uuid`; a bare `[16]byte` is binary); slices of those are native `array`s (PostgreSQL arrays, JSON text on SQLite); maps, `any`, `json.RawMessage` and other slices are `json` documents (`jsonb`); structs are `object` documents.

Declare physical storage with `parse.ColumnType(column, core.SQLJSON)` / `core.SQLJSONB`, or `core.WithColumnTypes(map[string]core.SQLType{...})` for a lower-level parser. Column names follow the configured column tag/mapper, including hidden and unread fields. Declared JSON strings, bytes and scalar slices use document operators and JSON binding; typed objects retain their member schemas. JSON columns cannot be sorted or searched. `core.SQLTextArray`, `core.SQLInt32Array` and `core.SQLUUIDArray` retain native array semantics. Each schema generator owns a copy of its declarations, so resources over the same Go type can use different storage profiles.

## Parser and generator options

`Option`s build a `*ParserConfig` (start from `DefaultConfig()`); `parse.NewResource` accepts them through `parse.ParserOptions`.

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxDepth(n)` | int | `5` | Filter boolean nesting, JSON filter document nesting, patch JSON Pointer segments |
| `WithMaxCost(n)` | int | `50` | Estimated filter cost: about 1 per indexed condition, 10 per unindexed one, more for documents, relations, unanchored patterns and `$or` |
| `WithMaxInputSize(n)` | int | `65536` | Filter / patch body bytes |
| `WithMaxConditions(n)` | int | `20` | Filter leaf conditions |
| `WithMaxArraySize(n)` | int | `100` | `$in` / `$nin` members, array `$contains` elements, values in a JSON filter document |
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
| `WithColumnMapper(m)` | `SQLColumnMapper` | none | Full control over column expressions; the mapper's SQL is trusted as written |
| `WithColumnTypes(types)` | `map[string]SQLType` | none | Copied physical column declarations; authoritative JSON/native-array profile for filter, patch, sort and search schemas |
| `WithFieldNameTag(tag)` | string | `"json"` | Tag holding external field names |
| `WithColumnTag(tag)` | string | `"db"` | Tag holding column names |
| `WithSortMaxFields(n)` | int | `3` | Sort fields a request may name |
| `WithSearchParam(p)` | string | `"search"` | Search parameter a search schema advertises (`filter.ParamSearch`) |
| `GeneratorOptions.DefaultFilterOps` | `map[FieldType][]string` | filter package defaults | Default operators per type; each must apply to the type (`filter.OperatorsFor`) |
| `GeneratorOptions.DefaultPatchOps` | `map[FieldType][]string` | patch package defaults | Default patch operations per type |

Table names and columns must be SQL identifiers; `TableName`, `ColumnExpr` and `ColumnName` return an error otherwise, so a bad tag fails at schema generation.

`WithFieldNameTag` changes schema field names. Direct typed application uses those names. Document operations also require a codec whose field naming agrees: using a non-JSON field tag with the built-in JSON codec is rejected when names disagree. The option does not teach `encoding/json` a different struct-tag convention.

## Value model

Request documents (JSON filters, JSON Patch and merge patch bodies) decode with `core.StdlibJSON`: the standard library's JSON on `encoding/json/v2` with encoding/json's data model, refusing a member named twice, invalid UTF-8 and data after the document (E2086), and keeping every number's literal (`json.Number`).

| Function | Canonical result |
|---|---|
| `NormalizeInt(v, kind)` | `int64` in the signed kind's range (`reflect.Invalid`: the int64 range); `json.Number`, every Go integer kind, and integral floats within ±2^53 accepted; an unsigned kind is an error |
| `NormalizeUint(v, kind)` | `uint64` in the unsigned kind's range (`reflect.Invalid`: the uint64 range), negatives refused; otherwise as `NormalizeInt` |
| `NormalizeFloat(v, kind)` | Finite `float64`; float32 values are range-checked and rounded to the Go destination precision before binding |
| `NormalizeScalar(type, kind, v)` | Per `FieldType`: exact numbers, canonical lowercase UUID string, RFC 3339 UTC timestamp, string, bool |
| `NormalizeJSON(v)` | A generic document (`map[string]any`, `[]any`, scalars) with `int64` / `uint64` / `float64` numbers; integers outside the supported range and decimal literals that change on float64 decimal round-trip are rejected |
| `CompareNumbers(a, b)` | Exact comparison across integer and float representations |
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

Use `core.Date` (or `*core.Date` for a nullable column) for SQL DATE values. It is a calendar value with exactly `YYYY-MM-DD` text, JSON, filter and cursor representations. It supports equality and ordering without converting the date to an instant. TIMESTAMPTZ fields continue to use `time.Time` and RFC3339 timestamps.

```go
type Task struct {
    ID string `json:"id" db:"id" parse:"filter,sort"`
    DueOn *core.Date `json:"due_on" db:"due_on" parse:"filter,patch,sort"`
    DueAt *time.Time `json:"due_at" db:"due_at" parse:"filter,patch,sort"`
}
```

`core.ParseDate("2028-02-29")` validates a value. Invalid calendar dates, timestamp strings, relative-time tokens and null for a non-nullable date are refused. A nullable patch can explicitly clear a date; omission leaves it untouched. `Date.Scan` and `Date.Value` provide native database/sql and pgx interoperability; native DATE values retain their calendar components in every session timezone. The zero Date is invalid. `Date.Time()` is an explicit conversion for an internal API that requires UTC-midnight `time.Time`; it does not change the public date representation.
