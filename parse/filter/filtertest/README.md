# parse/filter/filtertest

The filter conformance fixture. Each family of cases is an entity with a field of every type it covers, its rows, and the rows a backend must return for each filter, each server policy, each facet and each sort. The SQL dialects, through PostgreSQL and SQLite, and the in-memory evaluator (`filter.Evaluate`) run the same cases, so an operator, a policy and an order mean one thing on every backend. A backend for parse's filter grammar, in this module or outside it, is conformant when it passes every family.

## Configuration

The fixture holds fixed data and reads no environment. Its parsers pin the clock to `Now`, an offset from `Base`, so relative-time tokens resolve the same on every run. `SetConfig` is the parser configuration the set cases need: `$in` / `$nin` sets of every size in `SetSizes`, up to an authorized-ID set of 100 000 members.

## External dependencies

`parse/core` and `parse/filter`; the standard library's `testing`, through which every `Run*` function reports; `github.com/shopspring/decimal` for the decimal family's rows. A backend brings its own database.

## API

A family is its rows, its schema, its cases and a `Run*` function that parses each case and asks the backend for the IDs it matches:

| Family | Rows | Schema | Cases and expectations | Run |
|---|---|---|---|---|
| Every operator on every type | `Record`, `Records` | `Schema`, `Parser` | `Cases`, each a `Case`: a filter and the IDs it matches, or `ParseErr`, the E-code the parser refuses it with | `Run` |
| Decimals | `DecimalRecord`, `DecimalRecords` | `DecimalSchema` | `DecimalCases`; `DecimalOrders`, `DecimalFacets`, `DecimalFacetOrders` | `RunDecimals` |
| Arrays | `ArrayRecord`, `ArrayRecords` | `ArraySchema` | `ArrayCases`; `ArrayFacets`, `ArrayFacetOrders` | `RunArrays` |
| Declared JSON paths | `PathRecord` (with the typed document `Settings` and its `Window`), `PathRecords` | `PathSchema`, `PathParser` | `PathCases`; `PathOrders`, `PathFacets`, `PathFacetOrders` | `RunPaths` |
| `$in` / `$nin` sets | `Records`, `PathRecords` | `Schema`, `PathSchema` with `SetConfig` | `SetCases`, `PathSetCases`, over `SetSizes` | `RunSets`, `RunPathSets` |
| Geo | `GeoRecord`, `GeoRecords` | `GeoSchema` | `GeoCases` | `RunGeo` |
| Quoted identifiers | `QuotedRecord`, `QuotedRecords` | `QuotedSchema` | `QuotedCases` | `RunQuoted` |
| `$ilike` case | `FoldRecord`, `FoldRecords` | `FoldSchema` | `FoldCases`, which every backend answers alike; `FoldUnicodeCases`, which lower-case a non-ASCII letter, for PostgreSQL under `pg_c_utf8` and memory | `RunFold`, the Unicode cases with `unicode` set |
| Number keys | `NumberKeyRecord`, `NumberKeyRecords` | `NumberKeySchema` | `NumberKeyFacets` | the caller's facet and keyset tests |
| Timestamps | `TimestampRecord`, `TimestampRecords` | `TimestampSchema` | `TimestampFilters`, `TimestampOrders`, `TimestampFacets` | the caller's filter, sort and facet tests |
| Memberships | `Records` and the lookup tables `LookupTables` (`LookupTable`, `LookupColumn`) | `Schema` | `MembershipCases`, each a `MembershipCase`; `Lookups`, the reference `filter.MembershipLookup` over the tables | `RunMembership` |
| Server policies | `Records`, `LookupTables` | `Schema`, `SearchSchema` | `PolicyCases`, each a `PolicyCase`: a request filter, a policy, an optional search, and the IDs or the error | `RunPolicy` |

A backend is a `Backend`, a function from a parsed filter to the IDs it matches over the family's rows, or a `PolicyBackend`, which receives a `PolicyQuery` (the parsed request filter, the policy and the search). `RunPolicy` skips the cases a memory backend cannot evaluate (raw SQL, search) unless it is told the backend is SQL. A `FacetOrder` is a facet's expected bucket order under a limit.

## Build, run and test

The fixture has no tests of its own beyond `arrays_test.go`; it is exercised by the suites that run it:

```bash
go test -tags=unit ./filter/...                  # Evaluate against every family
go test -tags=unit .                              # the SQL each family compiles to
go test -tags=integration ./...                   # PostgreSQL and SQLite: see ../../docs/operations.md
```

## Operations

A family failing on one backend and passing on the others is a disagreement between that backend and parse's semantics: the subtest names the family and the case, and the message gives the IDs the backend returned against the IDs every backend must. Check the backend's rendering of the operator in the case first, then the case's rows. A case is never changed to fit a backend.

## Security

The fixture is test data: it holds no credentials and opens no connection. The policy family is where a backend proves it applies a server policy, tenant floors and soft-delete masking included, exactly as the SQL builder does; a backend that skips it can return rows a policy forbids.

## Code map

| File | Responsibility |
|---|---|
| `filtertest.go` | `Record`, `Records`, `Base`, `Now`, `Case`, `Backend`, `Parser`, `Schema`, `Run` |
| `cases.go` | `Cases`: the operator × type matrix |
| `decimals.go` | The decimal family |
| `arrays.go` | The array family |
| `paths.go` | The declared JSON path family |
| `sets.go` | The `$in` / `$nin` set family, `SetSizes`, `SetConfig` |
| `geo.go` | The geo family |
| `quoted.go` | The quoted-identifier family |
| `fold.go` | The `$ilike` case family |
| `numbers.go` | Number keys |
| `timestamps.go` | Timestamps |
| `membership.go` | Lookup tables, `MembershipCases`, `Lookups`, `RunMembership` |
| `policy.go` | `PolicyCases`, `PolicyQuery`, `PolicyBackend`, `SearchSchema`, `RunPolicy` |
| `facets.go` | `FacetOrder` |
