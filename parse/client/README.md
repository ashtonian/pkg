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
| `Ordered[Quoted[T]]`, `Field[Quoted[T]]` | a bool, integer or float the server declares `json:",string"` (schema `"wire": "string"`) | as for `T`, with `Quote(v)` operands sent as JSON strings (`"9007199254740993"`) |
| `Document[V]` | JSON document | `Eq`, `Ne`, `Contains`, `HasKey`, `Null`, `NotNull` |
| `Array[E]`, `OrderedArray[E]`, `TextArray` | array of keys / numbers, decimals, timestamps / strings: a column or a declared document member | `Contains`, `Null`, `NotNull`, `Some`, `None` (a condition on one element); `Element()` is a `Field[E]`, `Ordered[E]` or `Text` whose comparisons, combined by `And`, `Or` and `Not`, build that condition; `Append(e)`, an `array_append` operation on an array column |
| `Elements[V]` | JSON array of objects | `Document`'s, `Some`, `None` (a filter over element members) |
| `Relation[K]` | has-many or many-to-many | `Field`'s on target keys, `Some`, `None` (a filter over the target); `Replace`, `Add`, `Remove` membership operations |
| `Reference[K]` | belongs-to | `Field`'s on the target key, `Is`, `IsNot` |
| `Geo` | geographic | `Near(point, meters)` (meters positive and finite), `Within(shape)`, `Intersects(shape)`, `Null`, `NotNull` |

`SortableField[V]`, `SortableOrdered[V]`, `SortableText` and `SortableTime` add `Asc` and `Desc`; a sort-only field is a `SortKey`.

An element condition names no field: `tags.Some(client.Or(tag.Like("a%"), tag.Eq("b")))`, with `tag := tags.Element()`, encodes `{"tags":{"$some":{"$or":[{"$like":"a%"},{"$eq":"b"}]}}}`. The empty filter is any element: `tags.Some(client.Filter{})`.

A relative `Moment` is sent as the server's token (`now-1d`) and resolved by the server's clock, so the client's clock never skews it.

A `Quoted[T]` travels as encoding/json writes a `json:",string"` field: its JSON text inside a JSON string. Use it for operands, merge-patch members (`client.Nullable[client.Quoted[int64]]`) and JSON Patch values (`client.Replace("/id", client.Quote(int64(42)))`) of such a field, and to decode its responses, so a 64-bit integer stays exact end to end.

## Encoding

- **Filter** encodes every condition in operator form (`{"name":{"$eq":"a"}}`), so a document value whose members start with `$` is never read as operators. `And` and `Or` flatten nested groups, so a filter built up in a loop stays within the server's nesting depth. The zero `Filter` matches every row and encodes as `{}`.
- **ListParams** encodes the list parameters `Resource.ParseList` reads (`filter`, `sort`, `cursor`, `page_size`, `include_total`, `include_deleted`, `search`, `facets`, `include`, `fields`) and leaves zero members out: `Values` as a query string, `Body` as the JSON document `Resource.ParseListBody` reads from a POST, for a request too large for a URL.
- **Nullable[T]** is a merge-patch (RFC 7396) member: with the `omitzero` tag option the zero value is absent, `Null[T]()` clears the field, `Set(v)` writes it. Tag every `Nullable` member `omitzero`: `omitempty` does not omit a struct, so an absent member under it (or under no option) would encode as `null` and clear its column; encoding one is `ErrAbsentMember` instead. `Set` of a nil slice or map writes the empty one, and `Set` of a nil pointer is `Null`, since that is what it encodes.
- **Operation** is a JSON Patch (RFC 6902) operation; a `[]Operation` is the document. `Append(path, element)` and `Array[E].Append(e)` write parse's `array_append` extension, which appends one element to an array column atomically. `Pointer(tokens...)` escapes member names into a JSON Pointer.

## Errors

The client refuses only what the grammar cannot express; the server judges everything else (an operator a field does not allow, a value out of bounds, a distance past the Earth's circumference). A filter is refused where it is built: it carries its error (`Filter.Err`) from then on, and encoding it, `ListParams.Values` or `ListParams.Body` returns it. Operands are encoded when their condition is built, so a filter never shares the caller's values.

| Sentinel | When |
|----------|------|
| `ErrEmptyOr` | `Or()` of no filters, which would match no row |
| `ErrNegatedEmpty` | `Not` of the empty filter, which would match no row |
| `ErrOperand` | An operand with no spelling in the grammar: a non-finite number (`NaN`, `±Inf`, also inside a `Quoted`, a `Point` or a document), a `Near` distance that is not positive, a value encoding/json cannot encode. It wraps the cause |
| `ErrSubSecond` | A relative `Moment` that is not a whole number of seconds; in a filter, wrapped by `ErrOperand` |
| `ErrElementCondition` | A comparison of an array's `Element()` outside that array's `Some` or `None` (carried when nested in another `Some`, `None`, `Is` or `IsNot`; returned when encoded at the top), or a field's condition inside them |
| `ErrAbsentMember` | An absent `Nullable` encoded: its struct member is not tagged `omitzero` |

## API Reference

- `NewField[V]`, `NewOrdered[V]`, `NewText`, `NewTime`, `NewArray[E]`, `NewOrderedArray[E]`, `NewTextArray`, `NewDocument[V]`, `NewElements[V]`, `NewRelation[K]`, `NewReference[K]`, `NewGeo`, `NewSortKey`, `NewSortable*` — catalog fields by name
- `And`, `Or`, `Not` — combine filters; `Filter.IsZero`, `Filter.Err`, `Filter.MarshalJSON`
- `At`, `Now`, `Ago`, `FromNow` — timestamp operands
- `Point`, `Box`, `Polygon` — geographic operands
- `Order`, `Sort` — sort orders; `Sort.String` is the sort parameter
- `ListParams.Values` — a list request's query parameters
- `ListParams.Body` — the same request as a POST body
- `Nullable[T]`, `Null[T]`, `Set` — merge-patch members; `ErrAbsentMember`
- `Operation`, `Add`, `Remove`, `Replace`, `Move`, `Copy`, `Test`, `Append`, `Pointer` — JSON Patch
- `ErrEmptyOr`, `ErrNegatedEmpty`, `ErrElementCondition`, `ErrOperand`, `ErrSubSecond` — what the grammar cannot express

## Testing

`client_test.go` pins each encoding. parse's `client_conformance_test.go` classifies every case of `testdata/filter_conformance.json` and `testdata/patch_conformance.json`:

- the client builds it, and the server reads the same verdict, AST and SQL;
- the client builds it, and the server refuses it with the corpus's code;
- the client refuses it itself;
- or the client's types cannot express it.

A new corpus case fails the test until it is classified. The same file holds the list parameters to `ParseList`, parses a document from every `Operation` constructor and checks the code `BuildUpdate` answers for each, and checks that geo filters parse.

## Key Files

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

## Dependencies

- *Imports*: standard library only
- *Imported by*: parse's conformance tests (`client_conformance_test.go`) and Go clients of a parse server
- *Package boundary*: standard-library-only request builders; no Parse server package imports

## References

- [Package overview](../README.md) · [Queries](../docs/queries.md) · [Patches](../docs/patches.md)
