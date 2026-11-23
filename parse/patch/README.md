# parse/patch

JSON Patch and Merge Patch validation, SQL SET compilation, relation membership statements, and patch policy. Parsing checks the schema; application can still fail on runtime paths, tests, copy budgets, or destination constraints.

## Usage and configuration

| Guide | Contents |
|---|---|
| [Validation, typed documents, SQL compilation, and options](../docs/patches.md) | Consumer guide |
| [Relation membership writes](../docs/relations.md#change-membership) | Consumer guide |
| [Junction statements and transaction requirements](../docs/pair-tables.md#update-membership) | Consumer guide |
| [Error catalog](../docs/http-errors.md) | Consumer guide |

Parsers read their limits from `core.ParserConfig`; SQL compilation takes `PatchSQLOption`s ([patch configuration](../docs/patches.md#configuration)) and a policy its `PolicyOption`s ([policy](../docs/patches.md#policy)).

Start with the [runnable package example](../README.md#quick-start). The focused [patch example](example_test.go) is compiled and output-checked in the unit suite.

## API reference

- Schemas: `GeneratePatchSchema[T]`, `GenerateServerPatchSchema[T]` (adds the `parse:"server"` fields, for the server's own writes), `NewPatchSchema`, `PatchSchema` (`AddPath`, `Path`, `Paths`, `MaxOperations`, `Clone`), `DefaultMaxOperations`, `PathSchema` (`AllowsOp`, `Nullable`; `EnumValues` and `Bounds` advertise its value tags), `Validator`, `DefaultPatchOps`, `IsValidOp`, `Ops`
- Parsing: `NewParser(schema, cfg)` (`Parse`, `ParseOps`), `ParseMergePatch(body, schema, cfg)`, `ParseMergePatchMap`, `ParsedPatch` (`AuditInfo`, `ParsedFor`: whether a parser validated it against a schema), `PatchOperation`, `RawPatchOp`, `PatchOp` (its `PatchOp*` constants, one per RFC 6902 operation and `array_append`); `ErrUnparsed` for a `ParsedPatch` no parser produced
- Applying to Go values: `NewApplier[T](gen, cfg)` (`Apply`; `CheckSchema`: refuses a schema with a path reaching a field promoted through an unexported embedded pointer, which no patch can write), the in-memory counterpart of `BuildPatchSQL`
- SQL: `BuildPatchSQL`, `SQLDialect`, `PatchSQLResult` (`Assignments`, `ChangePredicate`), `ColumnAssignment` (`Column`, `Expression`), `PatchSQLOption` (`WithStartParam`, `WithAutoTimestamp`, `WithAutoTimestampSQL`, `WithIncrement`, `WithTimeFunc`, `WithChangePredicate`)
- Relations: `RelationPatchOp` (`Values`: a target key per value, a `core.Key` for a key of several columns), `RelationVerb` (`RelationVerb*`: add, remove, replace), `RenderRelationOp` (the selected owner keys as `[]core.Key`, written for a `RelationDialect`, which `filter.Dialect` is: owner and target keys bind as one set each, every column compared as a filter compares its type (`RelationSchema.SourceTypes`, `TargetTypes`), and an insert skips a pair the table holds under another spelling, a decimal `"1.50"` in a text column for `1.5`), `RenderRelationOpOptions` (`Owner` / `RelationOwner` and `TargetVisible`: guarded statements for a relation with a target table; `RelationOwner.Key` lists the owner's key columns), `RenderedRelationStatement`, `RelationPath` (a membership path from its relation and the target key's columns), `RelationKey` (a target key column's Go type and wire form; a key of several columns takes each key as an array of a value per column, in key order)
- Policy: `NewPatchPolicyConstraints` (`ForbidPaths`, `ConstrainValues`, `ValidateWith`: `PolicyOption`), `PatchPolicyConstraints`, `ValueConstraint`, `PatchValidator`, `PolicySubject`, `ApplyPatchPolicyConstraints`, `PatchPolicyFromWritableFields`
- Errors: `ECode*` (aliases of `core.Code*`, each code's one definition), `Codes`, `ErrSQLColumnMissing` (a schema path with no column, the server's: not a `ParseError`); `ConstraintError` (`Constraint`, `Limit`: a declared constraint's E2077 cause), `NewConstraintError`

## Code map

| File | Responsibility |
|------|----------------|
| `patch.go` | `Parser` — RFC 6902 validation and value normalization |
| `merge.go` | `ParseMergePatch` — RFC 7396 |
| `apply.go` | `Applier` — a parsed patch applied to a Go value: direct field sets, or T's JSON document, written to a working copy that copies each embedded parent before writing through it; `CheckSchema` refuses a path through an unexported embedded pointer, whose parent the copy can neither allocate nor copy; `ErrUnparsed` |
| `apply_jsonpatch.go` | RFC 6902 runs through json-patch with one copy budget, and its empty-key escape |
| `schema.go` | `PatchOp` and the operation set, `PatchSchema` and its versioned path set, `PathSchema`, `PatchOperation`, `ParsedPatch` |
| `generate.go` | Schema generation from struct tags, refusing a patch or server field promoted through an unexported embedded pointer; default operations |
| `validate.go` | The value check both wire formats run (`checkValue`): operation, nullability, normalization, validators, element counts; `Validator`, `ConstraintError` |
| `validators.go` | Path validators from value tags: `enum`, `minlen` / `maxlen`, `min` / `max`, per element on an array path (`minitems` / `maxitems` count an array set whole: `checkItems` in `patch.go`) |
| `sql.go` | `BuildPatchSQL` — per-column fold, merge and append compilation; the change predicate (`WithChangePredicate`) |
| `sql_member.go` | Document-member add/replace/remove compiled against the stored document (`jsonb_set`/`#-`, `json_set`/`json_remove`); their RFC 6902 preconditions, settled from the patch's own operations or left to the row (`Precondition`, `RenderPreconditions`) |
| `relation_sql.go` | Relation statements: one delete and one insert builder, owners and values each one set parameter compared by type (`RelationDialect.KeyIn`), the held-pair guard for columns whose stored form is not their value, junction tenancy and target visibility |
| `policy.go` | Patch policy and value constraints, judged by what operations write (`writes.go`) |
| `pattern.go` | Path patterns: token check, matching (`*` one token, `**` zero or more), specificity order |
| `typed_json.go` | A merge patch's document values checked and normalized against the Go type of the member they write, map keys decoded and named as encoding/json does; a JSON Patch member's name and value held to that type (`checkDocumentMember`) |
| `writes.go` | The paths an operation writes and reads, and the members a written value holds |
| `errors.go` | E-codes |

## Build, run and test

From the module directory (`parse/`):

```bash
go test -tags=unit ./patch/...           # validation, typed apply, policy, SQL compilation; the patch example
PARSE_POSTGRES_DSN=postgres://… go test -tags=integration -count=1 ./patch/...    # compiled patches executed on PostgreSQL
```

## Operations

patch validates operations and compiles them to SQL; it runs no statement and has no health check, metrics or logs of its own. Its failures:

- A `*core.ParseError` is the request's fault: an invalid operation or value (E2064–E2077), a policy refusal (E2084, E2085), a relation value it cannot take (E2118, E2119), and from SQL compilation a test or transfer it cannot render (E2068, E2070, E2117) or a document-member operation the patch's own operations make fail (E2077). Joined failures name each member's JSON Pointer ([errors](../docs/http-errors.md#error-codes)).
- `ErrUnparsed` is the server's: a `ParsedPatch` no parser produced reached a compiler. `ErrSQLColumnMissing` is the schema's: a path with no SQL column.
- A patch is bounded by `MaxOperations` (`DefaultMaxOperations`), a relation operation by `MaxRelationValues` (`DefaultMaxRelationValues`) and typed copies by `MaxPatchCopyBytes` ([patch configuration](../docs/patches.md#configuration)).

## Security

A path is checked against the schema before anything compiles, and a request's path text is never used as an identifier. Policy (`ForbidPaths`, `ConstrainValues`, `ValidateWith`) judges every path an operation writes, a `move`'s source included; forbid the system columns a client must not write, such as the tenant. Membership writes pair an owner only with targets the request may relate. See [policy](../docs/patches.md#policy) and [trust boundaries](../docs/operations.md#trust-boundaries).

## Dependencies

- *Imports*: `parse/core`, `parse/internal/{dialect,jsonmerge,rfc6902}`, `gowalker` (type text in errors), `jsonvalue` (declared JSON columns decode as rows scan them), `github.com/evanphx/json-patch/v5` (RFC 6902 application)
- *Imported by*: `parse`, the example server, and servers that apply patches themselves
- *Package boundary*: standalone Parse module

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)
