# Patches and JSON documents

[Package overview](../README.md)

## Choose the operation

```go
parsed, err := products.ParsePatch(body)                      // RFC 6902 JSON Patch
parsed, err := products.ParseMergePatch(body)                 // RFC 7396 JSON Merge Patch
updated, err := products.ApplyValidatedPatch(parsed, current) // apply to a Go value (patch.Applier)
set, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
plan, err := products.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(id), scope) // complete, scoped UPDATE
```

A field tagged `parse:"server"` is a column only the server sets — a credential's fingerprint, a flag the issuer flips. `ParseMergePatch` refuses it, so no request body reaches it; `products.ParseServerMergePatch(body)` admits it alongside the patchable fields, for the server's own writes, and the result applies through `ApplyValidatedPatch` and `BuildPatchSQL` like any patch. A server field the field-name tag hides (`json:"-"`) is addressed by its column and can only be set by a top-level replace.

`BuildPatchSQL` folds each column's ordered operations into one expression; merge-patch objects merge into the stored document (nested nulls delete keys, siblings survive); `add`, `replace` and `remove` on a member of a JSON document write that member of the stored document (siblings survive) under RFC 6902's preconditions; `array_append` appends atomically. Operations SQL cannot express — `move`, `copy`, `test`, an array element below a field — fail with E2068 / E2070 and apply through `ApplyValidatedPatch`. See [SQL compilation](#sql-compilation).

| Need | Use | Execution |
|---|---|---|
| Update a resource's rows, scoped to the tenant, live rows and policy | `ParseMergePatch` / `ParsePatch`, then `Resource.BuildUpdate` | One `UPDATE … RETURNING` (plus relation statements in its transaction) |
| Update selected fields or recursively merge object members | `ParseMergePatch` | SQL or typed apply, within supported typed-document shapes |
| Replace a complete JSON document | JSON Patch `replace` | SQL or typed apply |
| Append one native array element atomically | JSON Patch `array_append` | SQL or typed apply |
| Add, replace or remove one member of a JSON document, siblings kept | JSON Patch on a path pattern the schema allows (`/attrs/*`) | SQL, failing as RFC 6902 does where the stored document lacks the member ([document members](#document-members)), or typed apply |
| Test a value, copy/move a value, or edit an array element below a field | `ParsePatch` then `ApplyValidatedPatch` | Go application; persist with appropriate concurrency control |
| Set server-owned fields | `ParseServerMergePatch` | Trusted server code only |

### Reading a patch request

`parsehttp.ParsePatch(r, products)` reads a `PATCH` request's body in the grammar its `Content-Type` names: `application/merge-patch+json` (`parse.MediaTypeMergePatch`) or `application/json`, which the SDK sends, is a merge patch; `application/json-patch+json` (`parse.MediaTypeJSONPatch`) is a JSON Patch. Any other media type, or none, is E2086 with `details.parameter` `content-type`. The body is read no further than `MaxInputSize`, the size the patch parsers hold a body to, or 1 MiB when `MaxInputSize` is unlimited (`(*core.ParserConfig).PatchBodyLimit`); a longer one is E2063. A framework that hands its handlers a request's parts calls `Resource.ParsePatchRequest(contentType, body, opts...)`, which applies the same rules.

A body that cannot be read to its end, because the client went away or sent less than its `Content-Length`, is a `*parse.BodyReadError` wrapping the reader's error, for a patch and a list body alike. It is not a `ParseError`, so `parsehttp.WriteError` declines it, and not a server fault: answer it as a bad request at `WARN`, as the example and the pgx recipe do, without an `ERROR` entry.

## Validation

JSON Patch bodies must be arrays of operations; resource Merge Patch bodies must be objects. A root `null` is rejected rather than treated as an empty update. An operation carries only the value it writes: a `remove` of a field, a `move` and a `copy` have no `PatchOperation.Value`, whatever `value` member the client sent. A `remove`, a merge-patch null and a `move`'s source are null writes, so the path's validators run with null.

Every patch schema — generated, or built with `patch.NewPatchSchema` — has `PatchSchema.MaxOperations` `patch.DefaultMaxOperations` (50) unless it is set (0 is unlimited). Code generators can read this constant without constructing a resource. JSON Patch may repeat an operation on one field up to this count; a body budget must account for each operation's path and value envelope. The parser's raw-input byte limit and declared field-value bounds remain independent limits.

A relation operation's value is a list of target keys, decided when it parses so that nothing the parser accepts is refused later for its shape. A key is never null (E2118), so a relation's membership type cannot be nullable (`[]*string` fails schema generation and `patch.RelationPath`). A merge-patch `null`, like a JSON Patch `replace` with `null`, is the clear — the same operation as `[]` — since RFC 7396 reads null as removing the member and the member is the membership; the schema document's `nullable` for a relation path says so. An `add` or `remove` names at least one key (E2118), and a value that is not a list, or a key of the wrong type, is E2077. Each key takes its target key's wire form: a JSON string for a key tagged `json:",string"` (`patch.RelationPath`'s `wire`). The parsers keep each key once, in the order first named, and refuse more than `PatchSchema.MaxRelationValues` (`patch.DefaultMaxRelationValues`, 1000; 0 is unlimited) with E2119 (422), whose `Details` hold the count (`patch.DetailValueCount`, `"count"`) and the limit (`patch.DetailValueLimit`, `"limit"`), before any SQL exists. Every key is a pair written per owner, so the limit bounds one factor of the rows a membership change writes; the owners are the rows the update targets. The schema document serves it as `max_relation_values`.

A schema's paths are its own: `NewPatchSchema(resource, paths)` checks every key as a policy checks its patterns (a JSON Pointer, its leading `/` included, whose tokens are each a literal, `*` or `**`; `name` or `attrs/*` is refused, since a request path would never match it), every path schema and its operations, and keeps copies of the path schemas, so changing one after the call changes nothing the schema parses; `AddPath` adds a copy of a path — refusing one the schema already has — which every parse that starts after it sees; a resource's patch schemas take theirs in `parse.CustomizeSchemas`, while it is built. `Clone` copies a schema deeply. `Path(key)` returns a copy of a key's path schema and `Paths()` yields copies in key order, so changing one changes nothing the schema parses; `UpdatePath(key, update)` changes a declared path (a validator, a narrower operation set), checked as `NewPatchSchema` checks one, for every parse that starts after it. An exact key is matched before a pattern, and patterns most specific first. `patch.IsValidOp` and `patch.Ops` name the recognized operations.

A field tagged `json:",string"` takes its value as a JSON string holding it (`{"id":"9007199254740993"}`), as it reads in responses; a bare JSON number is E2077, while a Go number passed in code is the value. A `test` compares canonical text, and a `copy` or `move` between a quoted and an unquoted field is refused ([quoted values](schema-configuration.md#quoted-values)).

Both wire formats judge each value by one rule — the operation is allowed on the path, a clear is nullable, the value normalizes, its validators pass, an array set whole holds the element counts — and frame failures their own way: a JSON Patch failure names its operation's index (`at index 0`, `ParseError.Index`), a merge patch's names only the path. A merge null on a field that cannot hold null reports that it is not nullable rather than that `remove` is not allowed. A parse with a nil schema is an error. A rejected patch returns no executable `ParsedPatch`. Independent member/operation failures are collected with `errors.Join`: merge members are ordered by name and JSON Patch operations by input order. Each invalid value remains a `*core.ParseError` with its path, operation index where applicable, and E2077. `errors.As(err, &pe)` still selects the first parse failure. Malformed document syntax and document-wide size/count/depth limits fail before value validation.

Declared constraints carry a `*patch.ConstraintError` as the parse error's cause. `Constraint` is `enum`, `minlen`, `maxlen`, `min`, `max`, `minitems` or `maxitems`; `Limit` is the schema's bound text or comma-separated enum allowlist. Value-specific messages are retained. Each invalid array element has its own parse failure; element-count failures are reported alongside value-tag failures. Type/shape failures do not invent a declared constraint.

```go
var pe *core.ParseError
if errors.As(err, &pe) {
    var constraint *patch.ConstraintError
    if errors.As(pe.Cause, &constraint) {
        // pe.Path identifies the member; constraint.Constraint and Limit
        // identify its declared rule without parsing the message.
    }
}
```

Traverse joined errors through `Unwrap() []error` to collect every parse failure. One operation reports at most its first 10, an array failing once per element included; when it fails for more, its first error's `Details` holds how many (`patch.DetailFailureCount`, `"failures"`). The TypeScript SDK mirrors the constraint names and limits; multiple merge-patch failures throw `SchemaValidationErrors`, whose `errors` contains the individual failures and whose inherited field/code identify the first.

Paths are RFC 6901 JSON Pointers: `/labels/a~1b` addresses the key `a/b`, and `~0` escapes a tilde. Patterns match tokens: `*` matches exactly one token and `**` zero or more, wherever it stands, so `/labels/**` matches `/labels` itself. A token is a literal, `*` or `**`: a policy refuses a pattern whose token holds `*` among other characters (`/internal_*`) when it is built. An empty key is a token, so `/labels/*` admits `/labels/` but not `/labels//x`. Among overlapping patterns the most specific decides: more leading literal tokens, then fewer `**`, then fewer `*`.

| Check | Behavior |
|---|---|
| Allowed path and operation | Reject undeclared paths (E2076) and disallowed operations (E2064) |
| Required value | `add`, `replace`, `test`, and `array_append` require a `value` member; missing is E2075 |
| Explicit null | A value, not an omitted member; `ParseOps` callers set `RawPatchOp.HasValue` to distinguish it |
| Nullability | Top-level null/remove requires a type that holds NULL (`core.IsNullableType`): a pointer, slice, map or interface, or a type whose zero value writes NULL and scans NULL back (`sql.NullInt64`, `sql.Null[T]`), which a clear sets to that zero value — not a slice whose `minitems` is above zero, which a clear would empty (E2077) |
| Field value | Normalize exact integers, canonical decimal text, canonical UUIDs/timestamps, array elements, and typed documents; invalid values are E2077. A value with no Go type to shape it — a member below a field, an interface field, a path with no `GoType` — is held as its decoded JSON document (`map[string]any`, `[]any`, `json.Number`), so a `ParseOps` or `ParseMergePatchMap` caller's typed Go value (a `map[string]string`, a struct) is judged by policy as SQL stores it; one JSON cannot hold (`NaN`) is E2077. Every string in the value, a document's member names included, is valid UTF-8 without U+0000 (`core.CheckText`), or E2077; a path or `from` that is not is E2075, and a merge patch member name E2076. A typed Go value is checked as its JSON encoding writes it, invalid UTF-8 as U+FFFD |
| Value ownership | A parsed patch owns its values: changing what was passed to `ParseOps` or `ParseMergePatchMap` after the parse changes nothing it holds, and each `ApplyValidatedPatch` (`Applier.Apply`) result holds values of its own, so a handler may adjust one result and still build the UPDATE from the same patch. A field typed as an interface with methods keeps the value it was given. A Go value that holds itself is refused: E2077 in a JSON Patch, E2074 in a merge patch, as an object nested past `MaxDepth` |
| Fixed arrays | Replacement must have exactly the Go array's length; `array_append` cannot grow it |
| Enum, bounds and policy | Enforce the `enum`, `minlen` / `maxlen` and `min` / `max` validators (per element on an array path; null passes, as nullability decides it) and caller-supplied path/value policy; a value outside them is E2077 |
| Element counts | An array a top-level `replace` or `add` sets whole holds the path's `minitems` / `maxitems` (E2077). An element added, appended or removed, and an array moved or copied in, change an array the parser does not see: the store's constraint counts those |
| Transfer source | `move` needs permission to remove its source; top-level source must be nullable, and its validators accept null. Typed source/destination kinds must be compatible, pointers aside |
| Runtime transfer destination | Validate the resolved value against destination nullability and validators after preceding operations run |
| `test` | Exact JSON comparison; `1.0` equals `1`, but missing paths or different document members fail E2077 |

Integral decimal/exponent values remain exact through the supported integer range. Fractional JSON literals that would change on float64 decimal round-trip are rejected rather than rounded. A [decimal field](schema-configuration.md#decimals) takes any JSON number or numeric string exactly, as its canonical text. A `test` operand preserves its supplied shape rather than filling missing struct members.


Default operations per type. `remove` follows nullability: it is dropped for non-nullable fields (and rejected in `patch_ops` on them) and added for nullable scalars such as `*bool` or `*uuid.UUID`. `GeneratorOptions.DefaultPatchOps` replaces a type's defaults as given:

<!-- BEGIN rendered patch-operations: edit the code, then go test -tags=unit -run TestDocTables -update -->

| Type | Default operations |
|---|---|
| string | replace |
| int | replace |
| float | replace |
| decimal | replace |
| bool | replace |
| timestamp | replace |
| date | replace |
| time | replace |
| interval | replace |
| inet | replace |
| cidr | replace |
| macaddr | replace |
| uuid | replace |
| json | replace add remove |
| array | replace add remove array_append |
| object | replace add remove |
| any | none |
| geo | none |
| relation | replace add remove (junction rows) |

<!-- END rendered patch-operations -->

`array_append` is an extension operation: it appends one element to an array, initializing a missing or null array. A Go fixed-size array (`[2]int`) cannot grow, so it is not given `array_append` and declaring it in `patch_ops` fails schema generation. A null appended element is valid only when the element type is nullable: `[]*int` can contain null, `[]int` cannot. Nullable pointer elements are also preserved during whole-array replacement.

A merge patch is bounded like the JSON Patch it stands for: a member nested `d` objects deep is a `d`-segment pointer (`MaxDepth`, E2074), and each top-level member and each nested object that merges counts as one operation against `MaxOperations` (E2066). Scalar and array members do not count — however many an object holds, they compile to one bound value.

## SQL compilation

`BuildPatchSQL(parsed, dialect, opts...)` folds each column's ordered operations into one expression, so repeated operations on a column compose instead of overwriting each other:

| Operations on a column | SQL |
|---|---|
| replace / add | `col = $n` (documents bound as JSON) |
| remove, or a null value | `col = NULL`; a slice or map field stores its empty value — slice and map columns are usually NOT NULL with an empty default |
| array_append | `array_append(COALESCE(col, '{}'), $n)` (PostgreSQL), `json_insert(COALESCE(col, '[]'), '$[#]', json(?n))` with the element bound as JSON (SQLite), so booleans, numbers and strings keep their JSON types; after a replace the result binds as one array |
| merge-patch object | SQLite `json_patch(COALESCE(col, '{}'), ?n)`; PostgreSQL, per object level, the target as an object (else `{}`) `- $deleted::text[] \|\| $members::jsonb` plus one two-argument `jsonb_build_object($key, …)` per nested object — a level with nested objects names its target once in a scalar subquery that its children read, so the SQL and its arguments grow linearly with the patch and no call nears PostgreSQL's 100-argument limit; a `json` column (declared `core.SQLJSON`) is read as `jsonb` and the result stored back. A merge patch writes its column with one operation, so nothing else folds with it |
| add / replace / remove on a member of a JSON document | PostgreSQL `jsonb_set(col::jsonb, $p::text[], $v::jsonb, true)` / `(col::jsonb #- $p::text[])`, SQLite `json_set(col, ?p, json(?v))` / `json_remove(col, ?p)`, one call per operation, nested in patch order; after a whole-field replace, add or remove in the same patch, the members apply in Go to the value it leaves (a map field's cleared value is `{}`, so `remove /meta` then `add /meta/x` stores `{"x":…}`, as Apply does) and one document binds. Each operation's RFC 6902 precondition the patch does not settle itself goes into `Precondition` ([document members](#document-members)) |
| move, copy, an array element below a field, `array_append` below a field or on a JSON document | E2070 |
| test | E2068 |

### Document members

An operation below a JSON document field (`/attrs/color` under a `/attrs/*` or `/attrs/**` pattern) compiles against the stored document, so the members it does not name survive. RFC 6902 makes a `replace` or `remove` of a missing member, and an `add` whose parent is not an object, fail the whole patch; `jsonb_set` and `json_set` set a member whether it exists or not, so `BuildPatchSQL` states each operation's requirement as a `patch.Precondition`:

| Operation | Requires of the document as the earlier operations leave it |
|---|---|
| replace, remove | the member exists |
| add | the member's parent is an object (a NULL or non-object document fails an add of a top-level member) |

What the patch's own earlier operations decide is settled at compile time: a `remove` of a member the patch added holds, a second `remove` of one member fails, and after a whole-field `replace` the rest apply to its value in Go. One settled as failing is E2077 from `BuildPatchSQL`, with the operation's `Index`, as `ApplyValidatedPatch` reports it. The rest depend on the stored row: `PatchSQLResult.Precondition` is their conjunction over the stored columns, sharing the SET clause's placeholders and counted in `ParamCount`, and the `UPDATE` must AND it into its WHERE. `PatchSQLResult.Preconditions` lists them, and `patch.RenderPreconditions` renders the conjunction and a `CASE` naming the first operation a row fails, for a statement of its own.

The single statement enforces the preconditions as a WHERE guard: a row that fails one is not written. A row that is not written cannot say why, so `Resource.BuildUpdate` adds `UpdatePlan.Conflict`, a read under `Current`'s row lock that returns the index of the first failing operation; the executors run it before `Owner` and return its E2077 with that operation's `Index` and `Path`, writing nothing ([Resource updates](#resource-updates)). A caller running `BuildPatchSQL` itself treats a target row its guarded `UPDATE` did not return as that failure.

A member is addressed by object keys only. PostgreSQL's `#>` and `jsonb_set` read a token that looks like an integer as an array index, so a member whose name reads as an index (`/attrs/0`, `/attrs/-`) is E2070 unless the field's Go type makes its parent an object (a struct or a map, `map[int]T` included); a member of an array is E2070 always. A member below a type that codes itself as JSON (`core.CustomJSON`: `MarshalJSON` or `UnmarshalJSON`, or encoding/json/v2's `MarshalJSONTo` or `UnmarshalJSONFrom`, on the value or its pointer) is E2070, since only its decoder can judge the stored document. Below any other declared Go type, `ParsePatch` holds the member to it, for SQL and `ApplyValidatedPatch` alike: a struct member by its exact JSON name, a map key as encoding/json writes it (`/sizes/07` for `map[int]T` is refused; name it `/sizes/7`), an added or replacing value that decodes as the member, its tag options applied, and a `remove` only of a struct member the type leaves out at its zero value (`omitempty` or `omitzero`): SQL deletes the member, which the type would write again, so `remove /dims/w` of a plain `W int` is refused, as a merge patch's `{"dims":{"w":null}}` is; replace it with `0`. Each is E2077.

Values bind through the dialect's `MarshalValue(value, core.TypeInfo)` (the `SQLDialect` interface): documents as JSON text; on PostgreSQL an array as a typed Go slice — UUIDs as their canonical text — which pgx encodes in every query mode, and on SQLite as JSON text; a timestamp on SQLite as its canonical text ([timestamps](schema-configuration.md#timestamps)). A hand-built `PathSchema` naming no Go type binds an array typed by its elements.

`parse.ColumnType` / `core.WithColumnTypes` carries the physical storage profile into patch paths and binding. A declared JSON string, byte slice or scalar slice binds its Go JSON encoding; native SQL arrays keep their array representation. Declared JSON uses this encoding even when the Go value implements `driver.Valuer`; native database values keep their driver binding.

A path that is neither a relation nor has a `SQLColumn` fails with `patch.ErrSQLColumnMissing`, a plain error: the schema is the server's, and the request's path text is never used as an identifier. Relation paths go to `PatchSQLResult.RelationOps`; `RenderRelationOp(op, dialect, ownerPKs, opts)` renders their junction statements for the caller's transaction and fails closed on a tenant-scoped junction without `RenderRelationOpOptions.TenantValue` (or explicit `Unscoped`). The owners and the values each bind as one array parameter — `col = ANY($n)` / `col <> ALL($n)` on PostgreSQL, `col IN (SELECT value FROM json_each(?n))` on SQLite — so a statement's parameter count does not depend on how many there are ([pair tables](pair-tables.md#update-membership)). It refuses an operation it cannot render: an `add` or `remove` with no values, a null key, keys of two types, or a dialect other than PostgreSQL and SQLite.

`PatchSQLResult.Assignments` exposes each folded `ColumnAssignment{Column, Expression}` before rendering the comma-separated `SetClause`. Both forms share `Args` and placeholder numbering. `Resource.BuildUpdateBatch` composes these expressions for distinct per-key column patches; its [transaction and exact-preimage contract](operations.md#varied-native-update-batches) applies to every chunk. A row patch with a document-member precondition the stored row decides is E2070 there: update that row with `BuildUpdate`, whose `Conflict` reports the operation it fails.

A SET clause is not a complete UPDATE. `Resource.BuildUpdate` builds the complete statement ([Resource updates](#resource-updates)); without a resource, add a trusted table/column projection, scoped WHERE, and correctly offset placeholders, and reject or deliberately handle an empty patch before executing SQL. See [the pgx recipe](../parsepgx/recipe_test.go). With `WithChangePredicate()`, `ChangePredicate` ANDed into that WHERE writes only the rows the patch changes; it uses the SET clause's placeholders, so the WHERE's own still start after `ParamCount`.

## Patches built in code

A server that builds a patch need not encode JSON to parse it back. `MergePatchMap(map[string]any{...})` validates a merge patch given as a map exactly as `ParseMergePatch` validates its body — a nested `map[string]any` merges into a document, any other value (a Go value of the path's type, such as a `time.Time` or a struct, which replaces the document whole) replaces, and null removes, null being what encoding/json writes as null: `nil` or a nil pointer, map or slice, at any depth — and `ValidateOperations([]patch.RawPatchOp{...})` validates JSON Patch operations as `ParsePatch` does. `ServerMergePatchMap` and `ValidateServerOperations` do the same against the server patch schema, for the server's own writes. A `patch.ParsedPatch` assembled by hand would skip validation, so it is refused: `BuildPatchSQL` and `ApplyPatchPolicyConstraints` return `patch.ErrUnparsed` for a patch no parser produced. A resource goes further and takes only the patches its own patch schemas validated (`ParsedPatch.ParsedFor`): `ApplyValidatedPatch`, `BuildUpdate` and `BuildUpdateBatch` return `parse.ErrForeign` for one assembled by hand or parsed by another resource.

## Resource updates

`Resource.BuildUpdate(dialect, parsed, target, scope, opts...)` compiles a validated patch into the statement that applies it. The WHERE carries the target (`ByID`, `ByIDs`, `ByKey`, `ByKeys` or `ByFilter`), the tenant floor (`Scope.Tenant`, required on a tenant-scoped resource: `ErrTenantRequired`; normalized once to the [tenant key](relations.md#tenant-key), `ErrTenantInvalid` when a tenant column the resource reaches cannot hold it), live rows only, and `Scope.Policy`. Its placeholders continue after the SET clause's.

```go
plan, err := products.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(id),
	parse.Scope{Tenant: tenantID, Policy: policy},
	parse.StampSQL("updated_at", "NOW()"))
if err != nil { /* ErrNoTarget, ErrKeyInvalid, ErrUpdateScope, ErrTenantRequired, ErrTenantInvalid or a patch error */ }

res, err := parsepgx.Update(ctx, pool, products, plan) // parsesql.Update on SQLite
if err != nil { /* the database's error; the row and membership changes are rolled back together */ }
if len(res.Changed)+len(res.Unchanged) == 0 { /* not found */ }
// res.Changed: the rows whose columns the update wrote. res.Unchanged: the
// rows it left. res.RelationRows: the junction rows it deleted and inserted.
```

| Patch | `plan.Owner` | `plan.Writes` |
|---|---|---|
| Writes a column | `UPDATE … SET … WHERE … RETURNING <columns>` | true |
| Changes relations only | `SELECT <columns> … WHERE …` plus the dialect's row lock (`FOR UPDATE` on PostgreSQL), so two replacements of one relation cannot interleave their delete and insert | true |
| Empty | `SELECT <columns> … WHERE …` — a scoped read | false |

The rows `Owner` returns are the rows the scope admits. Zero rows means the target is absent, deleted, another tenant's or outside the policy; answer that as not found. A patch that writes members of a JSON document ([document members](#document-members)) ANDs its `Precondition` into `Owner`'s WHERE and is planned with `Current` and `Conflict`: when a target row's stored document fails an operation's precondition, the update returns that operation's E2077, as `ApplyValidatedPatch` does, and writes no row, rather than skipping the row as not found. Under `SkipUnchanged` the `UPDATE` returns only the rows it changed, and `plan.Current` tells an unchanged target from a missing one ([Skipping unchanged rows](#skipping-unchanged-rows)). On PostgreSQL an `UPDATE` of a target that can hold several rows (`ByIDs`, `ByKeys`, `ByFilter`) that no `Current` guards is planned with `plan.Lock`, which locks the targets in key order just before `Owner`: an `UPDATE` locks rows in the order its plan reads them, so two updates of overlapping rows read in opposite orders would each hold a row the other waits for (`40P01`). Relation statements are rendered for the keys `Owner` returned, tenant-scoped as the update was, and run in its transaction. `Owner` runs before them, so a [computed column](queries.md#computed-columns) over a relation's pairs reads the pairs as they were; `Reread(plan, keys)` returns the statement that reads those rows' `Columns` again after the relation statements, held to the update's tenant, or nil when the patch changes no relation or the resource has no computed column.

`parsepgx.Update(ctx, pool, products, plan)` on PostgreSQL and `parsesql.Update(ctx, db, products, plan)` on SQLite do all of this and return a `parse.UpdateResult`. `Changed` holds the target rows whose columns the update wrote; `Unchanged` holds the rows it left: every row of a plan that assigns no column (`plan.Owner` a `SELECT`: an empty or relation-only patch), and under `SkipUnchanged` the rows the patch already described (`Resource.UpdatedRows` splits them). `RelationRows` counts the junction rows the relation statements deleted and inserted. Every update runs in one transaction — a savepoint inside the caller's — settled once every returned row has scanned, so its rows and memberships change together and an error means nothing was written, unless it is `parse.ErrCommitUnknown`: a COMMIT sent and never answered, which may have been applied ([server errors](operations.md#server-errors)). `Conflict` runs after `Current` and before `Owner`; on PostgreSQL `Owner` then goes in a batch of its own, so a conflicting update sends no write. A replace or add writes one junction row per matched owner and value: a `ByFilter` update over many rows writes as many pairs as its rows times the patch's values, so bound the target.

| Option / target | Effect |
|---|---|
| `ByID(id)`, `ByIDs(ids...)`, `ByKey(k)`, `ByKeys(ks...)`, `ByFilter(f)` | The rows to change; a zero target is `ErrNoTarget` rather than every row, and a key the key columns cannot hold is `ErrKeyInvalid`, refused as the key before any statement binds it. `ByID` and `ByIDs` name a one-column key's values; `ByKey` and `ByKeys` name `parse.Key`s, a value per key column, and a key of another length is `ErrKeyInvalid` naming both ([keys](schema-configuration.md#keys)) |
| `Stamp(column, at)` | Set `column` to `at` on every update that writes a column; `at` must be non-zero |
| `StampSQL(column, expr)` | Set `column` to the trusted SQL `expr` — the database clock, such as `NOW()` |
| `Increment(column)` | Set the integer `column` to one more than it holds on every update that writes a column — a version the server moves; `RETURNING` reports the moved value. Repeat for each counter; composes with the stamp |
| `SkipUnchanged()` | Update only the target rows the patch's columns change, so a row the patch leaves as it is keeps its stamp and counters and `RETURNING` omits it; `plan.Current` reads every target row. See [Skipping unchanged rows](#skipping-unchanged-rows) |
| `Scope.Governance`, `Scope.IncludeDeleted` | Refused with `ErrUpdateScope`: an update reaches only its own tenant's live rows |

A patch never writes the key or the tenant column. `NewResource` refuses a request or server patch path over either, naming the path and the column: the update's WHERE holds the scope's tenant and the row's key, so the write would move the row out of its tenant or away from every relation correlated on its key, and a tenant transfer is not a patch. `BuildUpdate` and `BuildUpdateBatch` refuse a stamp or increment that names one (`cannot change identity column`). The deleted column is an ordinary column to a patch: when its field has the `patch` capability, a request may set it (`{"deleted_at":"2026-01-01T00:00:00Z"}`), which soft-deletes the row. An update reaches only live rows, so restoring one is the server's own statement.

An update has one stamp: a later `Stamp` or `StampSQL` replaces an earlier one. The stamp column and each increment column must be one of the resource's columns, an increment column an integer one, none a [computed column](queries.md#computed-columns) (`parse.ReadExpr`), which the table does not hold, and none may be named twice. A patch's own write to a stamped or incremented column — a request's or the server's — is dropped: the server's value wins, and a patch writing only those columns writes nothing. `Dialect` is a dialect both the filter and patch compilers speak; `filter.PostgresDialect` and `filter.SQLiteDialect` are.

### Skipping unchanged rows

A patch that sets what a row already holds still writes it: the stamp and counters move, and a consumer that emits an event per updated row emits one for nothing. `SkipUnchanged()` ANDs the patch's change predicate into the `UPDATE`'s WHERE — null-safe comparisons of each column the patch assigns with the value it assigns, reusing the SET clause's placeholders and binding nothing of their own — so only rows that change are written, stamped, counted and returned. A note's `{"title":"Agenda","rank":2}` with `StampSQL("updated_at", "NOW()")`, `Increment("version")` and `SkipUnchanged()`:

<!-- output of ExampleSkipUnchanged_stamped -->
```text
UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = NOW(), "version" = "version" + 1 WHERE (("notes"."tenant_id" = $3 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $4) AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) RETURNING "notes"."id", …
[2 Agenda t1 n1]
SELECT "notes"."id", … FROM "notes" WHERE (("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $2) FOR UPDATE
[t1 n1]
```

The second statement is `plan.Current`: the same scoped WHERE without the predicate, with the dialect's row lock. The executors run `Current` first, then `Owner`, in one transaction, and split the target rows:

```go
plan, err := notes.BuildUpdate(d, parsed, parse.ByID(id), scope,
	parse.StampSQL("updated_at", "NOW()"), parse.Increment("version"), parse.SkipUnchanged())
if err != nil {
	return err
}
res, err := parsepgx.Update(ctx, pool, notes, plan) // parsesql.Update on SQLite
if err != nil {
	return err
}
if len(res.Changed)+len(res.Unchanged) == 0 {
	return ErrNotFound
}
// res.Changed: written, stamped and counted — emit events for these.
// res.Unchanged: matched, left as they were.
```

`Current` goes first because its lock holds every target while `Owner` compares them, so the two split one set of rows: the unchanged rows are exactly those the update compared and left. Run the other way, under READ COMMITTED a row another transaction changes or deletes between the two statements would read as it then stands, or not at all. SQLite has no row lock; the transaction must be a write transaction so no other writer commits between the two, which with `database/sql` is the connection's `_txlock=immediate` setting ([database/sql](integrations.md#databasesql-and-sqlite)), not a statement. `parsepgx.Update` sends `Current` and `Owner` in one batch, in that order. `Unchanged(changed, current)` is the rows of `current` whose keys `changed` lacks: a row the patch changed and moved out of a `ByFilter` target is in `changed` alone. `ExampleSkipUnchanged` prints both statements and is checked by the unit lane.

| Patch, under `SkipUnchanged` | `plan.Owner` | `plan.Current` | What changed |
|---|---|---|---|
| Assigns a column | `UPDATE … WHERE … AND <change predicate> RETURNING <columns>`: the rows it changed | `SELECT <columns> … WHERE …` plus the row lock | `Changed`: Owner's rows; `Unchanged`: the matched rows it left; neither holds a row: not found |
| Changes relations only | `SELECT <columns> … WHERE …` plus the row lock, as without the option | nil: Owner returns every target row | Every target is `Unchanged`: no column, stamp or counter moves. `RelationRows` counts the memberships removed and added: zero means none changed (each junction pair needs a unique key, which `ON CONFLICT DO NOTHING` relies on) |
| Assigns nothing: empty, or server columns alone | `SELECT <columns> … WHERE …` | nil | `plan.Writes` is false and every target is `Unchanged`: nothing changes |

A patch with columns and relations changes the memberships of every target row, changed and unchanged: the executors pass the keys of both to `RelationStatements` in the same transaction. A membership change alone moves no stamp or counter, with the option or without it.

"Unchanged" means the write would leave the column reading as it does: the stored value is compared with the value the update assigns — the expression the SET clause computes, so a merge that sets what a document already holds is unchanged and an `array_append` never is. A false "changed" costs a write; the comparisons are built never to report a false "unchanged".

| Column | PostgreSQL | SQLite |
|---|---|---|
| Any | `col IS DISTINCT FROM <value> OR col::text COLLATE "C" IS DISTINCT FROM (<value>)::text`: the text a read returns, byte for byte, NULL equal to NULL. The equality types the placeholder as the column — an enum, a domain — since PostgreSQL types it at its first use and the WHERE precedes the SET clause | `col COLLATE BINARY IS NOT <value>` (`IS NOT` is null-safe on every SQLite version): byte for byte whatever collation the column declares (NOCASE, RTRIM), numbers by value under the column's affinity |
| Text (string fields, enum labels) | A change of case or accents counts where the column's equality ignores it: citext, a nondeterministic collation | The same, against NOCASE and RTRIM |
| Interval (a string field) | `1 day` and `24 hours` differ: equal intervals, other text | By text |
| Float | `-0` and `0` differ | By value; a REAL column stores `-0.0` as `0.0` |
| Decimal | By value, `col IS DISTINCT FROM <value>` alone: parse binds and reads a decimal in one canonical text (`1.50` binds as `1.5`), and a `numeric(p,s)` column stores scale `s` whatever text is bound, so value equality is the read's equality. The one boundary: a scale another writer stored in an unconstrained `numeric` column (`1.50` where parse writes `1.5`) is not part of the value, and a patch of the same number leaves it | By value in a NUMERIC or REAL column, which converts the value on store as it does in the comparison: `1` and `1.0` read the same. By text in a TEXT column, which holds decimals exactly: parse writes the canonical text, so a value another writer stored with a scale (`1.50`) counts as changed once |
| Timestamp | One instant, whatever offset the patch wrote | parse's canonical text (UTC, nine fractional digits): one instant is one text where parse wrote the stored value |
| Civil date, UUID, time, interval, inet, cidr, macaddr | Their text: one value, one text. An `Interval` whose fields differ (`P1M`, `P30D`) counts as changed though PostgreSQL's equality matches them | The canonical text; a legacy midnight timestamp in a date column counts as changed, and the write stores the date |
| JSON document (`jsonb`) | jsonb's text, which is canonical: key order, whitespace and duplicate keys do not count; a number's written form (`1.0` against `1`) does | By text. parse writes compact JSON with object keys in Go's sorted order, and a merge keeps the stored order: a document stored in another order or spacing counts as changed once, and the write stores parse's text |
| JSON declared `core.SQLJSON` (`json`) | Compared as `jsonb` text (`json` has no equality operator): a document whose stored text differs only in whitespace or key order counts as unchanged, although a read returns that text | By text, as above |
| Native array | Its text: element by element, in order | By text, as documents |
| Cleared value | A slice or map field clears to empty, unchanged when already empty; others to NULL | The same |

A value the column rounds as it stores it — a timestamp finer than the column's precision, a decimal beyond a `numeric(p,s)` scale — is compared as the patch gave it, so sending it again counts as changed every time and writes what the column already holds. A PostgreSQL `json` column must be declared with `parse.ColumnType(column, core.SQLJSON)`: undeclared, it is compared as a `jsonb` one and the update fails with `operator does not exist: json = …`. A document in a `text` column compares by text, as on SQLite. On SQLite, a column with no affinity (an untyped or `ANY` column) compares an integer and a real of one value as equal although a read reports their storage classes apart; scanned into the field's Go type they read the same. Each assigned expression runs up to twice in the WHERE for every target row and once more in the SET clause for the rows that change, so a merge costs up to three times what it does unguarded.

## JSON documents

A `map[string]any` or `json.RawMessage` field is a JSON document (`jsonb` on PostgreSQL, JSON text on SQLite); a struct field is too, once given `filter_ops`. Filtering one, whole or by its declared members, is in [queries](queries.md#json-documents) and [JSON paths](queries.md#json-paths). Patching one:

- Updates: a merge patch merges recursively in SQL on both dialects and in `ApplyValidatedPatch` — siblings survive, nested `null` deletes. A JSON Patch to `/attrs/color` is refused (E2076); with a `/attrs/*` path pattern it parses, and its `add`, `replace` and `remove` compile to SQL on both dialects, siblings kept, failing with E2077 where RFC 6902 does ([document members](#document-members)).
- Typed documents have additional canonicalization and merge limits; see [typed document behavior](#typed-document-behavior).
- Empty JSON Pointer tokens address empty object keys, including intermediate keys, when the path schema allows them.

## Typed document behavior

For a document with required integer members `w` and `h`:

| Input | Behavior |
|---|---|
| JSON Patch `replace` with `{"w":9}` | Canonical whole value `{"w":9,"h":0}` |
| Merge patch `{"w":9}` into an existing nonnullable document | Updates `w`; preserves `h` |
| Unknown member | E2077 rather than silently losing a field on Go decode |
| Merge deletes required `h` with `null` | E2077 because Go serialization would recreate `h` |
| Partial merge into a nullable struct or typed-map struct value | Must supply members required if the old value is absent; otherwise E2077 |
| Merge patch into a map keyed by an integer or by a type with text methods (`map[int]T`, `map[netip.Addr]T`) | Each member names the key encoding/json decodes it to (`"01"` is key 1, `"0:0:0:0:0:0:0:1"` is `::1`) and is stored under the name encoding/json writes for it; a name no key decodes from, or two names of one key, is E2077 |

Object merges into types with custom JSON encoding (`core.CustomJSON`, either encoding/json version's methods) are rejected, and so is a merge of a Go value of such a type (`MergePatchMap`); a `json.RawMessage`, at the top level or as a member of a declared or undeclared document, merges as a generic document. A struct's embedded fields merge as the fields encoding/json promotes; a name it resolves by depth is refused. Omission-sensitive map merges containing only removals are rejected because deleting the last key would leave SQL `{}` while Go omitted the field. A merge containing a non-null map member guarantees a remaining key and can be accepted. Use whole-document replacement for shapes whose merge cannot be represented consistently.

For declared JSON columns, replacement validates a fresh typed Go JSON decode before writing. Custom-codec subtrees retain their supplied JSON representation; ordinary typed objects still fill required members and honor field omission and `,string`. Integer/text map keys use their canonical JSON names, in a replacement and a merge alike, and colliding key forms are rejected. The stored result is one fresh decode of the actual SQL-bound JSON; validation never stores a custom decoder's transformed value by encoding it again.

A non-SQL-NULL document that decodes to a nil map, slice, pointer or interface is rejected before assignment; use `json.RawMessage` when explicit JSON null must be preserved. Top-level typed nil values follow the same clear semantics as nil input. Request codecs still validate input; they do not replace the declared storage codec. An undeclared typed document decodes with the request codec, and its members are checked on that codec's encoding by encoding/json's rules (the `Codec` interface has no mode that refuses an unknown member), so a member the type does not declare is E2077 rather than dropped.

Keep undeclared document codecs stable across round trips. Do not use top-level `omitempty` / `omitzero` as storage metadata; that enclosing-field omission metadata is not a complete storage schema.

A top-level schema field exists for every operation even when its current JSON encoding omits it through `omitempty` or `omitzero`: replace and remove write it as direct typed application does, and test, copy and move read the value it holds, written as the field's tag writes it without the omit option (`""`, `null`, `"0"` for a `,string` zero). Nested JSON Pointers keep strict existence rules. A nil pointer target is an application error.

`ApplyValidatedPatch` writes a copy of the value it is given, never the value or anything it points to. Before it writes a field promoted from an embedded pointer, the copy gets its own copy of that parent, allocated when the pointer is nil, as encoding/json allocates it; later writes through the same parent land in that copy. The caller's row, and any pointer it kept to an embedded parent, keep their values whether the patch succeeds or fails part-way.

A patch never writes a field promoted through an unexported embedded pointer: reflection can neither allocate that pointer while it is nil nor point it at a copy while it is shared, so the copy could only be written by writing the caller's parent. Such a path is refused where it is declared, never offered to a request:

- `parse:"patch"` on such a field, and `parse:"server"` in the server patch schema, fail schema generation (`GeneratePatchSchema`, `GenerateServerPatchSchema`, and so `NewResource` and `Register`), naming the field and the embedded type. A document column reached that way is refused the same way, and so is a patch field whose document type holds, at any depth, a member promoted through an unexported embedded pointer: every value decodes into that type, which encoding/json cannot decode the member into ([fields and embedding](schema-configuration.md#fields-and-embedding)). A row scan has the same limit, so `NewResource` refuses such a column it reads too.
- A path a `CustomizeSchemas` function adds that reaches such a field (an exact key, a member pattern such as `/attrs/*` on a document column reached that way, or a wildcard such as `/*`) fails `NewResource`.
- A schema completed by hand for a standalone `patch.Applier` is checked with `Applier.CheckSchema`. Unchecked, `Apply` refuses the write with an error that is not a `core.ParseError` — the server's composition fault, not the request's, so `parsehttp.WriteError` leaves it to the caller's 500.

Export the embedded type, or embed it by value: reflection sets the exported fields an unexported struct embedded by value promotes, as encoding/json does.

Document fallback decodes only changed top-level fields. Untouched values retain their hidden/private state and do not invoke custom decoders. Touched composite fields are reconstructed from their codec data, so custom nonserialized state in a modified document is not a storage contract.

JSON Patch `test` checks the supplied document shape; missing struct members are not filled in to make a test succeed. A parsed operation is not a guarantee of application success: tests, missing runtime paths, copy budgets, and destination validation can still reject it. Persist read/modify/write results under a transaction, row lock, or version predicate appropriate to your storage.

## Configuration

`PatchSQLOption`s for `BuildPatchSQL`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithStartParam(n)` | int | `0` | Placeholder offset: the first is `n+1`, as with `filter.WithStartParam` |
| `WithAutoTimestamp(col)` | string | none | Also set `col` to the current time (once, even if the patch sets it) |
| `WithAutoTimestampSQL(col, expr)` | string, string | none | `WithAutoTimestamp` with `col` set to the SQL expression `expr` (the database's clock, `NOW()`) instead of a bound time — for a repository whose other writes stamp the database's clock |
| `WithIncrement(col)` | string | none | Also set `col` to `col + 1` (once, even if the patch sets it); repeat for each counter. `col` must be an identifier and differ from the timestamp column |
| `WithTimeFunc(fn)` | `func() time.Time` | `time.Now().UTC` | Clock for `WithAutoTimestamp` |
| `WithChangePredicate()` | — | off | Also render `PatchSQLResult.ChangePredicate`: true for a row the patch's columns change, over the SET clause's own placeholders and without the timestamp or increments; [Skipping unchanged rows](#skipping-unchanged-rows) defines its comparisons. Fails for a dialect other than PostgreSQL and SQLite |

Parsers take a `*core.ParserConfig` (`MaxDepth` — pointer segments, and merge-patch nesting; `MaxInputSize`; codec; see [schema/configuration](schema-configuration.md)); `PatchSchema.MaxOperations` (default `50`) bounds operations per patch, merge patches counted as above (E2066), and `PatchSchema.MaxRelationValues` (default `1000`) the keys of one relation operation (E2119).

## Policy

A policy restricts the paths a patch may write and the values it may write there:

```go
pc, err := patch.NewPatchPolicyConstraints(
	patch.ForbidPaths("/tenant_id", "/internal/**"),
	patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}),
	patch.ValidateWith(func(op *patch.PatchOperation, subject *patch.PolicySubject) error { return nil }),
)
```

| Option | Effect |
|---|---|
| `ForbidPaths(patterns...)` | Forbid writing the paths the patterns match (E2084). A pattern is a JSON Pointer whose tokens are each a literal, `*` (exactly one token) or `**` (zero or more): `/tenant_id`, `/internal/**` |
| `ConstrainValues(pattern, constraint)` | Restrict the values written at the paths `pattern` matches to a `ValueConstraint` (E2085) |
| `ValidateWith(validators...)` | `PatchValidator`s each operation must pass, called with the request's `PolicySubject` |

`NewPatchPolicyConstraints` checks the policy when it is built, so a bad pattern is a startup error and never a request's: it refuses a path pattern whose token holds `*` among other characters, a value constraint given twice for one pattern, a `ValueConstraint.Pattern` that does not compile, and a nil validator. The policy is immutable. `ApplyPatchPolicyConstraints(parsed, pc, subject)` enforces it and records it on the patch (`AuditInfo`), and `parse.WithPatchPolicy(pc, subject)` applies it inside a `Resource`'s `ParsePatch`, `ParseMergePatch`, `MergePatchMap`, `ValidateOperations` and `ApplyPatch`; `subject` is a `*patch.PolicySubject` for the validators. `AllowedValues` and `DeniedValues` compare numbers by value, a decimal path's exactly; `PatchPolicyFromWritableFields` builds a policy forbidding every path a writable-field set leaves out.

Policy judges what each operation writes, not its envelope. A whole value writes every path below its own, a merge object writes the members it names, a `remove` writes null whatever `value` it carries, a `move` writes its source as well as its destination, and a `test` writes nothing.

- **Forbidden paths (E2084).** An operation is refused when a pattern matches its path. One that writes or reads a path whole — `replace`, `add`, `remove`, `array_append`, a relation's membership, a `move`'s or `copy`'s destination and source, a `test`'s path, a merge member whose value is not an object — is also refused when a pattern can match a path below it, so `{"op":"replace","path":"/attrs","value":{}}` is refused under `/attrs/secret`. A merge object is refused when it names a member chain a pattern matches, null included: `{"attrs":{"secret":null}}`. A pattern forbids the paths it matches and nothing inside them: `/attrs/secret` leaves `/attrs/secret/x` writable, `/attrs/secret/**` does not.
- **Value constraints (E2085).** Each path an operation writes is judged by its constraint: the path's own key, else the most specific pattern that matches it. A constraint below the operation's path judges the member at that sub-path of the written value: for a whole-value write an absent member reads as null, for a merge an absent member is untouched. On an array path (a schema path whose `DataType` is `array`, at a field or inside a document) a constraint judges each element, whichever operation writes them: a whole array, an `array_append`, and an element added or replaced by its own path (`/tags/-`, `/tags/0`); an array cleared, left absent, or an element removed writes no element. A write it cannot judge as an element is refused: a `move` or `copy` into an element, and any write inside one (`/tags/0/name`). Null is judged by `AllowedValues` and `DeniedValues`; `Pattern` judges strings and `Min` / `Max` numbers, and each refuses any other value. A `move` or `copy` into a path that has a constraint, or a constraint below it, is refused, since its value is read only as the patch applies; so is a merge into a path with a constraint of its own.
- **Affected paths.** `ParsedPatch.AffectedPaths`, and `AuditInfo`'s `affected_paths`, list every path the operations write: a `move`'s source included, a `test`'s path not.

`copy` / `move` validate the resolved source against destination nullability, normalization, and validators after preceding operations run. Copy budgets count every serialized source, including overwrites. These checks complement authorization; the application still decides who may modify the resource.

[Error catalog](http-errors.md#error-codes) · [Relations](relations.md#change-membership) · [Pair tables](pair-tables.md#update-membership)
