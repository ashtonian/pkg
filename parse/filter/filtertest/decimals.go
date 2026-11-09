package filtertest

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// DecimalRecord is the exact-decimal fixture: a decimal column and a
// document whose price member holds a decimal as a JSON number, as a string
// in plain notation, or not at all. Its values sit where a double loses
// them: around 2^53 and 2^63, at 20 and 38 digits, and negatives that
// differ in the seventeenth digit.
type DecimalRecord struct {
	ID     string           `json:"id"     db:"id"     parse:"filter,sort" filter_ops:"eq,in"`
	Amount *decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort,facet" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null" facet:"range:0-1,1-100,100+"`
	Doc    json.RawMessage  `json:"doc"    db:"doc"    parse:"filter" paths:"price:decimal:filter+sort+facet"`
}

// DecimalRecords is the fixture data. Amounts are written as decimal text,
// h's with a scale (10.50); n's equals h's. Prices are JSON numbers as
// written (h's 1.050e1, i's -0.0) or strings (b, d, f, n's 10.500); j's is
// a boolean, k's a string with an exponent, l's JSON null and m's absent,
// so those four hold no price.
func DecimalRecords() []DecimalRecord {
	return []DecimalRecord{
		decimalRecord("a", "9007199254740992", `{"price":9007199254740992.5}`),
		decimalRecord("b", "9007199254740993", `{"price":"9007199254740993.5"}`),
		decimalRecord("c", "9223372036854775807", `{"price":9223372036854775807}`),
		decimalRecord("d", "9223372036854775808", `{"price":"9223372036854775808"}`),
		decimalRecord("e", "12345678901234567890", `{"price":12345678901234567890}`),
		decimalRecord("f", "12345678901234567890.123456789012345678", `{"price":"12345678901234567890.123456789012345678"}`),
		decimalRecord("g", "12345678901234567890.123456789012345679", `{"price":12345678901234567890.123456789012345679}`),
		decimalRecord("h", "10.50", `{"price":1.050e1}`),
		decimalRecord("i", "-0.5", `{"price":-0.0}`),
		decimalRecord("j", "-12345678901234567.2", `{"price":true}`),
		decimalRecord("k", "-12345678901234567.1", `{"price":"1e3"}`),
		{ID: "l", Doc: json.RawMessage(`{"price":null}`)},
		decimalRecord("m", "0", `{}`),
		decimalRecord("n", "10.5", `{"price":"10.500"}`),
	}
}

func decimalRecord(id, amount, doc string) DecimalRecord {
	return DecimalRecord{ID: id, Amount: new(decimal.RequireFromString(amount)), Doc: json.RawMessage(doc)}
}

// DecimalOrders are the presentation orders decimal sorts give
// DecimalRecords: NULL greatest, ties on id in the primary direction.
var DecimalOrders = map[string]string{
	"amount":     "jkimhnabcdefgl",
	"-amount":    "lgfedcbanhmikj",
	"doc.price":  "ihnabcdefgjklm",
	"-doc.price": "mlkjgfedcbanhi",
}

// DecimalFacets are the buckets decimal facets count over DecimalRecords:
// amount's ranges and the price member's terms, each keyed by canonical
// text, so h's and n's equal prices share a bucket.
var DecimalFacets = map[string]map[string]int64{
	"amount": {"0-1": 1, "1-100": 2, "100+": 7},
	"doc.price": {
		"0": 1, "10.5": 2, "9007199254740992.5": 1, "9007199254740993.5": 1, "9223372036854775807": 1, "9223372036854775808": 1,
		"12345678901234567890": 1, "12345678901234567890.123456789012345678": 1, "12345678901234567890.123456789012345679": 1,
	},
}

// DecimalFacetOrders are DecimalFacets' terms facets cut through a tie,
// which by text would keep 12345678901234567890 before 9007199254740992.5.
var DecimalFacetOrders = map[string]FacetOrder{
	"doc.price": {Limit: 4, Keys: []string{"10.5", "0", "9007199254740992.5", "9007199254740993.5"}},
}

// DecimalCases are filters on DecimalRecords' amount column and price
// member that a double would answer wrongly, and the rows each matches.
func DecimalCases() []Case {
	return slices.Clone(decimalCases)
}

var decimalCases = []Case{
	{Name: "eq past 2^53 as a number", Filter: `{"amount":9007199254740993}`, Want: ids("b")},
	{Name: "eq ignores a stored scale", Filter: `{"amount":"10.5"}`, Want: ids("hn")},
	{Name: "eq at 38 digits", Filter: `{"amount":"12345678901234567890.123456789012345678"}`, Want: ids("f")},
	{Name: "ne past 2^53", Filter: `{"amount":{"$ne":"9007199254740993"}}`, Want: ids("acdefghijkmn")},
	{Name: "gt past 2^63", Filter: `{"amount":{"$gt":"9223372036854775807"}}`, Want: ids("defg")},
	{Name: "gte at 2^63 - 1", Filter: `{"amount":{"$gte":9223372036854775807}}`, Want: ids("cdefg")},
	{Name: "gt at 38 digits", Filter: `{"amount":{"$gt":"12345678901234567890.123456789012345678"}}`, Want: ids("g")},
	{Name: "lt among negatives", Filter: `{"amount":{"$lt":"-12345678901234567.1"}}`, Want: ids("j")},
	{Name: "lte among negatives", Filter: `{"amount":{"$lte":"-12345678901234567.1"}}`, Want: ids("jk")},
	{Name: "a range around zero", Filter: `{"$and":[{"amount":{"$gt":-1}},{"amount":{"$lt":1}}]}`, Want: ids("im")},
	{Name: "a range at one value", Filter: `{"amount":{"$gte":"1.05e1","$lte":"10.50"}}`, Want: ids("hn")},
	{Name: "in past 2^53 and at 38 digits", Filter: `{"amount":{"$in":[9007199254740992,"12345678901234567890.123456789012345679"]}}`, Want: ids("ag")},
	{Name: "nin excludes null", Filter: `{"amount":{"$nin":["9007199254740992","-0.5",0]}}`, Want: ids("bcdefghjkn")},
	{Name: "null", Filter: `{"amount":{"$null":true}}`, Want: ids("l")},

	{Name: "path eq a string member", Filter: `{"doc.price":"9007199254740993.5"}`, Want: ids("b")},
	{Name: "path eq a number member past double precision", Filter: `{"doc.price":9007199254740992.5}`, Want: ids("a")},
	{Name: "path eq at 38 digits", Filter: `{"doc.price":"12345678901234567890.123456789012345679"}`, Want: ids("g")},
	{Name: "path ne", Filter: `{"doc.price":{"$ne":0}}`, Want: ids("abcdefghn")},
	{Name: "path gt past 2^63", Filter: `{"doc.price":{"$gt":"9223372036854775807"}}`, Want: ids("defg")},
	{Name: "path lt", Filter: `{"doc.price":{"$lt":1}}`, Want: ids("i")},
	{Name: "path in across forms", Filter: `{"doc.price":{"$in":["0","10.5"]}}`, Want: ids("hin")},
	{Name: "path nin excludes members of no decimal", Filter: `{"doc.price":{"$nin":["9007199254740992.5"]}}`, Want: ids("bcdefghin")},
	{Name: "path null takes every member of no decimal", Filter: `{"doc.price":{"$null":true}}`, Want: ids("jklm")},
}

// DecimalSchema returns DecimalRecord's generated filter schema.
func DecimalSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[DecimalRecord](core.NewSchemaGenerator())
}

// RunDecimals evaluates every DecimalCases case, parsed against schema
// (DecimalSchema or a copy declaring the backend's storage), on backend,
// which evaluates against DecimalRecords.
func RunDecimals(t *testing.T, schema *filter.ResourceSchema, backend Backend) {
	run(t, "", PathParser(schema), DecimalCases(), backend)
}
