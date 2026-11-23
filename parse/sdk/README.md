# parse/sdk

Client SDKs for APIs built on `parse`. Each speaks the server's wire contract — the `filter.Param*` list parameters, `filter.Page` responses, the `{code, message, details}` error body or an RFC 9457 problem document — and performs client-side validation backed by shared fixtures. The server remains authoritative for validation and authorization.

## Subpackages

| Package | Language |
|---|---|
| [TypeScript SDK](ts/README.md) | TypeScript / JavaScript (`@ashtonian/parse`) |

## Invariants

- Filter, sort and list-request validation agrees with the Go server on every case in `testdata/filter_conformance.json`, and patch validation on every case in `testdata/patch_conformance.json` (validity and E-code), each validated against the schema documents the corpus records the registry serving.
- Every request the client sends equals one `testdata/sdk_wire.json` records parse's Go client building, and every answer it reads is one parse's handlers gave that request: pages forward and backward, a total, a list that matches nothing (`items: []`), a list over POST, facet counts, a patch of one row and of several with a key that updated none, the schema documents, a 404 and a 422 error body.
- The fixtures are regenerated from Go (`-update`) and fail their Go tests when stale; Go tests also require the filter corpus to hold a valid and an invalid case for every operator and every filterable type.

## References

[Package overview](../README.md) · [HTTP contract](../docs/http-errors.md) · [Client limitations](../docs/integrations.md#typescript-client)
