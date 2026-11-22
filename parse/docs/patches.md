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

JSON Patch bodies must be arrays of operations; resource Merge Patch bodies must be objects. A root `null` is rejected rather than treated as an empty update. An operation carries only the value it writes: a `remove` of a field, a `move` and a `copy` have no `PatchOperation.Value`, whatever `value` member the client sent. A `remove`, a merge-patch null and a `move`'s source are null writes, so the path's validators run with null.

Every patch schema — generated, or built with `patch.NewPatchSchema` — has `PatchSchema.MaxOperations` `patch.DefaultMaxOperations` (50) unless it is set (0 is unlimited). Code generators can read this constant without constructing a resource. JSON Patch may repeat an operation on one field up to this count; a body budget must account for each operation's path and value envelope. The parser's raw-input byte limit and declared field-value bounds remain independent limits.

A schema's paths are its own: `NewPatchSchema(resource, paths)` checks every key as a policy checks its patterns (a token is a literal, `*` or `**`), every path schema and its operations, and `AddPath` adds a path — refusing one the schema already has — which every parse that starts after it sees. `Path(key)` looks a key up and `Paths()` yields them in key order. An exact key is matched before a pattern, and patterns most specific first. `patch.IsValidOp` and `patch.Ops` name the recognized operations.

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

Traverse joined errors through `Unwrap() []error` to collect every parse failure. The TypeScript SDK mirrors the constraint names and limits; multiple merge-patch failures throw `SchemaValidationErrors`, whose `errors` contains the individual failures and whose inherited field/code identify the first.

Paths are RFC 6901 JSON Pointers: `/labels/a~1b` addresses the key `a/b`, and `~0` escapes a tilde. Patterns match tokens: `*` matches exactly one token and `**` zero or more, wherever it stands, so `/labels/**` matches `/labels` itself. A token is a literal, `*` or `**`: a policy refuses a pattern whose token holds `*` among other characters (`/internal_*`) when it is built. An empty key is a token, so `/labels/*` admits `/labels/` but not `/labels//x`. Among overlapping patterns the most specific decides: more leading literal tokens, then fewer `**`, then fewer `*`.

| Check | Behavior |
|---|---|
| Allowed path and operation | Reject undeclared paths (E2076) and disallowed operations (E2064) |
| Required value | `add`, `replace`, `test`, and `array_append` require a `value` member; missing is E2075 |
| Explicit null | A value, not an omitted member; `ParseOps` callers set `RawPatchOp.HasValue` to distinguish it |
| Nullability | Top-level null/remove requires a nullable Go type: pointer, slice, map, or interface — not a slice whose `minitems` is above zero, which a clear would empty (E2077) |
| Field value | Normalize exact integers, canonical decimal text, canonical UUIDs/timestamps, array elements, and typed documents; invalid values are E2077 |
| Fixed arrays | Replacement must have exactly the Go array's length; `array_append` cannot grow it |
| Enum, bounds and policy | Enforce the `enum`, `minlen` / `maxlen` and `min` / `max` validators (per element on an array path; null passes, as nullability decides it) and caller-supplied path/value policy; a value outside them is E2077 |
| Element counts | An array a top-level `replace` or `add` sets whole holds the path's `minitems` / `maxitems` (E2077). An element added, appended or removed, and an array moved or copied in, change an array the parser does not see: the store's constraint counts those |
| Transfer source | `move` needs permission to remove its source; top-level source must be nullable, and its validators accept null. Typed source/destination kinds must be compatible, pointers aside |
| Runtime transfer destination | Validate the resolved value against destination nullability and validators after preceding operations run |
| `test` | Exact JSON comparison; `1.0` equals `1`, but missing paths or different document members fail E2077 |

Integral decimal/exponent values remain exact through the supported integer range. Fractional JSON literals that would change on float64 decimal round-trip are rejected rather than rounded. A [decimal field](schema-configuration.md#decimals) takes any JSON number or numeric string exactly, as its canonical text. A `test` operand preserves its supplied shape rather than filling missing struct members.


Default operations per type. `remove` follows nullability: it is dropped for non-nullable fields (and rejected in `patch_ops` on them) and added for nullable scalars such as `*bool` or `*uuid.UUID`. `GeneratorOptions.DefaultPatchOps` replaces a type's defaults as given:

| Type | Default operations |
|---|---|
| string, int, float, decimal, bool, timestamp, date, uuid | replace (+ remove when the Go type is a pointer) |
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
| remove, or a null value | `col = NULL`; a slice or map field stores its empty value — slice and map columns are usually NOT NULL with an empty default |
| array_append | `array_append(COALESCE(col, '{}'), $n)` (PostgreSQL), `json_insert(COALESCE(col, '[]'), '$[#]', json(?n))` with the element bound as JSON (SQLite), so booleans, numbers and strings keep their JSON types; after a replace the result binds as one array |
| merge-patch object | SQLite `json_patch(COALESCE(col, '{}'), ?n)`; PostgreSQL, per object level, the target as an object (else `{}`) `- $deleted::text[] \|\| $members::jsonb` plus one two-argument `jsonb_build_object($key, …)` per nested object — a level with nested objects names its target once in a scalar subquery that its children read, so the SQL and its arguments grow linearly with the patch and no call nears PostgreSQL's 100-argument limit; after a replace it folds in Go into one bound document |
| move, copy, nested path, `array_append` on a JSON document | E2070 |
| test | E2068 |

Values bind through the dialect's `MarshalValue(value, core.TypeInfo)` (the `SQLDialect` interface): documents as JSON text; on PostgreSQL an array as a typed Go slice — UUIDs as their canonical text — which pgx encodes in every query mode, and on SQLite as JSON text; a timestamp on SQLite as its canonical text ([timestamps](schema-configuration.md#timestamps)). A hand-built `PathSchema` naming no Go type binds an array typed by its elements.

`parse.ColumnType` / `core.WithColumnTypes` carries the physical storage profile into patch paths and binding. A declared JSON string, byte slice or scalar slice binds its Go JSON encoding; native SQL arrays keep their array representation. Declared JSON uses this encoding even when the Go value implements `driver.Valuer`; native database values keep their driver binding.

A path with no `SQLColumn` fails with E2069. Relation paths go to `PatchSQLResult.RelationOps`; `RenderRelationOp(op, dialect, ownerPKs, opts)` renders their junction statements for the caller's transaction and fails closed on a tenant-scoped junction without `RenderRelationOpOptions.TenantValue` (or explicit `Unscoped`).

`PatchSQLResult.Assignments` exposes each folded `ColumnAssignment{Column, Expression}` before rendering the comma-separated `SetClause`. Both forms share `Args` and placeholder numbering. `Resource.BuildUpdateBatch` composes these expressions for distinct per-key column patches; its [transaction and exact-preimage contract](operations.md#varied-native-update-batches) applies to every chunk.

A SET clause is not a complete UPDATE. `Resource.BuildUpdate` builds the complete statement ([Resource updates](#resource-updates)); without a resource, add a trusted table/column projection, scoped WHERE, and correctly offset placeholders, and reject or deliberately handle an empty patch before executing SQL. See [the pgx recipe](../parsepgx/recipe_test.go). With `WithChangePredicate()`, `ChangePredicate` ANDed into that WHERE writes only the rows the patch changes; it uses the SET clause's placeholders, so the WHERE's own still start after `ParamCount`.

## Patches built in code

A server that builds a patch need not encode JSON to parse it back. `MergePatchMap(map[string]any{...})` validates a merge patch given as a map exactly as `ParseMergePatch` validates its body — a nested `map[string]any` merges into a document, any other value (a Go value of the path's type, such as a `time.Time` or a struct, which replaces the document whole) replaces, and null removes, null being what encoding/json writes as null: `nil` or a nil pointer, map or slice, at any depth — and `ValidateOperations([]patch.RawPatchOp{...})` validates JSON Patch operations as `ParsePatch` does. `ServerMergePatchMap` and `ValidateServerOperations` do the same against the server patch schema, for the server's own writes. A `patch.ParsedPatch` assembled by hand would skip validation, so it is refused: `ApplyValidatedPatch`, `BuildPatchSQL`, `BuildUpdate` and `ApplyPatchPolicyConstraints` return `patch.ErrUnparsed` for a patch no parser produced.

## Resource updates

`Resource.BuildUpdate(dialect, parsed, target, scope, opts...)` compiles a validated patch into the statement that applies it. The WHERE carries the target (`ByID`, `ByIDs` or `ByFilter`), the tenant floor (`Scope.Tenant`, required on a tenant-scoped resource: `ErrTenantRequired`; normalized once to the [tenant key](relations.md#tenant-key), `ErrTenantInvalid` when a tenant column the resource reaches cannot hold it), live rows only, and `Scope.Policy`. Its placeholders continue after the SET clause's.

```go
plan, err := products.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(id),
	parse.Scope{Tenant: tenantID, Policy: policy},
	parse.StampSQL("updated_at", "NOW()"))
if err != nil { /* ErrNoTarget, ErrUpdateScope, ErrTenantRequired, ErrTenantInvalid or a patch error */ }

tx, err := db.BeginTx(ctx, nil)
rows, err := tx.QueryContext(ctx, plan.Owner.SQL, plan.Owner.Args...) // the target rows as the update left them
if plan.HasRelations() {
	stmts, err := products.RelationStatements(plan, keysOf(rows)) // keys read with products.Key, never the requested ids
	// execute each statement in tx
	reread, err := products.Reread(plan, keysOf(rows))
	// when reread is not nil, its rows in tx replace rows: computed columns read the new pairs
}
err = tx.Commit()
```

| Patch | `plan.Owner` | `plan.Writes` |
|---|---|---|
| Writes a column | `UPDATE … SET … WHERE … RETURNING <columns>` | true |
| Changes relations only | `SELECT <columns> … WHERE …` plus the dialect's row lock (`FOR UPDATE` on PostgreSQL), so two replacements of one relation cannot interleave their delete and insert | true |
| Empty | `SELECT <columns> … WHERE …` — a scoped read | false |

The rows `Owner` returns are the rows the scope admits. Zero rows means the target is absent, deleted, another tenant's or outside the policy; answer that as not found. Under `SkipUnchanged` the `UPDATE` returns only the rows it changed, and `plan.Current` tells an unchanged target from a missing one ([Skipping unchanged rows](#skipping-unchanged-rows)). Relation statements are rendered for the keys `Owner` returned, tenant-scoped as the update was, and run in its transaction. `Owner` runs before them, so a [computed column](queries.md#computed-columns) over a relation's pairs reads the pairs as they were; `Reread(plan, keys)` returns the statement that reads those rows' `Columns` again after the relation statements, held to the update's tenant, or nil when the patch changes no relation or the resource has no computed column. On PostgreSQL, `parsepgx.Update(ctx, pool, products, plan)` does all of this: it returns the owner rows and, when the patch changes relations, runs owner and relation statements in one transaction (a savepoint inside a caller's `pgx.Tx`).

| Option / target | Effect |
|---|---|
| `ByID(id)`, `ByIDs(ids...)`, `ByFilter(f)` | The rows to change; a zero target is `ErrNoTarget` rather than every row |
| `Stamp(column, at)` | Set `column` to `at` on every update that writes a column; `at` must be non-zero |
| `StampSQL(column, expr)` | Set `column` to the trusted SQL `expr` — the database clock, such as `NOW()` |
| `Increment(column)` | Set the integer `column` to one more than it holds on every update that writes a column — a version the server moves; `RETURNING` reports the moved value. Repeat for each counter; composes with the stamp |
| `SkipUnchanged()` | Update only the target rows the patch's columns change, so a row the patch leaves as it is keeps its stamp and counters and `RETURNING` omits it; `plan.Current` reads every target row. See [Skipping unchanged rows](#skipping-unchanged-rows) |
| `Scope.Governance`, `Scope.IncludeDeleted` | Refused with `ErrUpdateScope`: an update reaches only its own tenant's live rows |

An update has one stamp: a later `Stamp` or `StampSQL` replaces an earlier one. The stamp column and each increment column must be one of the resource's columns, an increment column an integer one, none a [computed column](queries.md#computed-columns) (`parse.ReadExpr`), which the table does not hold, and none may be named twice. A patch's own write to a stamped or incremented column — a request's or the server's — is dropped: the server's value wins, and a patch writing only those columns writes nothing. `Dialect` is a dialect both the filter and patch compilers speak; `filter.PostgresDialect` and `filter.SQLiteDialect` are.

### Skipping unchanged rows

A patch that sets what a row already holds still writes it: the stamp and counters move, and a consumer that emits an event per updated row emits one for nothing. `SkipUnchanged()` ANDs the patch's change predicate into the `UPDATE`'s WHERE — null-safe comparisons of each column the patch assigns with the value it assigns, reusing the SET clause's placeholders and binding nothing of their own — so only rows that change are written, stamped, counted and returned:

```text
UPDATE notes SET rank = $1, title = $2, updated_at = NOW(), version = version + 1
WHERE (notes.tenant_id = $3 AND notes.deleted_at IS NULL AND notes.id = $4)
  AND (rank IS DISTINCT FROM $1 OR rank::text COLLATE "C" IS DISTINCT FROM ($1)::text
    OR title IS DISTINCT FROM $2 OR title::text COLLATE "C" IS DISTINCT FROM ($2)::text)
RETURNING notes.id, …

SELECT notes.id, … FROM notes WHERE (notes.tenant_id = $1 AND notes.deleted_at IS NULL AND notes.id = $2) FOR UPDATE
```

The second statement is `plan.Current`: the same scoped WHERE without the predicate, with the dialect's row lock. Run `Current` first, then `Owner`, in one transaction, and split the target rows (`queryNotes` runs a statement and scans its rows):

```go
plan, err := notes.BuildUpdate(d, parsed, parse.ByID(id), scope,
	parse.StampSQL("updated_at", "NOW()"), parse.Increment("version"), parse.SkipUnchanged())
if err != nil {
	return err
}
var current []Note
if plan.Current != nil {
	if current, err = queryNotes(ctx, tx, *plan.Current); err != nil { // locks every target
		return err
	}
}
changed, err := queryNotes(ctx, tx, plan.Owner) // written, stamped and counted: emit events for these
if err != nil {
	return err
}
unchanged, err := notes.Unchanged(changed, current) // matched, left as they were
if err != nil {
	return err
}
if len(changed)+len(unchanged) == 0 {
	return ErrNotFound
}
```

`Current` goes first because its lock holds every target while `Owner` compares them, so the two split one set of rows: the unchanged rows are exactly those the update compared and left. Run the other way, under READ COMMITTED a row another transaction changes or deletes between the two statements would read as it then stands, or not at all. SQLite has no row lock; begin the transaction as a write transaction (`BEGIN IMMEDIATE`) so no other writer commits between the two. `parsepgx.Update` sends `Current` and `Owner` in one batch, in that order. `Unchanged(changed, current)` is the rows of `current` whose keys `changed` lacks: a row the patch changed and moved out of a `ByFilter` target is in `changed` alone. `ExampleSkipUnchanged` prints both statements and is checked by the unit lane.

| Patch, under `SkipUnchanged` | `plan.Owner` | `plan.Current` | What changed |
|---|---|---|---|
| Assigns a column | `UPDATE … WHERE … AND <change predicate> RETURNING <columns>`: the rows it changed | `SELECT <columns> … WHERE …` plus the row lock | Owner's rows; `Unchanged` gives the matched rows it left; neither returns a row: not found |
| Changes relations only | `SELECT <columns> … WHERE …` plus the row lock, as without the option | nil: Owner returns every target row | No column, stamp or counter moves. The relation statements' affected rows count the memberships removed and added: zero means none changed (each junction pair needs a unique key, which `ON CONFLICT DO NOTHING` relies on) |
| Assigns nothing: empty, or server columns alone | `SELECT <columns> … WHERE …` | nil | `plan.Writes` is false: nothing changes |

A patch with columns and relations changes the memberships of every target row: pass the keys of the changed and the unchanged rows to `RelationStatements` in the same transaction. A membership change alone moves no stamp or counter, with the option or without it.

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
| `WithIncrement(col)` | string | none | Also set `col` to `col + 1` (once, even if the patch sets it); repeat for each counter. `col` must be an identifier and differ from the timestamp column |
| `WithTimeFunc(fn)` | `func() time.Time` | `time.Now().UTC` | Clock for `WithAutoTimestamp` |
| `WithChangePredicate()` | — | off | Also render `PatchSQLResult.ChangePredicate`: true for a row the patch's columns change, over the SET clause's own placeholders and without the timestamp or increments; [Skipping unchanged rows](#skipping-unchanged-rows) defines its comparisons. Fails for a dialect other than PostgreSQL and SQLite |

Parsers take a `*core.ParserConfig` (`MaxDepth` — pointer segments, and merge-patch nesting; `MaxInputSize`; codec; see [schema/configuration](schema-configuration.md)); `PatchSchema.MaxOperations` (default `50` for generated schemas) bounds operations per patch, merge patches counted as above (E2066).

## Policy

A policy restricts the paths a patch may write and the values it may write there:

```go
pc, err := patch.NewPatchPolicyConstraints(
	patch.ForbidPaths("/tenant_id", "/internal/**"),
	patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}),
	patch.ValidateWith(func(op *patch.PatchOperation, subject *patch.PolicySubject) error { return nil }),
)
```

`NewPatchPolicyConstraints` checks the policy when it is built, so a bad pattern is a startup error and never a request's: it refuses a path pattern whose token holds `*` among other characters, a value constraint given twice for one pattern, a `ValueConstraint.Pattern` that does not compile, and a nil validator. The policy is immutable. `ApplyPatchPolicyConstraints(parsed, pc, subject)` enforces it and records it on the patch (`AuditInfo`), and `parse.WithPatchPolicy(pc, subject)` applies it inside a `Resource`'s `ParsePatch`, `ParseMergePatch`, `MergePatchMap`, `ValidateOperations` and `ApplyPatch`; `subject` is a `*patch.PolicySubject` for the validators. `AllowedValues` and `DeniedValues` compare numbers by value, a decimal path's exactly; `PatchPolicyFromWritableFields` builds a policy forbidding every path a writable-field set leaves out.

Policy judges what each operation writes, not its envelope. A whole value writes every path below its own, a merge object writes the members it names, a `remove` writes null whatever `value` it carries, a `move` writes its source as well as its destination, and a `test` writes nothing.

- **Forbidden paths (E2084).** An operation is refused when a pattern matches its path. One that writes or reads a path whole — `replace`, `add`, `remove`, `array_append`, a relation's membership, a `move`'s or `copy`'s destination and source, a `test`'s path, a merge member whose value is not an object — is also refused when a pattern can match a path below it, so `{"op":"replace","path":"/attrs","value":{}}` is refused under `/attrs/secret`. A merge object is refused when it names a member chain a pattern matches, null included: `{"attrs":{"secret":null}}`. A pattern forbids the paths it matches and nothing inside them: `/attrs/secret` leaves `/attrs/secret/x` writable, `/attrs/secret/**` does not.
- **Value constraints (E2085).** Each path an operation writes is judged by its constraint: the path's own key, else the most specific pattern that matches it. A constraint below the operation's path judges the member at that sub-path of the written value: for a whole-value write an absent member reads as null, for a merge an absent member is untouched. On an array path a constraint judges each element, whichever operation writes them. Null is judged by `AllowedValues` and `DeniedValues`; `Pattern` judges strings and `Min` / `Max` numbers, and each refuses any other value. A `move` or `copy` into a path that has a constraint, or a constraint below it, is refused, since its value is read only as the patch applies; so is a merge into a path with a constraint of its own.
- **Affected paths.** `ParsedPatch.AffectedPaths`, and `AuditInfo`'s `affected_paths`, list every path the operations write: a `move`'s source included, a `test`'s path not.

`copy` / `move` validate the resolved source against destination nullability, normalization, and validators after preceding operations run. Copy budgets count every serialized source, including overwrites. These checks complement authorization; the application still decides who may modify the resource.

[Error catalog](http-errors.md#error-codes) · [Relations](relations.md#update-membership)
