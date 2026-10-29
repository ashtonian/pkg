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

- Schemas: `GeneratePatchSchema[T]`, `GenerateServerPatchSchema[T]` (adds the `parse:"server"` fields, for the server's own writes), `ServerFieldName`, `PatchSchema`, `PathSchema` (`AllowsOp`, `Nullable`; `EnumValues` and `Bounds` advertise its value tags), `DefaultPatchOps`, `ValidPatchOps`
- Parsing: `NewParser(schema, cfg)` (`Parse`, `ParseOps`), `ParseMergePatch(body, schema, cfg)`, `ParsedPatch` (`AuditInfo`), `PatchOperation`, `RawPatchOp`, `PatchOp`
- SQL: `BuildPatchSQL`, `SQLDialect`, `PatchSQLResult`, `PatchSQLOption`
- Relations: `RelationPatchOp`, `RelationVerb`, `RenderRelationOp`, `RenderRelationOpOptions` (`Owner` / `RelationOwner` and `TargetVisible`: guarded statements for a relation with a target table), `RenderedRelationStatement`, `RelationPath`
- Policy: `PatchPolicyConstraints`, `ValueConstraint`, `NewValueConstraint`, `PatchValidator`, `ApplyPatchPolicyConstraints`, `PatchPolicyFromWritableFields`
- Errors: `ECode*`, `Codes`

## Code map

| File | Responsibility |
|------|----------------|
| `patch.go` | `Parser` — RFC 6902 validation and value normalization |
| `merge.go` | `ParseMergePatch` — RFC 7396 |
| `schema.go` | `PatchOp`, `PatchSchema`, `PathSchema`, `PatchOperation`, `ParsedPatch` |
| `generate.go` | Schema generation from struct tags; default operations |
| `validators.go` | Path validators from value tags: `enum`, `minlen` / `maxlen`, `min` / `max`, per element on an array path |
| `sql.go` | `BuildPatchSQL` — per-column fold, merge and append compilation |
| `relation_sql.go` | Relation patch rendering with junction tenancy |
| `policy.go` | Patch policy and value constraints |
| `errors.go` | E-codes |

## Dependencies

- *Imports*: `parse/core`, `parse/internal/{dialect,jsonmerge}`
- *Imported by*: `parse`, `forge/dal/*`, `forge/feature`
- *Package boundary*: standalone Parse module

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)
