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
| `enum` | `"active,offline"`, `"'net 30, eom',' padded'"` | filter, patch | Allowed values of a string field, or of each element of an array of strings, a struct member's path included. Plain values are comma-separated and trimmed; a value in single quotes keeps its commas and edge spaces, a doubled quote inside it is one quote (`'it''s'`), and `''` is the empty string. An empty plain value, an unterminated quote, or an enum on any other type fails generation |
| `minlen`, `maxlen` | `"3"` | patch | Fewest and most characters of a string, inclusive (per element on array fields). Characters are Unicode code points, as a `CHAR(n)` / `VARCHAR(n)` column and `char_length` count them. A non-negative integer; `minlen` above `maxlen`, or either on a field that is not a string or an array of strings, fails schema generation |
| `min`, `max` | `"0.01"`, `"1e6"` | patch | Least and greatest number, inclusive (per element on array fields). A decimal literal compared exactly with an int or decimal value, and at the field's precision with a float (`max:"0.1"` admits a `float32` field's 0.1). `min` above `max`, or either on a field that is not a number or an array of numbers, fails schema generation |
| `minitems`, `maxitems` | `"1"`, `"8"` | patch | Fewest and most elements of an array, inclusive, as PostgreSQL's `cardinality` counts them. Checked on an array a `replace` or `add` sets whole; an element added, appended or removed is left to the store. A slice field with `minitems` above zero is not nullable: null and `remove` would empty it, so the schema drops `remove` and refuses null (E2077), while a pointer to a slice still clears to NULL. A non-negative integer; `minitems` above `maxitems`, either on a field that is not an array, or on a relation, fails schema generation |
| `indexed` | `"true"` | filter | Cost hint: the field is indexed. `"true"` or `"false"`; any other value fails schema generation |
| `immutable` | `"true"` | filter | The field is never written by a request: a field with the `patch` capability cannot be immutable, which fails schema generation, so no request patch path writes it. A `server` field may be immutable; the server's own write is not a request's. Advertised in the schema response. `"true"` or `"false"`; any other value fails schema generation |
| `sort_default` | `"desc"` | sort | The default sort field and direction, `asc` or `desc`; on one sortable field at most. Any other value, a second field, or a field without `sort` fails generation |
| `relation` | `"device_tags,device_id,tag_id[,pk[,tenant_col]]"`, `"stock_tags,warehouse_id+sku,tag_id,warehouse_id+sku"`, `"stock_tags,tag_id,warehouse_id+sku"` | filter, patch, facet, include | A virtual relation field: the table holding the pairs, its owner and target columns (see Relations); every part must be an identifier. A key of several columns joins them with `+`: an owner key in the owner and `pk` parts, paired by position, a target key in the target part, whose columns the struct the field's slice holds types ([pair tables](pair-tables.md)) |
| `relation_name` | `"tag_ids"` | hidden relation fields | Explicit external name when a virtual relation is hidden by the field-name tag (for example `json:"-"`); preserves acronym spellings. Ordinary visible field names are unchanged |
| `facet` | `"terms:20"`, `"range:0-50,50-100,100+"` | facet | Facet aggregation, on a field (or struct member) with the `facet` capability. `terms[:<limit>]` counts values of a type every backend keys alike: string, bool, int, float, decimal, date, timestamp, time, uuid, macaddr, or an array of one; the limit is at least 1 and has no cap, as a tag is not request input (a request's own limit is). `range:<lo>-<hi>,…,<lo>+` buckets a scalar int, float or decimal field: each range holds values from its minimum, inclusive, to its maximum, exclusive; bounds are finite, each minimum below its maximum, and no two ranges overlap. Anything else fails generation, and `BuildFacetQuery` refuses the same on a hand-built schema |
| `elements` | `"by:string;priority:int"` | filter | Typed members of each element of a JSON array, filtered together with `$some` / `$none` ([array elements](queries.md#array-elements)); a slice-of-struct document derives them from its element type's members tagged `parse:"filter"` |
| `paths` | `"color:string:filter+sort+facet;size:int:filter+indexed;dims.w:float;tags:[]string"` | filter, sort, facet | Typed members of a map / `any` document, filterable (with `sort` sortable, with `facet` a terms facet) as `<field>.<member>` ([JSON paths](queries.md#json-paths)); `[]<type>` declares an array of that type, filtered by `$contains` and its elements by `$some` / `$none` ([array elements](queries.md#array-elements)), never sorted, faceted by terms only; struct documents derive theirs from members tagged `parse:"filter"` / `parse:"sort"` / `parse:"facet"` |
| `storage` | `"jsonb"` (default), `"json"`, `"text"`, `"scalar:decimal"` | filter, sort | How a document column holds its JSON; `scalar:<type>` marks a struct the database holds as that scalar — `string`, `int`, `float`, `decimal`, `bool`, `timestamp` or `uuid` — which filters and sorts as one (no paths) |
| `geo` | `"jsonb"`, `"geography"`, `"geometry"` | filter | Geo field storage |
| `geo_srid` | `"3857"` | filter | SRID of a native `geometry` column (jsonb / geography are 4326) |
| `geo_lat`, `geo_lon` | `"$.latitude"` | filter | Members of a JSONB point |
| `label`, `description`, `group`, `order` | `"Name"` | schema response | UI metadata; `order` is an integer, and any other value fails generation |
| `json:",string"` | `json:"id,string"` | filter, patch | A bool, integer or float column travels as a JSON string holding its value, as encoding/json writes it ([quoted values](#quoted-values)) |

Schema generation refuses a key on a walked field, hidden fields included, that parse does not read but that misspells one of the keys above: one equal to it ignoring case, `-` and `_` (`filterOps`, `minItems`), or one inserted, deleted, replaced or swapped character from it (`filter_op`, `ptach_ops`). Parse would otherwise ignore it without a word, and a dropped `patch_ops` or `maxlen` widens what requests may do. The error names the field, the key written and the key meant. The field-name and column tags are not compared, as other libraries' keys sit one character from them (`bson` beside `json`). A key another library reads that is near one of parse's, such as a router's `path` beside `paths`, is left alone once named with `core.WithForeignTagKeys`.

### Fields and embedding

Every schema, the resource's columns, its cursors and its patch paths come from one walk of the type (`core.StructFields`), so they always agree. The walk sees the fields encoding/json writes. A struct embedded without a field-name tag has its fields promoted, through any depth, through pointers and through unexported struct types, so a server type can embed its wire type and keep every filter, sort and patch field. Fields `json:"-"` hides stay in the walk for columns and server-only paths, at any depth: having no name, a hidden field is never shadowed.

A name two fields claim goes to the field encoding/json writes: the shallower one, and at one depth the one whose tag names it. So a server type can shadow a field of the wire type it embeds, with another column or a narrower type, and requests reach the outer field.

Schema generation refuses the shapes encoding/json resolves silently:

- two promoted fields with one name at the same depth, both tagged or both untagged (encoding/json drops both);
- an embedded struct hidden whole by `json:"-"` (hide its fields one by one);
- an unexported embedded struct named by the field-name tag;
- two fields with one column, hidden fields included, at one depth or two.

A field promoted through an unexported embedded pointer is promoted for names, filters and sorts, but nothing fills it: a row scan fills a fresh row, where the pointer is nil, a patch writes a copy, where the caller's row shares it, and reflection can neither allocate that pointer nor point it at a copy. So `NewResource` refuses a column it reads whose field is promoted that way, a computed column (`ReadExpr`) included, naming the column, the field and the embedded type, and schema generation refuses `parse:"patch"` or `parse:"server"` on such a field ([typed document behavior](patches.md#typed-document-behavior)). Export the embedded type, or embed it by value: reflection sets the fields an unexported struct embedded by value promotes, as encoding/json does. A column left out of `ReadColumns` is never scanned, so a resource can keep such a field as long as it neither reads nor patches it.

A stored document's members are named as encoding/json names them, by the `json` tag alone (`core.JSONFieldsOf`): the document type's other tags are not read, a struct it embeds with `json:"-"` holds nothing, a struct embedded inside itself contributes its fields once, an unexported embedded struct its tag names is a member, and a name two of its fields claim at one depth belongs to neither. A document type with a tag reflect cannot read whole is refused, as a row type is. Reading a row checks each JSON column's member names against its Go type in one pass: a struct member spelled in another case, which decoding writes to a field it names (the error names that field: of the fields whose names differ from the member only in case, the shallowest, then the first declared; a `case:strict` field is matched only exactly, so its other spellings are unknown members, which decoding drops), a map key that does not read back as written (`"01"` for an integer key, `"0:0:0:0:0:0:0:1"` for a `netip.Addr`) and a member given twice are refused. Merge patches name a document's members the same way, so a document type held in a map, a pointer, a slice or an array works wherever it sits. A document column's own struct type and element type are also walked with the rules above when the resource is built, for the member capabilities their `parse` tags grant. A document type holding, at any depth (through pointers, slices, arrays and map values), a member promoted through an unexported embedded pointer is refused when the resource reads its column or a patch path writes it: encoding/json writes that member while the pointer is set, and cannot decode it into the fresh value a scan or a patch decodes into. The error names the column or field, the document type, the member's field and the embedded type. A type with its own JSON methods is not looked into, and the column of an `sql.Scanner`, which decodes the driver's value itself, is refused only when `ScanJSON` names it (`RowScanner`).

A field's data type comes from its Go type (`core.TypeOf`): string / int / float / decimal / bool / timestamp / date / uuid scalars, the [time, interval and network value types](#time-interval-and-network-values), a UUID being any named 16-byte array whose zero value marshals to the nil UUID's text (the standard library's `uuid.UUID`, `github.com/google/uuid`, `github.com/gofrs/uuid`), while a named 16-byte array that marshals to other text, such as a ULID or a hexadecimal hash, is a string compared, sorted and paged by that text, stored as text, and a bare `[16]byte` is binary and a decimal any named type with the `core.Decimal` method set ([decimals](#decimals)); slices of those other than decimals are native `array`s (PostgreSQL arrays, JSON text on SQLite); maps, `any`, `json.RawMessage` and other slices are `json` documents (`jsonb`); structs are `object` documents.

Declare physical storage with `parse.ColumnType(column, core.SQLJSON)` / `core.SQLJSONB`, or `core.WithColumnTypes(map[string]core.SQLType{...})` for a lower-level parser. Column names follow the configured column tag/mapper, including hidden and unread fields. Declared JSON strings, bytes and scalar slices use document operators and JSON binding; typed objects retain their member schemas. JSON columns cannot be sorted or searched. `core.SQLTextArray`, `core.SQLInt32Array` and `core.SQLUUIDArray` retain native array semantics. Each schema generator owns a copy of its declarations, so resources over the same Go type can use different storage profiles.

### Quoted values

A bool, integer or float field tagged with encoding/json's `string` option, `json:"id,string"`, reads in responses as a JSON string holding its value: `"9007199254740993"`, not `9007199254740993`. A 64-bit integer then survives a client whose numbers are doubles, as JavaScript's are. Requests give the field in the same form:

- filter operands of `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in` and `$nin` are JSON strings: `{"id":"9007199254740993"}`, `{"id":{"$in":["1","2"]}}`. A flat query parameter's text is that string already (`?id=9007199254740993`). This holds for the primary key and tenant columns even when they are not tagged `parse:"filter"`, and a relation's shorthands (`{"author":"7"}`) compare the target's key in its form;
- merge-patch members and JSON Patch values are JSON strings: `{"id":"9007199254740993"}`, `{"op":"replace","path":"/id","value":"42"}`. So are the keys of a writable relation whose target's key is quoted, in a merge patch and in `replace`, `add` and `remove` alike: `{"tags":["5","9007199254740993"]}`. A relation declared with the `relation:"…"` tag has no target resource, so its keys take the owner field's JSON form, where encoding/json ignores the option on a slice;
- the string holds the value's JSON text in JSON's own syntax — stricter than encoding/json's quoted decoding, so no `"+1"`, `"01"`, `" 1"` or `"NaN"` — and true or false for a bool. An integer may be any integral spelling (`"1.0"`, `"1e3"`), as parse reads integers anywhere, where encoding/json refuses one. The number then normalizes as any of the field's numbers do: exactly, range-checked against its Go kind and its `min` / `max` bounds;
- a bare JSON number is refused with E2073 (filters) or E2077 (patches), naming the expected form, as encoding/json refuses to decode one into the field. A bool field also takes a bare JSON boolean, which is the same value;
- a JSON Patch `test` compares canonical text, so `"5.0"` tests equal to a stored 5, and its value meets the field's bounds as a replace's does. A `copy` or `move` between a quoted and an unquoted field is refused: the value would not decode into the other.

Code passes Go values: `ParseFilterMap`, `MergePatchMap`, `ServerMergePatchMap` and `ValidateOperations` take the field's Go number (`int64(5)`) as well as its quoted text, while a `json.Number` there is a decoded bare JSON number and is refused. Policy constraints and resolved memberships take Go values too. SQL binds the number and `filter.Evaluate` compares it, so PostgreSQL, SQLite and memory backends select the same rows (`filtertest.RunQuoted`). The schema document marks each such field and patch path `"wire": "string"`, a relation path's for each key; `parse/client` sends them as `client.Quoted[T]` (a relation's as `client.Relation[client.Quoted[K]]`) and the TypeScript SDK validates them as strings, checking a patch value's integer kind and bounds exactly.

encoding/json ignores the option on a type that marshals itself (`json.Marshaler`, encoding/json/v2's `MarshalerTo`, `encoding.TextMarshaler`, on the value or, for the addressable values a page encodes, its pointer), and so does parse. On a string field, which encoding/json quotes a second time, the option changes nothing parse reads. Inside a JSON document a quoted member is stored as text that a number or bool path cannot compare, so a path onto one is refused: one derived from struct tags, one declared in the `paths` or `elements` tag, a sort path, and one `Resource.WithPaths` adds. Drop the option, or declare the member a string. `filter.ResourceSchema.WithPaths`, which has no Go types, cannot tell.

## Keys

A resource's key is the columns `parse.PrimaryKey` names, in key order: `"id"` alone unless it names others. The columns must be unique together among the table's rows, as a primary key or a unique constraint over exactly them makes them; columns two rows share page one of them out of every list and let an update by key change both. A key of several columns, such as a stock table's `("warehouse_id", "sku")`, serves wherever a one-column key does:

| Where | One column | Several columns |
|---|---|---|
| Lists | The keyset ends on the key column, unless the sort names it | The keyset ends on each key column the sort does not name, in key order and in the sort's direction, so pages are stable under every sort; a cursor minted under another key is refused (E2082) |
| Requests | The key column is a request field, tagged or not | Every key column is a request field |
| A row's key | `Resource.Key(item)` is a `parse.Key` of one value | A `parse.Key` of a value per column, in key order |
| Updates | `ByID(id)`, `ByIDs(ids...)`, or `ByKey` / `ByKeys` with one-value keys | `ByKey(k)`, `ByKeys(ks...)`; a key of another length than the key is `ErrKeyInvalid`, naming both lengths. One key compiles to the AND of its columns' equalities, a set of keys to the row value of the columns `IN` the set, bound as one parameter whatever its size |
| Update batches | `RowUpdate.Key`: one-value keys | `RowUpdate.Key`: a value per column; rows lock in key order, column by column |
| Relations | Each correlation is one column | Each correlation pairs a column with each key column, by position ([keys of several columns](relations.md#keys-of-several-columns)) |
| Schema document | `"key": ["id"]` | `"key": ["warehouse_id", "sku"]` |

A key column is of any scalar type (`core.IsKeyType`): a string, UUID, integer, float, decimal, boolean, date, time of day, timestamp, interval or network value. A document, array, geometry or opaque column has no equality to identify a row by, and `NewResource` refuses it as a key column, naming it. Wherever a key is compared it compares as a filter compares its type, one rule for every statement and for Go:

| Where | How a key compares |
|---|---|
| Updates by key, key sets (`ByKeys`, a relation's `$in`), relation joins, include and facet owner sets, membership lookups | Each column through the operand a filter's equality on its type uses: a decimal by value (`parse_decimal_key` on SQLite, `numeric` on PostgreSQL), so a stored `"1.50"` is the key `1.5` and two 20-digit keys one apart stay apart; a date by its day; a timestamp at its instant. A set binds as one parameter |
| Include and facet keys | The key's canonical text (`core.KeyTextOf`), which the statement writes per row: `1.5` for a stored `1.50`, a timestamp as its fixed-width UTC text, a key of several columns as the JSON array of its columns' texts |
| Go identity (an update batch's duplicate and `CheckCurrent` checks, include grouping) | `core.KeyTextOf` of the normalized key: equal exactly when the database holds the keys equal; `-0` is `0`, NaN and the infinities are no key |
| Update batch lock order | `filter.KeyOrder`, which `core.CompareCanonical` mirrors in Go: decimals and numbers by value, timestamps by instant, strings by code point; an interval or network value, which no sort orders, by its canonical text's code points |

A key normalizes by one rule wherever it binds, `core.NormalizeKey`: a value per key column, each read as the database holds it (`core.StoredValue`: a `driver.Valuer` stands for the value it writes) and normalized as its column's type. An update's targets (`ByKey`, `ByKeys`, `RowUpdate.Key`), `ParseKey`, the owner keys relation statements and includes bind, a relation's members, and key sets all go through it, and a policy's key constraint (`filter.PolicyConstraint.Fields`) reads each position the same way, so a key whose Go value is a codec names the row the driver stored, in each, and a negated key constraint excludes it.

PostgreSQL holds a timestamp at microseconds and rounds a finer one, half to even, as it reads it; an update batch's keys are held at that precision (`Dialect.StoredKeyValue`), so a key a few nanoseconds off names the row there, and SQLite, which holds the canonical text as written, names no row by it. The tenant column may be a key column, as `("tenant_id", "sku")`; the soft-delete column may not.

<!-- output of ExamplePrimaryKey -->
```text
SELECT "stock"."warehouse_id", "stock"."sku", "stock"."qty" FROM "stock" WHERE "stock"."qty" > $1 ORDER BY "stock"."qty" DESC, "stock"."warehouse_id" COLLATE pg_c_utf8 DESC, "stock"."sku" COLLATE pg_c_utf8 DESC LIMIT 3
[0]
UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id" = $2 AND "stock"."sku" = $3) RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
[7 w1 a]
UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id", "stock"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock", $2) AS "k") RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
[7 [{"sku":"a","warehouse_id":"w1"},{"sku":"a","warehouse_id":"w2"}]]
```

The list sorts by `qty` and ends its keyset on both key columns; the first update reaches one row by its whole key, the second a set of keys bound as one parameter. PostgreSQL reads the set as rows of the table's own type (`jsonb_populate_recordset`), which types each value as its column; SQLite reads it with `json_each`, each column by position (`json_extract(value, '$[0]')`).

## Parser and generator options

`Option`s build a `*ParserConfig` (start from `DefaultConfig()`); `parse.NewResource` accepts them through `parse.ParserOptions`.

The limits follow one rule, which filters and patches read alike through `(*ParserConfig).Effective`: zero selects the default for `MaxDepth`, `MaxCost`, `MaxConditions` and `MaxArraySize`, so a hand-built `&core.ParserConfig{MaxDepth: 10}` keeps the other defaults and no value forbids nesting altogether (the smallest depth is 1); zero or less is unlimited for `MaxInputSize` and `MaxPatchCopyBytes`; zero derives `MaxListBodySize`. A negative `MaxDepth`, `MaxCost`, `MaxConditions`, `MaxArraySize` or `MaxListBodySize` is refused by `(*ParserConfig).Validate`, which `NewResource` calls, naming the option: `parse: devices: parser options: WithMaxDepth(-1): a limit is never negative`. A parser built directly (`filter.NewFilterParser`, `patch.NewParser`) with a negative limit refuses every request it bounds.

<!-- BEGIN rendered parser-options: edit the code, then go test -tags=unit -run TestDocTables -update -->

| Option | Type | Default | Description |
|---|---|---|---|
| `WithMaxDepth(n)` | int | `5` | Filter boolean nesting, JSON filter document nesting, patch JSON Pointer segments and merge patch nesting, one bound for both. `0` is the default; negative is refused; the smallest depth is `1` |
| `WithMaxCost(n)` | int | `50` | `0` is the default; negative is refused. Estimated filter cost: about 1 per indexed condition, 10 per unindexed one, more for documents, relations, unanchored patterns and `$or`; a `$in` / `$nin` set costs one condition whatever its size |
| `WithMaxInputSize(n)` | int | `65536` | Filter / patch body bytes; `0` or less is unlimited, for filters and patches alike. A filter is bounded by it in a query string and in a POST list body alike. A request body is bounded however the parsers are: unlimited, a patch body read from a request is held to 1 MiB (`(*ParserConfig).PatchBodyLimit`), as a list body is (`ListBodyLimit`) |
| `WithMaxListBodySize(n)` | int | `0`: derived, `MaxInputSize` + `16384` (`81920` at the default input size), or `1048576` when `MaxInputSize` is unlimited | Bytes of a list request sent as a POST body ([`Resource.ParseListBody`](http-errors.md#the-same-request-as-a-post-body)); a body over it is E2063, read no further. `0` derives it; negative is refused. A list body is always bounded, since nothing in front of a handler bounds a body as header limits bound a URL. The derived headroom holds the members beside the filter: a cursor (at most 4096 bytes), sort, search, name lists and the JSON around them. Set it explicitly to at least `MaxInputSize` plus that room, or a filter a GET carries is refused in a body. `(*ParserConfig).ListBodyLimit` returns the effective bound |
| `WithMaxConditions(n)` | int | `20` | Filter leaf conditions. `0` is the default; negative is refused |
| `WithMaxArraySize(n)` | int | `100` | `$in` / `$nin` members, array `$contains` elements, values in a JSON filter document. `0` is the default; negative is refused. A set or an array `$contains` list binds as one parameter whatever its size, so this alone bounds it; raise it with `WithMaxInputSize` for endpoints filtering by large sets |
| `WithMaxPatchCopyBytes(n)` | int64 | `1048576` | Bytes a patch's `copy` operations may add during typed apply; `0` or less is unlimited |
| `WithCodec(c)` | `Codec` | `StdlibJSON` (numbers decode as `json.Number`, trailing data rejected) | Decodes request bodies and undeclared documents in typed apply; declared JSON storage uses fresh Go JSON decoding. Cursors use Parse's internal bounded MessagePack envelope |
| `WithClock(fn)` | `func() time.Time` | `time.Now().UTC` | Anchor for relative-time tokens (`now-7d`), read once per request ([values](queries.md#type-capabilities)) |
| `WithGeneratorOptions(o...)` | `...GeneratorOption` | none | Schema generation options a `Resource` generates its schemas with; each use adds to the earlier ones, a later option overriding one it conflicts with |

<!-- END rendered parser-options -->

`GeneratorOption`s configure a `SchemaGenerator` (`NewSchemaGenerator`, `parse.NewSchemaRegistry`, or `WithGeneratorOptions`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithSQLTableName(name)` | string | `PluralSnake(type name)` | Table name for the type |
| `WithTableConvention(fn)` | `TableConvention` | `PluralSnake` | Table naming. `PluralSnake` and `PluralCamel` pluralize by the regular English rules (`Analysis` → `analyses`, `Quiz` → `quizzes`, `userIDs` → `user_ids`); name the table of an irregular noun (`Person`, `Leaf`, `Child`) with `WithSQLTableName` |
| `WithColumnConvention(fn)` | `NamingConvention` | `SnakeCase` | Column naming when a field has no column tag |
| `WithColumnMapper(m)` | `SQLColumnMapper` | none | Maps a field's column to the SQL expression that reads it. `MapColumn(table, column, type)` gets the column the `db` tag or the column convention names (the one patch writes), the same in every view of the field; the mapper's SQL is trusted and emitted as written, so it quotes any name a statement must quote (a reserved word, a mixed-case column) |
| `WithColumnTypes(types)` | `map[string]SQLType` | none | Copied physical column declarations; authoritative JSON/native-array profile for filter, patch, sort and search schemas |
| `WithFieldNameTag(tag)` | string | `"json"` | Tag holding external field names |
| `WithColumnTag(tag)` | string | `"db"` | Tag holding column names |
| `WithSortMaxFields(n)` | int | `3` | Sort fields a request may name |
| `WithSearchParam(p)` | string | `"search"` | Query parameter a search schema advertises (`search.param` in the schema document), which a resource's `ParseList` reads beside `search`; the list body member stays `search`. `NewResource` refuses a `p` that is another list parameter, an `AcceptParams` name or a flat filter on a field ([search parameter](http-errors.md#search-parameter)) |
| `WithForeignTagKeys(keys...)` | `...string` | none | Tag keys another library reads, left alone though one is a character from a key parse reads; calls accumulate |
| `WithSearchLanguage(lang)` | string | `"english"` | PostgreSQL text search configuration a search schema searches with; a name that is no (schema-qualified) identifier fails schema generation |
| `GeneratorOptions.DefaultFilterOps` | `map[FieldType][]string` | filter package defaults | Default operators per type; each must apply to the type (`filter.OperatorsFor`) |
| `GeneratorOptions.DefaultPatchOps` | `map[FieldType][]string` | patch package defaults | Default patch operations per type |

A patch schema carries the two limits a patch is held to beside the parser's. Set them on a resource's schemas in `parse.CustomizeSchemas` (`s.Patch`, `s.ServerPatch`), or on a schema built with `patch.NewPatchSchema` before it serves requests:

| Field | Type | Default | Description |
|--------|------|---------|-------------|
| `PatchSchema.MaxOperations` | int | `patch.DefaultMaxOperations` (`50`) | Operations in one patch, a merge patch's members and merged objects counted (E2066); `0` is unlimited |
| `PatchSchema.MaxRelationValues` | int | `patch.DefaultMaxRelationValues` (`1000`) | Keys one operation on a relation path names, repeats counted once (E2119, `Details` `count` and `limit`); `0` is unlimited. Served in the schema document as `max_relation_values` |

Table names and columns must be SQL identifiers (`[A-Za-z_][A-Za-z0-9_]*`); `TableName`, `ColumnName` and `MappedExpr` return an error otherwise, so a bad tag fails at schema generation. `MappedExpr` leaves the column unchecked when a `ColumnMapper` renders it.

Every statement quotes the tables and columns it names (`"user"."order"`) and qualifies each column it reads by its table — an `UPDATE`'s targets, and the stored values its `SET` and change predicate read, are its one table's columns — so a reserved word (`user`, `order`, `group`) or a mixed-case column reads as a name. A quoted name is case-sensitive: on PostgreSQL the `db` tag and the table name must be spelt as the catalog holds them — lower case for a column created unquoted, as written for one created quoted (`createdAt`). SQLite compares names without regard to case. Qualifying matters on SQLite, which reads an unqualified quoted name that matches no column as a string literal: a misspelt column is an error at execution, never a constant.

`WithFieldNameTag` changes schema field names. Direct typed application uses those names. Document operations also require a codec whose field naming agrees: using a non-JSON field tag with the built-in JSON codec is rejected when names disagree. The option does not teach `encoding/json` a different struct-tag convention.

## Value model

Request documents (JSON filters, JSON Patch and merge patch bodies) decode with `core.StdlibJSON`: the standard library's JSON on `encoding/json/v2` with encoding/json's data model, refusing a member named twice, invalid UTF-8 and data after the document (E2086), and keeping every number's literal (`json.Number`).

| Function | Canonical result |
|---|---|
| `NormalizeInt(v, kind)` | `int64` in the signed kind's range (`reflect.Invalid`: the int64 range); `json.Number`, every Go integer kind, and integral floats within ±2^53 accepted; an unsigned kind is an error |
| `NormalizeUint(v, kind)` | `uint64` in the unsigned kind's range (`reflect.Invalid`: the uint64 range), negatives refused; otherwise as `NormalizeInt` |
| `NormalizeFloat(v, kind)` | Finite `float64`; float32 values are range-checked and rounded to the Go destination precision before binding |
| `NormalizeScalar(type, kind, v)` | Per `FieldType`: exact numbers, canonical decimal text, canonical lowercase UUID string, RFC 3339 UTC timestamp of fixed width (UTC years 0001 to 9999), string, bool, and the canonical text of a date, time, interval, inet, cidr or macaddr. A value of a named type reads as its kind's value: a named string (`type Stamp string`) as the string it holds for every type a string spells, timestamps and dates included, as a driver binds it. A float is finite: a request value holding NaN or an infinity is an error |
| `NormalizeStored(type, kind, v)` | A value a column holds, normalized as `NormalizeScalar` normalizes a request value, except that a float column's NaN and infinities are kept as `float64`: a table written outside parse may hold them. `filter.ColumnValue` (the rows `filter.Evaluate` reads) and cursor values normalize through it |
| `StoredValue(v)` / `Deref(v)` | The value the database holds for a Go value, and whether it is NULL: pointers and interfaces followed (`Deref`, which does only that, and refuses more than 32 in a chain, so a `*any` holding itself is an error rather than a loop, wherever a value is read), then a `driver.Valuer` read, again while its value is one, a pointer-receiver Valuer included. A value of a type parse types by its Go type (`time.Time`, a UUID or decimal type, `Date`, `LocalTime`, `Interval`, `Inet`, `CIDR`, `MACAddr`) is kept as it is: its Valuer writes the canonical text `NormalizeScalar` gives it. Cursors, facets, membership lookups and relation keys read stored values through it, so a scanned `*any`, an `sql.NullString` or a UUID reads alike in each |
| `NormalizeJSON(v)` | A generic document (`map[string]any`, `[]any`, scalars) with `int64` / `uint64` / `float64` numbers; integers outside the supported range and decimal literals that change on float64 decimal round-trip are rejected |
| `CompareNumbers(a, b)` | Comparison by value without allocating: an integer exactly however it is written (`9007199254740993`, `"9007199254740993.0"`, `"9.007199254740993e15"`), against a float as the float's exact value; a fractional literal, or an integer literal beyond 64 bits, as its nearest float64. `IsNumber(v)` is true for the same types: `json.Number` and every Go integer or float kind, named types included |
| `DecimalText(v)` | A decimal's canonical text: plain notation, no exponent, no leading or trailing zeros, `"0"` for either zero; from a JSON number, a numeric string, a Go number, a `core.Decimal` or a decimal text marshaler; at most `MaxDecimalDigits` (1000) digits |
| `CompareDecimals(a, b)` | Exact decimal order of any two values `DecimalText` accepts |
| `ParsePointer` / `FormatPointer` / `EscapePointerToken` | RFC 6901 paths (`~0`, `~1` escapes) |
| `TypeOf(t)` | `TypeInfo{Type, Elem, NumberKind, SQLType}` — Go-only classification; `SQLType` is empty, slices of scalars are arrays, maps/`any`/`json.RawMessage` and other slices are JSON, structs are objects |
| `IsKeyType(t)` | Whether a `FieldType` is a scalar and so may be a key: anything but a document, array, geometry or opaque value. Key columns, relation correlation columns and target keys, and membership fields must be one. A tenant column is narrower: a string, UUID or integer, the types `Scope.Tenant`'s text names one value of |
| `NormalizeKey(types, key)` | A key with each value read as the database holds it (`StoredValue`) and normalized as its column's type (`NormalizeScalar`): the one key rule. A key of another length is an error naming both lengths; a value its column cannot hold, NULL, or a column of no scalar type is a `*KeyValueError` naming the value's position |
| `CompareCanonical(t, a, b)` | Two values of a column of type `t` in the order the SQL dialects give the column, each read as the database holds it (`StoredValue`): NULL after every value and equal to NULL; numbers and decimals by exact value, a float column's NaN after `Infinity` and equal to NaN, as PostgreSQL orders float8, and a float32 column's finite values at float32 precision, as `real` holds them (`0.1` and `float32(0.1)` are equal there, and a value beyond float32's range is an error); strings by code point; UUIDs, timestamps, dates, times, intervals and network values by canonical text; false before true; an object (a scalar codec) by the scalar it reduces to. A value the type cannot hold, NaN outside a float column, and a type with no order (a document, array, geometry or opaque value) are errors, never ordered by another reading. `filter.Evaluate`, keysets (`filter.CompareKeyValues`), key order and patch policies order and equate values through it |
| `KeyTextOf(types, key)` / `CanonicalText(t, v)` | A key's, or one value's, canonical text, each value normalized as its column's type: equal for two keys exactly when the database holds them equal (`"1.50"` and `1.5`, one instant in two zones, `-0` and `0`, a UUID in any spelling), and the text includes, facets (`filter.FacetKeys`) and typed lookups name the value by, which the statements write too. NULL, NaN, the infinities and a document are errors |
| `SchemaGenerator.FieldType(field)` | Go classification combined with declared physical `SQLType`; JSON scalar/slice fields become documents, typed objects retain members, and native arrays remain arrays |

## Schema discovery

`parse.NewSchemaRegistry` holds the schemas discovery serves, one entry per name. Register each `Resource` with `parse.RegisterResource(registry, resource)` or `parse.MustRegisterResource`: its filter schema (relations included), patch, sort and search schemas, under its table. `parse.Register[T](registry)` or `parse.MustRegister[T]` generate schemas for a type no `Resource` serves, with the registry's generator options; they never mirror a resource, which has its own relations, read set, computed columns and options. A name is registered once: a second registration under it, such as a second resource over the same table, is `parse.ErrRegistered` and leaves the first in place, and a `WithPaths` view is not registered. `SchemaFor(name)` returns an independent snapshot of discovery metadata; `FilterSchema`, `PatchSchema`, `SortSchema` and `SearchSchema` return copies of one name's schemas; `ResourceNames` lists registrations. The registry does not execute queries or install your resource handlers. See [HTTP and errors](http-errors.md) for routing and [TypeScript integration](integrations.md#typescript-client) for consuming the schema.

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

Filters take the numeric operators (default `eq ne gt gte lt lte in nin`; the NULL tests when declared in `filter_ops`). Comparison is exact and by value on every backend, never by text: SQL compares it as the dialect's exact decimal (`Dialect.DecimalValue` and `DecimalOrder` of a `filter.DecimalColumn`, `DecimalMember` or `DecimalParam` operand: `numeric` on PostgreSQL; on SQLite the functions [`parsesqlite`](../parsesqlite/README.md) registers), a memory backend compares with `core.CompareDecimals`, and a keyset cursor carries the canonical text, so `9.5` sorts and seeks before `10.25`. A patch replaces a decimal with its canonical text, which the column parses; patch policy `Min`, `Max` and allowed and denied values compare a decimal path by value.

| Backend | Storage and precision |
|---|---|
| PostgreSQL | `numeric` holds every decimal exactly; a declared scale (`numeric(12,4)`) rounds what it stores, as PostgreSQL does. Facet terms count by `trim_scale(value)::text` (PostgreSQL 13+), so `2` and `2.00` share a bucket |
| SQLite | No decimal type, so parse reads a decimal through two deterministic functions: `parse_decimal_key`, a BLOB that orders as the value and is equal exactly where values are, for equality, sets, ranges, sorts, keyset seeks and range facets, so one expression index serves every filter and sort ([index design](operations.md#index-and-query-design)); `parse_decimal`, its canonical text, for facet terms and the value a statement reads. Register them with [`parsesqlite.Register`](../parsesqlite/README.md) before the database opens; a connection without them fails the statement (`no such function: parse_decimal`) rather than rounding. Declare a decimal column `TEXT` to hold every decimal exactly: parse writes the canonical text, and reads a JSON number another writer stored (`10.50`, `1.05e1`) by value. A `NUMERIC` or `REAL` column converts text to REAL beyond 64-bit integers, keeping 15 significant digits: parse compares exactly what it holds, which the column has already rounded |
| Memory | `core.CompareDecimals` over `core.DecimalText`; `filter.KeyValue` canonicalizes a decimal key from any form its column scans to (text, INTEGER, REAL, a `core.Decimal`) |

A slice of decimals stays a `json` document: its elements' wire form is a string each, which no native array column holds as numbers. A decimal path member is a JSON number, or a JSON string in plain notation (`"9.50"`, not `"1e3"`) of at most 1000 characters; anything else is absent ([JSON paths](queries.md#json-paths)).
