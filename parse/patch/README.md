# parse/patch

JSON Patch and Merge Patch validation, SQL SET compilation, relation membership statements, and patch policy. Parsing checks the schema; application can still fail on runtime paths, tests, copy budgets, or destination constraints.

## Usage and configuration

| Guide | Contents |
|---|---|
| [Validation, typed documents, SQL compilation, and options](../docs/patches.md) | Consumer guide |
| [Relation statements and transaction requirements](../docs/relations.md) | Consumer guide |
| [Error catalog](../docs/http-errors.md) | Consumer guide |

Start with the [runnable package example](../README.md#quick-start). The focused [patch example](example_test.go) is compiled and output-checked in the unit suite.

## API reference

- Schemas: `GeneratePatchSchema[T]`, `GenerateServerPatchSchema[T]` (adds the `parse:"server"` fields, for the server's own writes), `NewPatchSchema`, `PatchSchema` (`AddPath`, `Path`, `Paths`, `MaxOperations`), `DefaultMaxOperations`, `PathSchema` (`AllowsOp`, `Nullable`; `EnumValues` and `Bounds` advertise its value tags), `Validator`, `DefaultPatchOps`, `IsValidOp`, `Ops`
- Parsing: `NewParser(schema, cfg)` (`Parse`, `ParseOps`), `ParseMergePatch(body, schema, cfg)`, `ParseMergePatchMap`, `ParsedPatch` (`AuditInfo`), `PatchOperation`, `RawPatchOp`, `PatchOp`; `ErrUnparsed` for a `ParsedPatch` no parser produced
- Applying to Go values: `NewApplier[T](gen, cfg)` (`Apply`), the in-memory counterpart of `BuildPatchSQL`
- SQL: `BuildPatchSQL`, `SQLDialect`, `PatchSQLResult` (`Assignments`, `ChangePredicate`), `ColumnAssignment` (`Column`, `Expression`), `PatchSQLOption` (`WithStartParam`, `WithAutoTimestamp`, `WithAutoTimestampSQL`, `WithIncrement`, `WithTimeFunc`, `WithChangePredicate`)
- Relations: `RelationPatchOp`, `RelationVerb`, `RenderRelationOp`, `RenderRelationOpOptions` (`Owner` / `RelationOwner` and `TargetVisible`: guarded statements for a relation with a target table), `RenderedRelationStatement`, `RelationPath`
- Policy: `NewPatchPolicyConstraints` (`ForbidPaths`, `ConstrainValues`, `ValidateWith`: `PolicyOption`), `PatchPolicyConstraints`, `ValueConstraint`, `PatchValidator`, `PolicySubject`, `ApplyPatchPolicyConstraints`, `PatchPolicyFromWritableFields`
- Errors: `ECode*` (aliases of `core.Code*`, each code's one definition), `Codes`; `ConstraintError` (`Constraint`, `Limit`: a declared constraint's E2077 cause), `NewConstraintError`

## Code map

| File | Responsibility |
|------|----------------|
| `patch.go` | `Parser` — RFC 6902 validation and value normalization |
| `merge.go` | `ParseMergePatch` — RFC 7396 |
| `apply.go` | `Applier` — a parsed patch applied to a Go value: direct field sets, or T's JSON document; `ErrUnparsed` |
| `apply_jsonpatch.go` | RFC 6902 runs through json-patch with one copy budget, and its empty-key escape |
| `schema.go` | `PatchOp` and the operation set, `PatchSchema` and its versioned path set, `PathSchema`, `PatchOperation`, `ParsedPatch` |
| `generate.go` | Schema generation from struct tags; default operations |
| `validate.go` | The value check both wire formats run (`checkValue`): operation, nullability, normalization, validators, element counts; `Validator`, `ConstraintError` |
| `validators.go` | Path validators from value tags: `enum`, `minlen` / `maxlen`, `min` / `max`, per element on an array path (`minitems` / `maxitems` count an array set whole: `checkItems` in `patch.go`) |
| `sql.go` | `BuildPatchSQL` — per-column fold, merge and append compilation; the change predicate (`WithChangePredicate`) |
| `relation_sql.go` | Relation patch rendering with junction tenancy |
| `policy.go` | Patch policy and value constraints, judged by what operations write (`writes.go`) |
| `pattern.go` | Path patterns: token check, matching (`*` one token, `**` zero or more), specificity order |
| `writes.go` | The paths an operation writes and reads, and the members a written value holds |
| `errors.go` | E-codes |

## Dependencies

- *Imports*: `parse/core`, `parse/internal/{dialect,jsonmerge,rfc6902}`, `jsonvalue` (declared JSON columns decode as rows scan them), `github.com/evanphx/json-patch/v5` (RFC 6902 application)
- *Imported by*: `parse`, the example server, and servers that apply patches themselves
- *Package boundary*: standalone Parse module

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)
