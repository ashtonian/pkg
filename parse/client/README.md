# parse/client

## Overview

The Go side of parse's wire, for SDKs that call a parse server. It builds requests in parse's grammar: typed filters, sort orders, list parameters, a row's item path, merge-patch members and JSON Patch operations. It decodes the answers: a list's page with its page info, includes and facets, a facets answer, a patch answer, a refused request's error with its code, and the schema document.

It is a module of its own, `github.com/ashtonian/pkg/parse/client`, and requires nothing beyond the standard library: a program that imports it lists it alone in its module graph, and links no parse server package and no `net/http`. It is held to the same filter and patch corpora as the TypeScript SDK, and to the exchanges parse records from its handlers ([Testing](#testing)).

## Quick start

Requires Go 1.27 or later. Install with `go get github.com/ashtonian/pkg/parse/client@latest`.

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ashtonian/pkg/parse/client"
)

// Device is a row of the devices resource.
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func main() {
	// The resource's catalog, usually generated from its schema document.
	devices := struct {
		Name     client.SortableText
		Enabled  client.Field[bool]
		LastSeen client.SortableTime
	}{client.NewSortableText("name"), client.NewField[bool]("enabled"), client.NewSortableTime("last_seen")}

	// GET /devices?…: the list request.
	q, err := client.ListParams{
		Filter:   client.And(devices.Name.Like("pump-%"), devices.Enabled.Eq(true), devices.LastSeen.Gte(client.Ago(24*time.Hour))),
		Sort:     client.Sort{devices.LastSeen.Desc(), devices.Name.Asc()},
		PageSize: 50,
	}.Values()
	if err != nil {
		panic(err)
	}
	fmt.Println(q.Get("filter"))
	fmt.Println(q.Get("sort"))

	// Its answer.
	var page client.Page[Device]
	answer := `{"items":[{"id":"d1","name":"pump-1"}],"page_info":{"has_next_page":true,"has_prev_page":false,"end_cursor":"c1"}}`
	err = json.Unmarshal([]byte(answer), &page)
	if err != nil {
		panic(err)
	}
	fmt.Println(page.Items[0].Name, page.PageInfo.HasNextPage, page.PageInfo.EndCursor)

	// A refused request's answer.
	err = client.DecodeError(http.StatusUnprocessableEntity, []byte(`{"code":"E2071","message":"field 'colour' is not filterable"}`))
	fmt.Println(errors.Is(err, client.CodeUnknownField))
	fmt.Println(err)
}
```

```text
{"$and":[{"name":{"$like":"pump-%"}},{"enabled":{"$eq":true}},{"last_seen":{"$gte":"now-1d"}}]}
-last_seen,name
pump-1 true c1
true
client: E2071 (HTTP 422): field 'colour' is not filterable
```

`example_test.go` runs this request, a merge patch, a JSON Patch document, a page walk and a decoded error with checked output.

## Field types

A catalog field's type follows the operators its value type can declare on the server, so a misspelt field or an operand of the wrong type does not compile. The server still refuses an operator a field does not allow (E2072).

| Type | Value | Filter methods |
|------|-------|----------------|
| `Field[V]` | key, enum, boolean | `Eq`, `Ne`, `In`, `Nin`, `Null`, `NotNull` |
| `Ordered[V]` | number, decimal (`Ordered[decimal.Decimal]`: any type that marshals its exact text) | `Field`'s, `Gt`, `Gte`, `Lt`, `Lte` |
| `Text` | string | `Ordered`'s, `Like`, `ILike`, `Contains` |
| `Time` (`Ordered[Moment]`) | timestamp | `Ordered`'s, with `At(t)`, `Now()`, `Ago(d)` or `FromNow(d)` operands |
| `Ordered[Quoted[T]]`, `Field[Quoted[T]]` | a bool, integer or float the server declares `json:",string"` (schema `"wire": "string"`) | as for `T`, with `Quote(v)` operands sent as JSON strings (`"9007199254740993"`) |
| `Document[V]` | JSON document | `Eq`, `Ne`, `Contains`, `HasKey`, `Null`, `NotNull` |
| `Array[E]`, `OrderedArray[E]`, `TextArray` | array of keys / numbers, decimals, timestamps / strings: a column or a declared document member | `Contains`, `Null`, `NotNull`, `Some`, `None` (a condition on one element); `Element()` is a `Field[E]`, `Ordered[E]` or `Text` whose comparisons, combined by `And`, `Or` and `Not`, build that condition; `Append(e)`, an `array_append` operation on an array column |
| `Elements[V]` | JSON array of objects | `Document`'s, `Some`, `None` (a filter over element members) |
| `Relation[K]` | has-many or many-to-many | `Field`'s on target keys, `Some`, `None` (a filter over the target); `Replace`, `Add`, `Remove` membership operations |
| `Reference[K]` | belongs-to | `Field`'s on the target key, `Is`, `IsNot` |
| `Geo` | geographic | `Near(point, meters)` (meters positive and finite), `Within(shape)`, `Intersects(shape)`, `Null`, `NotNull` |

`SortableField[V]`, `SortableOrdered[V]`, `SortableText` and `SortableTime` add `Asc` and `Desc`; a sort-only field is a `SortKey`.

The value types are the caller's: an operand is encoded with encoding/json and its own `MarshalJSON`, so a date, an interval, a MAC address or a UUID is whatever type marshals the text the server reads (a `string` will do), an address a `netip.Addr` and a network a `netip.Prefix`.

An element condition names no field: `tags.Some(client.Or(tag.Like("a%"), tag.Eq("b")))`, with `tag := tags.Element()`, encodes `{"tags":{"$some":{"$or":[{"$like":"a%"},{"$eq":"b"}]}}}`. The empty filter is any element: `tags.Some(client.Filter{})`.

A relative `Moment` is sent as the server's token (`now-1d`) and resolved by the server's clock, so the client's clock never skews it.

A `Quoted[T]` travels as encoding/json writes a `json:",string"` field: its JSON text inside a JSON string. Use it for operands, merge-patch members (`client.Nullable[client.Quoted[int64]]`) and JSON Patch values (`client.Replace("/id", client.Quote(int64(42)))`) of such a field, and to decode its responses, so a 64-bit integer stays exact end to end.

## Encoding

- **Filter** encodes every condition in operator form (`{"name":{"$eq":"a"}}`), so a document value whose members start with `$` is never read as operators. `And` and `Or` flatten nested groups, so a filter built up in a loop stays within the server's nesting depth. The zero `Filter` matches every row and encodes as `{}`.
- **ListParams** encodes the list parameters the server's `ParseList` reads (`filter`, `sort`, `cursor`, `page_size`, `include_total`, `include_deleted`, `search`, `facets`, `include`, `fields`) and leaves zero members out: `Values` as a query string, `Body` as the JSON document the server's `ParseListBody` reads from a POST, for a request too large for a URL.
- **Nullable[T]** is a merge-patch (RFC 7396) member: with the `omitzero` tag option the zero value is absent, `Null[T]()` clears the field, `Set(v)` writes it. Tag every `Nullable` member `omitzero`: `omitempty` does not omit a struct, so an absent member under it (or under no option) would encode as `null` and clear its column; encoding one is `ErrAbsentMember` instead. `Set` of a nil slice or map writes the empty one, and `Set` of a nil pointer is `Null`, since that is what it encodes.
- **Operation** is a JSON Patch (RFC 6902) operation; a `[]Operation` is the document. `Append(path, element)` and `Array[E].Append(e)` write parse's `array_append` extension, which appends one element to an array column atomically. `Pointer(tokens...)` escapes member names into a JSON Pointer.
- **ItemPath(key...)** is the path of one row below its collection's path, the item route the server registers: a segment per key value, in key order, each escaped on its own (`url.PathEscape`), so `ItemPath("w1", "a/b")` is `w1/a%2Fb`. A value is written as the server's `ParseKey` reads its column: a string as itself, an integer in decimal, a float as its JSON number, a bool as `true` or `false`, a `Quoted` value as its value, and a `MarshalText` value (a UUID, a decimal, a `time.Time`) as its text.

## Decoding

The answer types mirror the JSON the server writes, member for member, so each decodes the recorded answer and encodes back to it.

| Type | Answer | Members |
|------|--------|---------|
| `Page[T]` | a list (GET or POST of the collection) | `Items` (never null), `PageInfo`, `Relations` (`_relations`: related keys by relation, then by the owner's key text), `Truncated` (`_truncated`: the owners whose related keys were cut at the server's cap), `Facets` |
| `PageInfo` | a page's position | `HasNextPage`, `HasPrevPage`, `StartCursor`, `EndCursor` (send one back as `ListParams.Cursor`), `TotalCount` (with `IncludeTotal`) |
| `FacetsAnswer` | the facets of a list (OPTIONS of the collection) | `Facets`: a `[]FacetBucket` (`Key`, `Count`) by field |
| `PatchAnswer[T]` | a patch of one row or of a batch | `Updated` (the rows as the update left them; none means not found), `Failed` (`[]PatchFailure`: `ID`, `Code`, `Error` of each batch key that updated no row) |
| `Error` | a refused request (`DecodeError`) | `Status`, `Code`, `Message`, `Path`, `Index` (a JSON Patch operation's position), `Details` |
| `Schema`, `SchemaList` | the schema routes | the document's `Key`, `Filter` (`FilterSchema`, `FilterFieldSchema`, `RelationSchema`, `GeoConfigSchema`), `Patch` (`PatchSchema`, `PatchPathSchema`, `RelationKeySchema`), `Sort` (`SortSchema`, `SortFieldSchema`) and `Search` (`SearchSchema`) sections |

`T` is the caller's row type: a narrower struct for a field mask (`ListParams.Fields`), whose items hold the masked members only, or `json.RawMessage` to keep each item as sent. `Page`, `PatchAnswer` and `FacetsAnswer` encode a nil `Items`, `Updated` or `Facets` as `[]` or `{}`, as the server does, so a test server built on them answers what a real one does.

`DecodeError(status, body)` reads any non-2xx answer: a body in parse's shape (a JSON object whose `code` and `message` are strings) fills the `*Error`; anything else (a proxy's page, an empty body) leaves it the status alone, with no `Code`. Match it with `errors.As`, and its code with `errors.Is`: a `Code` is an `errors.Is` target. The `Code*` constants are the codes parse answers ([error catalog](../docs/http-errors.md#error-codes)), named as parse's core package names them; an application answers codes of its own beside them, which arrive as a `Code` with no constant (`PatchFailure.Code` is always the application's).

Decoding a `Schema`, alone or in a `SchemaList`, refuses a document whose `version` is not `SchemaVersion` with a `*SchemaVersionError`: the server raises the version when a member is removed, renamed or redefined, so a client that reads another version would misread it.

## Errors

The client refuses only what the grammar cannot express; the server judges everything else (an operator a field does not allow, a value out of bounds, a distance past the Earth's circumference). A filter is refused where it is built: it carries its error (`Filter.Err`) from then on, and encoding it, `ListParams.Values` or `ListParams.Body` returns it. Operands are encoded when their condition is built, by encoding/json with their own `MarshalJSON` and tag options, so a built filter's wire value is fixed: changing a document, a slice, a pointer's target, raw JSON or the state a marshaler reads changes neither `ListParams.Values` nor `Body` afterwards, in a filter alone or shared by several, and a number keeps the text it was encoded with.

| Sentinel or type | When |
|----------|------|
| `ErrEmptyOr` | `Or()` of no filters, which would match no row |
| `ErrNegatedEmpty` | `Not` of the empty filter, which would match no row |
| `ErrOperand` | An operand with no spelling in the grammar: a non-finite number (`NaN`, `±Inf`, also inside a `Quoted`, a `Point` or a document), a `Near` distance that is not positive, a value encoding/json cannot encode. It wraps the cause |
| `ErrSubSecond` | A relative `Moment` that is not a whole number of seconds; in a filter, wrapped by `ErrOperand` |
| `ErrElementCondition` | A comparison of an array's `Element()` outside that array's `Some` or `None` (carried when nested in another `Some`, `None`, `Is` or `IsNot`; returned when encoded at the top), or a field's condition inside them |
| `ErrAbsentMember` | An absent `Nullable` encoded: its struct member is not tagged `omitzero` |
| `ErrKeyValue` | A key `ItemPath` cannot write: no value, a value of a type with no text (a struct, a pointer, nil), a non-finite float, or a value whose text is `""`, `.` or `..`, which URL parsing drops from a path. It wraps the cause |
| `*Error` | The server refused the request: its status and parse's error body (`DecodeError`) |
| `*SchemaVersionError` | A schema document in a format version this client does not read |

## For generated SDKs

A generator emits, per resource, from its schema document (`Schema`):

- **A catalog** of fields, one per `filter.fields` entry: a `relation` is a `Relation` (`has_many`, `many_to_many`) or a `Reference` (`belongs_to`); a `data_type` of `geo` is a `Geo`, of `array` one of the array types by its `element`, of `json` or `object` a `Document`, or an `Elements` when the entry lists `elements`; otherwise `gt`…`lte` among its `operators` make it `Ordered`, `like` makes it `Text`, and the rest are a `Field`. `wire: "string"` wraps the value type in `Quoted`. A `sort.fields` entry makes the field its `Sortable` variant, or a `SortKey` when it sorts and does not filter.
- **The row type** the list and patch answers decode into (`Page[Row]`, `PatchAnswer[Row]`), with `json:",string"` on the members the schema marks `wire: "string"`.
- **A merge-patch struct** of `Nullable` members tagged `omitzero`, one per `patch.paths` entry; `Relation[K].Add`, `Remove` and `Replace` for membership paths (`relation: true`), with `Quoted` keys where `key` or `wire` says `string`.
- **The key**: `ItemPath` takes a value per `key` column, in order.

The routes are the server application's (the TypeScript SDK's [server contract](../sdk/ts/README.md#server-contract) lists the ones parse serves): send `ListParams.Values` on a GET of the collection's path, or `ListParams.Body` as a POST when the URL would be too long; the facets as an OPTIONS with the same parameters; a patch as a PATCH of the collection's path, a slash and `ItemPath`. Decode a 2xx answer into its type and any other with `DecodeError`. The generated code then imports this module and the standard library alone, re-declares no wire type and imports no parse server package.

The module versions apart from parse. What holds them together is the wire, which both sides' tests pin to the exchanges parse records, and the schema document's `version`: regenerate when it changes.

## API reference

- `NewField[V]`, `NewOrdered[V]`, `NewText`, `NewTime`, `NewArray[E]`, `NewOrderedArray[E]`, `NewTextArray`, `NewDocument[V]`, `NewElements[V]`, `NewRelation[K]`, `NewReference[K]`, `NewGeo`, `NewSortKey`, `NewSortable*` — catalog fields by name
- `And`, `Or`, `Not` — combine filters; `Filter.IsZero`, `Filter.Err`, `Filter.MarshalJSON`
- `At`, `Now`, `Ago`, `FromNow` — timestamp operands
- `Point`, `Box`, `Polygon` — geographic operands
- `Order`, `Sort` — sort orders; `Sort.String` is the sort parameter
- `ListParams.Values` — a list request's query parameters
- `ListParams.Body` — the same request as a POST body
- `Nullable[T]`, `Null[T]`, `Set` — merge-patch members; `ErrAbsentMember`
- `Operation`, `Add`, `Remove`, `Replace`, `Move`, `Copy`, `Test`, `Append`, `Pointer` — JSON Patch
- `ItemPath`, `ErrKeyValue` — the path of one row, a segment per key value
- `ErrEmptyOr`, `ErrNegatedEmpty`, `ErrElementCondition`, `ErrOperand`, `ErrSubSecond` — what the grammar cannot express
- `Page[T]`, `PageInfo`, `FacetBucket`, `FacetsAnswer`, `PatchAnswer[T]`, `PatchFailure` — the answers
- `Error`, `DecodeError`, `Code` and its `Code*` constants — a refused request
- `Schema`, `SchemaList`, `SchemaVersion`, `SchemaVersionError` and the section types — the schema document

## Configuration

| Setting | Type | Default | Description |
|---|---|---|---|
| None | — | — | No environment variables, global settings or runtime options. |

## External dependencies

None: the module requires nothing, and its tests use the standard library alone, since a test dependency would be listed in every consumer's module graph.

## Build, run and test

From this module directory:

```sh
GOWORK=off go test -tags=unit -race -count=1 ./...
GOWORK=off go vet -tags=unit ./...
GOWORK=off go mod tidy -diff
```

From the repository root, `make test-parse-client` runs the tests in the workspace. `make parse-client-release-check` installs a candidate module ZIP into a fresh consumer, runs its tests, checks that its module graph holds this module alone, that it links only the standard library and no `net/http`, and runs the quick start above.

## Testing

The module's tests read parse's recorded wire and corpora from `../testdata`, beside it in the repository; a module ZIP does not carry them, so outside the repository those tests skip.

- `wire_test.go` replays `testdata/sdk_wire.json`, the exchanges parse records from its handlers: every answer decodes into its type, refusing a member the type does not hold, and encodes back to the recorded JSON; every request the client builds (the list parameters and body, a keyset walk following the decoded cursors, the item paths and patch bodies) is the recorded one; and the list parameter names are the server's.
- `error_test.go` holds the `Code*` constants to the server's catalog (`testdata/error_codes.json`) and to parse's core declarations, so a code the server adds fails until it is mirrored.
- `schema_test.go` round-trips every schema document parse records: `testdata/schema_document.json` and the schemas the corpora are judged against.
- `conformance_test.go` classifies every case of `testdata/filter_conformance.json` and `testdata/patch_conformance.json`: the client builds it, refuses it itself, or cannot express it because its types rule it out. A new corpus case fails until it is classified, and a classification that names no corpus case fails too. A merge patch the client builds equals the corpus input.
- `TestClientRequests` records what the client builds in `testdata/client_requests.json` and fails when it builds anything else. parse's `client_conformance_test.go` reads that file as the server: each filter means what its corpus case means (verdict, code, AST and SQL), the list parameters and body are `ParseList`'s request, every `Operation` constructor's document parses and `BuildUpdate` compiles or answers its documented code, and the geo filters parse. Regenerate it after an intentional change to what the client sends: `go test -tags=unit -run TestClientRequests -update ./`.

parse imports nothing of this module, and this module imports nothing of parse: each side checks the other through those files, in the same commit.

## Operations

The module builds requests and decodes answers, and sends nothing: the caller's `http.Client` carries the timeouts, retries and connection limits, so give it a `Timeout` or each request a context deadline. A request the server refuses answers its E-code ([error catalog](../docs/http-errors.md#error-codes)); `DecodeError` reads it. When the client and a server disagree:

- A request the server refuses that it should take: compare the request the client built with `testdata/sdk_wire.json`'s record of the same request.
- An answer that fails to decode: a `*SchemaVersionError` means the server serves another schema document version (upgrade the client); a JSON type error names the member whose shape changed, so compare the answer with the recorded one.
- An `*Error` with no `Code`: the body was not parse's (a proxy, a load balancer, an application handler of its own); its `Status` is the HTTP status.

## Security

The client encodes values with encoding/json and query parameters with `net/url`, so a value never changes a request's structure. Credentials are the caller's: set them on the `http.Request` the client's parameters are sent with. The server validates every request; the client's own refusals are a convenience, not a control. `DecodeError` and the answer types decode whatever the server sends: bound the body you read (`http.MaxBytesReader`, `io.LimitReader`) before decoding an answer from a server you do not trust, and do not show an `Error`'s `Message` or `Details` to end users as-is.

## Code map

| File | Responsibility |
|------|----------------|
| `doc.go` | Package overview |
| `filter.go` | `Filter`, `And`, `Or`, `Not`, encoding, `ErrEmptyOr`, `ErrNegatedEmpty`, `ErrElementCondition`, `ErrOperand` |
| `field.go` | `Field`, `Ordered`, `Text`, `Time`, `SortKey` and the sortable variants |
| `moment.go` | `Moment` (`At`, `Now`, `Ago`, `FromNow`), `ErrSubSecond` |
| `quoted.go` | `Quoted[T]`, `Quote`, `QuotedScalar`: values of `json:",string"` fields |
| `array.go` | `Array`, `OrderedArray`, `TextArray` and their element conditions |
| `document.go` | `Document`, `Elements`, `Relation`, `Reference` |
| `geo.go` | `Geo`, `Point`, `Shape` (`Box`, `Polygon`) |
| `sort.go` | `Order`, `Sort` |
| `list.go` | `ListParams` |
| `patch.go` | `Nullable`, `ErrAbsentMember`, `Operation`, `Append`, `Pointer` |
| `item.go` | `ItemPath`, `ErrKeyValue`: a row's path below its collection, a segment per key value |
| `page.go` | `Page`, `PageInfo`, `FacetBucket`: a list's answer |
| `answer.go` | `FacetsAnswer`, `PatchAnswer`, `PatchFailure`: the facets and patch answers |
| `error.go` | `Code` and the `Code*` constants, `Error`, `DecodeError`: a refused request |
| `schema.go` | `Schema`, `SchemaList`, `SchemaVersion`, `SchemaVersionError` and the section types: the schema document |
| `../../tools/check-parse-client-release.sh` | The candidate archive in a fresh consumer: module graph, dependencies, tests and the quick start |

## References

- [parse](../README.md) · [Queries](../docs/queries.md) · [Patches](../docs/patches.md) · [HTTP contract and errors](../docs/http-errors.md) · [Client integration](../docs/integrations.md#go-client)

## License

[MIT](LICENSE).
