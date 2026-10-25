# parse/sdk

Client SDKs for APIs built on `parse`. Each speaks the server's wire contract — the `filter.Param*` list parameters, `filter.Page` responses, the `{code, message, details}` error body — and performs client-side validation backed by shared fixtures. The server remains authoritative for validation and authorization.

## Subpackages

| Package | Language |
|---|---|
| [TypeScript SDK](ts/README.md) | TypeScript / JavaScript (`@ashtonian/parse`) |

## Invariants

- Filter validation agrees with the Go parser on every case in `testdata/filter_conformance.json` (validity and E-code).
- Pages, schemas and errors are consumed exactly as `testdata/sdk_wire.json` records the Go types serializing them.
- Both fixtures are regenerated from Go (`-update`) and fail their Go tests when stale.

## References

[Package overview](../README.md) · [HTTP contract](../docs/http-errors.md) · [Client limitations](../docs/integrations.md#typescript-client)
