# parse/client

Builds requests in parse's grammar from Go: typed filters, sort orders, list parameters, merge-patch members and JSON Patch operations, for SDKs that call a parse server. It imports only the standard library, and it is held to the same filter and patch corpora as the TypeScript SDK.

## Quick Start

```go
import (
	"net/http"
	"time"

	"github.com/ashtonian/pkg/parse/client"
)

// One catalog per resource, usually generated.
var Devices = struct {
	Name     client.SortableText
	Enabled  client.Field[bool]
	LastSeen client.SortableTime
}{client.NewSortableText("name"), client.NewField[bool]("enabled"), client.NewSortableTime("last_seen")}

func listRecent(req *http.Request) error {
	q, err := client.ListParams{
		Filter:   client.And(Devices.Name.Like("pump-%"), Devices.Enabled.Eq(true), Devices.LastSeen.Gte(client.Ago(24*time.Hour))),
		Sort:     client.Sort{Devices.LastSeen.Desc(), Devices.Name.Asc()},
		PageSize: 50,
	}.Values()
	if err != nil {
		return err
	}
	req.URL.RawQuery = q.Encode() // filter={"$and":[...]}&page_size=50&sort=-last_seen,name
	return nil
}
```

`example_test.go` runs this request, a merge patch and a JSON Patch document with checked output.

## Field types

A catalog field's type follows the operators its value type can declare on the server, so a misspelt field or an operand of the wrong type does not compile. The server still refuses an operator a field does not allow (E2072).

| Type | Value | Filter methods |
|------|-------|----------------|
| `Field[V]` | key, enum, boolean | `Eq`, `Ne`, `In`, `Nin`, `Null`, `NotNull` |
| `Ordered[V]` | number, decimal (`Ordered[decimal.Decimal]`: any type that marshals its exact text) | `Field`'s, `Gt`, `Gte`, `Lt`, `Lte` |
| `Text` | string | `Ordered`'s, `Like`, `ILike`, `Contains` |
| `Time` (`Ordered[Moment]`) | timestamp | `Ordered`'s, with `At(t)`, `Now()`, `Ago(d)` or `FromNow(d)` operands |
| `Document[V]` | JSON document | `Eq`, `Ne`, `Contains`, `HasKey`, `Null`, `NotNull` |
| `Elements[V]` | JSON array of objects | `Document`'s, `Some`, `None` (a filter over element members) |
| `Relation[K]` | has-many or many-to-many | `Field`'s on target keys, `Some`, `None` (a filter over the target); `Replace`, `Add`, `Remove` membership operations |
| `Reference[K]` | belongs-to | `Field`'s on the target key, `Is`, `IsNot` |
| `Geo` | geographic | `Near(point, meters)`, `Within(shape)`, `Intersects(shape)`, `Null`, `NotNull` |

`SortableField[V]`, `SortableOrdered[V]`, `SortableText` and `SortableTime` add `Asc` and `Desc`; a sort-only field is a `SortKey`.

A relative `Moment` is sent as the server's token (`now-1d`) and resolved by the server's clock, so the client's clock never skews it.

## Encoding

- **Filter** encodes every condition in operator form (`{"name":{"$eq":"a"}}`), so a document value whose members start with `$` is never read as operators. `And` and `Or` flatten nested groups, so a filter built up in a loop stays within the server's nesting depth. The zero `Filter` matches every row and encodes as `{}`.
- **ListParams** encodes the list parameters `Resource.ParseList` reads (`filter`, `sort`, `cursor`, `page_size`, `include_total`, `include_deleted`, `search`, `facets`, `include`, `fields`) and leaves zero members out.
- **Nullable[T]** is a merge-patch (RFC 7386) member: with the `omitzero` tag option the zero value is absent, `Null[T]()` clears the field, `Set(v)` writes it.
- **Operation** is a JSON Patch (RFC 6902) operation; a `[]Operation` is the document. `Pointer(tokens...)` escapes member names into a JSON Pointer.

## Errors

The client refuses only what the grammar cannot express; the server judges everything else. A refused filter carries its error (`Filter.Err`), and encoding it, or `ListParams.Values`, returns it.

| Sentinel | When |
|----------|------|
| `ErrEmptyOr` | `Or()` of no filters, which would match no row |
| `ErrNegatedEmpty` | `Not` of the empty filter, which would match no row |
| `ErrSubSecond` | A relative `Moment` that is not a whole number of seconds |

## API Reference

- `NewField[V]`, `NewOrdered[V]`, `NewText`, `NewTime`, `NewDocument[V]`, `NewElements[V]`, `NewRelation[K]`, `NewReference[K]`, `NewGeo`, `NewSortKey`, `NewSortable*` — catalog fields by name
- `And`, `Or`, `Not` — combine filters; `Filter.IsZero`, `Filter.Err`, `Filter.MarshalJSON`
- `At`, `Now`, `Ago`, `FromNow` — timestamp operands
- `Point`, `Box`, `Polygon` — geographic operands
- `Order`, `Sort` — sort orders; `Sort.String` is the sort parameter
- `ListParams.Values` — a list request's query parameters
- `Nullable[T]`, `Null[T]`, `Set` — merge-patch members
- `Operation`, `Add`, `Remove`, `Replace`, `Move`, `Copy`, `Test`, `Pointer` — JSON Patch

## Testing

`client_test.go` pins each encoding. parse's `client_conformance_test.go` classifies every case of `testdata/filter_conformance.json` and `testdata/patch_conformance.json`:

- the client builds it, and the server reads the same verdict, AST and SQL;
- the client builds it, and the server refuses it with the corpus's code;
- the client refuses it itself;
- or the client's types cannot express it.

A new corpus case fails the test until it is classified. The same file holds the list parameters to `ParseList`, and checks that JSON Patch operations and geo filters parse.

## Key Files

| File | Responsibility |
|------|----------------|
| `doc.go` | Package overview |
| `filter.go` | `Filter`, `And`, `Or`, `Not`, encoding, `ErrEmptyOr`, `ErrNegatedEmpty` |
| `field.go` | `Field`, `Ordered`, `Text`, `Time`, `SortKey` and the sortable variants |
| `moment.go` | `Moment` (`At`, `Now`, `Ago`, `FromNow`), `ErrSubSecond` |
| `document.go` | `Document`, `Elements`, `Relation`, `Reference` |
| `geo.go` | `Geo`, `Point`, `Shape` (`Box`, `Polygon`) |
| `sort.go` | `Order`, `Sort` |
| `list.go` | `ListParams` |
| `patch.go` | `Nullable`, `Operation`, `Pointer` |

## Dependencies

- *Imports*: standard library only
- *Imported by*: parse's conformance tests; generated Go SDKs once they move off `forge/sdkgo` (A4.18)
- *Package boundary*: standard-library-only request builders; no Parse server package imports

## References

- [Package overview](../README.md) · [Queries](../docs/queries.md) · [Patches](../docs/patches.md)
