# Patches and JSON documents

[Package overview](../README.md)

## Choose the operation

```go
parsed, err := tp.ParsePatch(body)                      // RFC 6902 JSON Patch
parsed, err := tp.ParseMergePatch(body)                 // RFC 7396 JSON Merge Patch
updated, err := tp.ApplyValidatedPatch(parsed, current) // apply to a Go value
set, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
plan, err := products.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(id), scope) // complete, scoped UPDATE
```

A field tagged `parse:"server"` is a column only the server sets — a credential's fingerprint, a flag the issuer flips. `ParseMergePatch` refuses it, so no request body reaches it; `tp.ParseServerMergePatch(body)` admits it alongside the patchable fields, for the server's own writes, and the result applies through `ApplyValidatedPatch` and `BuildPatchSQL` like any patch. A server field the field-name tag hides (`json:"-"`) is addressed by its column (`patch.ServerFieldName`) and can only be set by a top-level replace.

`BuildPatchSQL` folds each column's ordered operations into one expression; merge-patch objects merge into the stored document (nested nulls delete keys, siblings survive); `array_append` appends atomically. Operations SQL cannot express — `move`, `copy`, `test`, nested paths — fail with E2068 / E2070 and apply through `ApplyValidatedPatch`. See [SQL compilation](#sql-compilation).

| Need | Use | Execution |
|---|---|---|
| Update a resource's rows, scoped to the tenant, live rows and policy | `ParseMergePatch` / `ParsePatch`, then `Resource.BuildUpdate` | One `UPDATE … RETURNING` (plus relation statements in its transaction) |
| Update selected fields or recursively merge object members | `ParseMergePatch` | SQL or typed apply, within supported typed-document shapes |
| Replace a complete JSON document | JSON Patch `replace` | SQL or typed apply |
| Append one native array element atomically | JSON Patch `array_append` | SQL or typed apply |
| Test a value, copy/move a value, or edit an explicitly allowed nested pointer | `ParsePatch` then `ApplyValidatedPatch` | Go application; persist with appropriate concurrency control |
| Set server-owned fields | `ParseServerMergePatch` | Trusted server code only |

## Validation

JSON Patch bodies must be arrays of operations; resource Merge Patch bodies must be objects. A root `null` is rejected rather than treated as an empty update. Explicit null removal still runs path validators.

Paths are RFC 6901 JSON Pointers: `/labels/a~1b` addresses the key `a/b`, and `~0` escapes a tilde. Patterns match tokens: `*` matches one token, `**` any number. An empty key is a token, so `/labels/*` admits `/labels/` but not `/labels//x`.

| Check | Behavior |
|---|---|
| Allowed path and operation | Reject undeclared paths (E2076) and disallowed operations (E2064) |
| Required value | `add`, `replace`, `test`, and `array_append` require a `value` member; missing is E2075 |
| Explicit null | A value, not an omitted member; `ParseOps` callers set `RawPatchOp.HasValue` to distinguish it |
| Nullability | Top-level null/remove requires a nullable Go type: pointer, slice, map, or interface (E2077) |
| Field value | Normalize exact integers, canonical UUIDs/timestamps, array elements, and typed documents; invalid values are E2077 |
| Fixed arrays | Replacement must have exactly the Go array's length; `array_append` cannot grow it |
| Enum and policy | Enforce schema validators and caller-supplied path/value policy |
| Transfer source | `move` needs permission to remove its source; top-level source must be nullable. Typed source/destination kinds must be compatible, pointers aside |
| Runtime transfer destination | Validate the resolved value against destination nullability and validators after preceding operations run |
| `test` | Exact JSON comparison; `1.0` equals `1`, but missing paths or different document members fail E2077 |

Integral decimal/exponent values remain exact through the supported integer range. Fractional JSON literals that would change on float64 decimal round-trip are rejected rather than rounded. A `test` operand preserves its supplied shape rather than filling missing struct members.


Default operations per type. `remove` follows nullability: it is dropped for non-nullable fields (and rejected in `patch_ops` on them) and added for nullable scalars such as `*bool` or `*uuid.UUID`. `GeneratorOptions.DefaultPatchOps` replaces a type's defaults as given:

| Type | Default operations |
|---|---|
| string, int, float, bool, timestamp, uuid | replace (+ remove when the Go type is a pointer) |
| json, object | replace add remove |
| array | replace add remove array_append |
| relation | replace add remove (junction rows) |

`array_append` is an extension operation: it appends one element to an array, initializing a missing or null array. A Go fixed-size array (`[2]int`) cannot grow, so it is not given `array_append` and declaring it in `patch_ops` fails schema generation. A null appended element is valid only when the element type is nullable: `[]*int` can contain null, `[]int` cannot. Nullable pointer elements are also preserved during whole-array replacement.

A merge patch is bounded like the JSON Patch it stands for: a member nested `d` objects deep is a `d`-segment pointer (`MaxDepth`, E2074), and each top-level member and each nested object that merges counts as one operation against `MaxOperations` (E2066). Scalar and array members do not count — however many an object holds, they compile to one bound value.

## SQL compilation

`BuildPatchSQL(parsed, dialect, opts...)` folds each column's ordered operations into one expression, so repeated operations on a column compose instead of overwriting each other:

| Operations on a column | SQL |
|---|---|
| replace / add | `col = $n` (documents bound as JSON) |
| remove, or a null value | `col = NULL`; a slice or map field stores its empty value (`PathSchema.ClearedValue`) — slice and map columns are usually NOT NULL with an empty default |
| array_append | `array_append(COALESCE(col, '{}'), $n)` (PostgreSQL), `json_insert(COALESCE(col, '[]'), '$[#]', json(?n))` with the element bound as JSON (SQLite), so booleans, numbers and strings keep their JSON types; after a replace the result binds as one array |
| merge-patch object | SQLite `json_patch(COALESCE(col, '{}'), ?n)`; PostgreSQL, per object level, the target as an object (else `{}`) `- $deleted::text[] \|\| $members::jsonb` plus one two-argument `jsonb_build_object($key, …)` per nested object — a level with nested objects names its target once in a scalar subquery that its children read, so the SQL and its arguments grow linearly with the patch and no call nears PostgreSQL's 100-argument limit; after a replace it folds in Go into one bound document |
| move, copy, nested path, `array_append` on a JSON document | E2070 |
| test | E2068 |

Values bind through the dialect's `MarshalValue(value, core.TypeInfo)` (the `SQLDialect` interface): documents as JSON text; on PostgreSQL an array as a typed Go slice — UUIDs as their canonical text — which pgx encodes in every query mode, and on SQLite as JSON text. A hand-built `PathSchema` naming no Go type binds an array typed by its elements.

`parse.ColumnType` / `core.WithColumnTypes` carries the physical storage profile into patch paths and binding. A declared JSON string, byte slice or scalar slice binds its Go JSON encoding; native SQL arrays keep their array representation. Declared JSON uses this encoding even when the Go value implements `driver.Valuer`; native database values keep their driver binding.

A path with no `SQLColumn` fails with E2069. Relation paths go to `PatchSQLResult.RelationOps`; `RenderRelationOp(op, dialect, ownerPKs, opts)` renders their junction statements for the caller's transaction and fails closed on a tenant-scoped junction without `RenderRelationOpOptions.TenantValue` (or explicit `Unscoped`).

A SET clause is not a complete UPDATE. `Resource.BuildUpdate` builds the complete statement ([Resource updates](#resource-updates)); without a resource, add a trusted table/column projection, scoped WHERE, and correctly offset placeholders, and reject or deliberately handle an empty patch before executing SQL. See [the pgx recipe](../parsepgx/recipe_test.go).

## Patches built in code

A server that builds a patch need not encode JSON to parse it back. `MergePatchMap(map[string]any{...})` validates a merge patch given as a map exactly as `ParseMergePatch` validates its body — a nested `map[string]any` merges into a document, any other value (a Go value of the path's type, such as a `time.Time` or a struct, which replaces the document whole) replaces, and null removes, null being what encoding/json writes as null: `nil` or a nil pointer, map or slice, at any depth — and `ValidateOperations([]patch.RawPatchOp{...})` validates JSON Patch operations as `ParsePatch` does. `ServerMergePatchMap` and `ValidateServerOperations` do the same against the server patch schema, for the server's own writes. Never assemble a `patch.ParsedPatch` by hand: its operations would skip validation.

## Resource updates

`Resource.BuildUpdate(dialect, parsed, target, scope, opts...)` compiles a validated patch into the statement that applies it. The WHERE carries the target (`ByID`, `ByIDs` or `ByFilter`), the tenant floor (`Scope.Tenant`, required on a tenant-scoped resource: `ErrTenantRequired`), live rows only, and `Scope.Policy`. Its placeholders continue after the SET clause's.

```go
plan, err := products.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(id),
	parse.Scope{Tenant: tenantID, Policy: policy},
	parse.StampSQL("updated_at", "NOW()"))
if err != nil { /* ErrNoTarget, ErrUpdateScope, ErrTenantRequired or a patch error */ }

tx, err := db.BeginTx(ctx, nil)
rows, err := tx.QueryContext(ctx, plan.Owner.SQL, plan.Owner.Args...) // the target rows as the update left them
if plan.HasRelations() {
	stmts, err := products.RelationStatements(plan, keysOf(rows)) // keys read with products.Key, never the requested ids
	// execute each statement in tx
}
err = tx.Commit()
```

| Patch | `plan.Owner` | `plan.Writes` |
|---|---|---|
| Writes a column | `UPDATE … SET … WHERE … RETURNING <columns>` | true |
| Changes relations only | `SELECT <columns> … WHERE …` plus the dialect's row lock (`FOR UPDATE` on PostgreSQL), so two replacements of one relation cannot interleave their delete and insert | true |
| Empty | `SELECT <columns> … WHERE …` — a scoped read | false |

The rows `Owner` returns are the rows the scope admits. Zero rows means the target is absent, deleted, another tenant's or outside the policy; answer that as not found. Relation statements are rendered for the keys `Owner` returned, tenant-scoped as the update was, and run in its transaction. On PostgreSQL, `parsepgx.Update(ctx, pool, products, plan)` does all of this: it returns the owner rows and, when the patch changes relations, runs owner and relation statements in one transaction (a savepoint inside a caller's `pgx.Tx`).

| Option / target | Effect |
|---|---|
| `ByID(id)`, `ByIDs(ids...)`, `ByFilter(f)` | The rows to change; a zero target is `ErrNoTarget` rather than every row |
| `Stamp(column, at)` | Set `column` to `at` on every update that writes a column; `at` must be non-zero |
| `StampSQL(column, expr)` | Set `column` to the trusted SQL `expr` — the database clock, such as `NOW()` |
| `Scope.Governance`, `Scope.IncludeDeleted` | Refused with `ErrUpdateScope`: an update reaches only its own tenant's live rows |

The stamp column must be one of the resource's columns. `Dialect` is a dialect both the filter and patch compilers speak; `filter.PostgresDialect` and `filter.SQLiteDialect` are.

## JSON documents

A `map[string]any` or `json.RawMessage` field is a JSON document (`jsonb` on PostgreSQL, JSON text on SQLite); a struct field is too, once given `filter_ops`. parse compares and contains documents as a whole:

```text
{"attrs":{"$contains":{"color":"red","dims":{"w":10}}}}    attrs holds these members, at any depth
postgres WHERE products.attrs @> $1::jsonb [{"color":"red","dims":{"w":10}}]
sqlite   (json_each predicates per member: same meaning, no jsonb)

{"tags":{"$contains":["sale"]}}                             a native array holds every listed element
postgres WHERE products.tags @> $1 [[sale]]

{"attrs.color":"red"}
[E2071] field 'attrs.color' is not filterable
```

- `{"attrs":{"color":"red"}}` is **whole-document equality**, not a partial match; use `$contains` for that. A `$gt` inside a `$contains` operand is data, a literal `"$gt"` key.
- `$null` / `$exists` test the column, not a key: a stored JSON `null` and `{}` are both "exists". `{"$contains":{"color":null}}` finds an explicit JSON null; the public grammar has no general key-presence predicate.
- No path into a document can be filtered, compared, sorted or faceted, and a JSON field cannot be sorted or searched at all. A `filter.FieldSchema` with your own `SQLExpr` (`attrs ->> 'color'`) is the escape hatch, with its typing and dialect left to you.
- Updates: a merge patch merges recursively in SQL on both dialects and in `ApplyValidatedPatch` — siblings survive, nested `null` deletes. A JSON Patch to `/attrs/color` is refused (E2076); with a `/attrs/*` path pattern it parses, but SQL refuses nested paths (E2070) and it applies only in Go.
- Typed documents have additional canonicalization and merge limits; see [typed document behavior](#typed-document-behavior).
- Empty JSON Pointer tokens address empty object keys, including intermediate keys, when the path schema allows them.
- Index: `@>` is served by a GIN index (`jsonb_path_ops` is smaller when containment is the only query).

## Typed document behavior

For a document with required integer members `w` and `h`:

| Input | Behavior |
|---|---|
| JSON Patch `replace` with `{"w":9}` | Canonical whole value `{"w":9,"h":0}` |
| Merge patch `{"w":9}` into an existing nonnullable document | Updates `w`; preserves `h` |
| Unknown member | E2077 rather than silently losing a field on Go decode |
| Merge deletes required `h` with `null` | E2077 because Go serialization would recreate `h` |
| Partial merge into a nullable struct or typed-map struct value | Must supply members required if the old value is absent; otherwise E2077 |

Object merges into types with custom JSON encoding or embedded fields are rejected; `json.RawMessage` remains a generic document. Omission-sensitive map merges containing only removals are rejected because deleting the last key would leave SQL `{}` while Go omitted the field. A merge containing a non-null map member guarantees a remaining key and can be accepted. Use whole-document replacement for shapes whose merge cannot be represented consistently.

For declared JSON columns, replacement validates a fresh typed Go JSON decode before writing. Custom-codec subtrees retain their supplied JSON representation; ordinary typed objects still fill required members and honor field omission and `,string`. Integer/text map keys use their canonical JSON names, and colliding key forms are rejected. The stored result is one fresh decode of the actual SQL-bound JSON; validation never stores a custom decoder's transformed value by encoding it again.

A non-SQL-NULL document that decodes to a nil map, slice, pointer or interface is rejected before assignment; use `json.RawMessage` when explicit JSON null must be preserved. Top-level typed nil values follow the same clear semantics as nil input. Request codecs still validate input; they do not replace the declared storage codec.

Keep undeclared document codecs stable across round trips. Do not use top-level `omitempty` / `omitzero` as storage metadata; that enclosing-field omission metadata is not a complete storage schema.

For top-level schema fields, replace/remove can address a field whose current JSON encoding omits it through `omitempty`; these resource-field operations match direct typed application. Nested JSON Pointers keep strict existence rules. A nil pointer target is an application error.

Document fallback decodes only changed top-level fields. Untouched values retain their hidden/private state and do not invoke custom decoders. Touched composite fields are reconstructed from their codec data, so custom nonserialized state in a modified document is not a storage contract.

JSON Patch `test` checks the supplied document shape; missing struct members are not filled in to make a test succeed. A parsed operation is not a guarantee of application success: tests, missing runtime paths, copy budgets, and destination validation can still reject it. Persist read/modify/write results under a transaction, row lock, or version predicate appropriate to your storage.

## Configuration

`PatchSQLOption`s for `BuildPatchSQL`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithStartParam(n)` | int | `0` | Placeholder offset: the first is `n+1`, as with `filter.WithStartParam` |
| `WithAutoTimestamp(col)` | string | none | Also set `col` to the current time (once, even if the patch sets it) |
| `WithAutoTimestampSQL(col, expr)` | string, string | none | `WithAutoTimestamp` with `col` set to the SQL expression `expr` (the database's clock, `NOW()`) instead of a bound time — for a repository whose other writes stamp the database's clock |
| `WithTimeFunc(fn)` | `func() time.Time` | `time.Now().UTC` | Clock for `WithAutoTimestamp` |

Parsers take a `*core.ParserConfig` (`MaxDepth` — pointer segments, and merge-patch nesting; `MaxInputSize`; codec; see [schema/configuration](schema-configuration.md)); `PatchSchema.MaxOperations` (default `50` for generated schemas) bounds operations per patch, merge patches counted as above (E2066).

## Policy

`PatchPolicyConstraints{ForbiddenPaths, ValueConstraints, Validators}` restricts paths (wildcards allowed) and values; `ApplyPatchPolicyConstraints(parsed, pc, subject)` enforces it, and `parse.WithPatchPolicy` applies it inside the typed parser. `NewValueConstraint(pattern, allowed, denied, min, max)` compiles a value rule up front (numbers compare exactly); `PatchPolicyFromWritableFields` derives constraints from a writable-field set.

`copy` / `move` validate the resolved source against destination nullability, normalization, and validators after preceding operations run. Copy budgets count every serialized source, including overwrites. These checks complement authorization; the application still decides who may modify the resource.

[Error catalog](http-errors.md#error-codes) · [Relations](relations.md#update-membership)
