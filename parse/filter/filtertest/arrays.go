package filtertest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ArrayRecord is the scalar-array fixture: a document whose members are
// arrays of strings, integers, decimals, timestamps and booleans, two array
// columns, and a JSON array of objects whose elements hold an array of
// strings. Its table is "array_records".
type ArrayRecord struct {
	ID     string          `json:"id"     db:"id"     parse:"filter" filter_ops:"eq,in"`
	Doc    json.RawMessage `json:"doc"    db:"doc"    parse:"filter" paths:"tags:[]string:filter+facet;counts:[]int;prices:[]decimal;seen:[]timestamp;flags:[]bool:facet"`
	Labels []string        `json:"labels" db:"labels" parse:"filter,facet"`
	Scores []int64         `json:"scores" db:"scores" parse:"filter"`
	Lines  json.RawMessage `json:"lines"  db:"lines"  parse:"filter" elements:"sku:string;tags:[]string"`
}

// ArrayRecords is the fixture data. An array member's value is its
// elements of its type, so these members hold none: c's empty arrays, d's
// JSON null and string, e's string and object, f's NULL document, h's
// absent counts, and i's [null]. g's elements are of mixed kinds: its tags
// hold one string ("A"), its counts 7.0 and 5.5, its prices 1e3 as a number
// (a string in exponent notation is no decimal). b's tags repeat an
// element. The array columns are NULL in d and empty in c and f. A line's
// tags follow the same rules: e's are a string, h's second line is no
// object, i's tags are empty, and f's lines are no array.
func ArrayRecords() []ArrayRecord {
	return []ArrayRecord{
		{ID: "a", Doc: json.RawMessage(`{"tags":["a","b"],"counts":[1,20],"prices":[9.5,"10.50"],"seen":["2026-01-02T03:04:05Z","2026-01-02T03:04:05.5+00:00"],"flags":[true]}`),
			Labels: []string{"x", "y"}, Scores: []int64{1, 20}, Lines: json.RawMessage(`[{"sku":"x","tags":["a","b"]},{"sku":"y","tags":["c"]}]`)},
		{ID: "b", Doc: json.RawMessage(`{"tags":["b","b","c"],"counts":[7],"prices":["9.50"],"seen":["2026-01-01T00:00:00.000000001Z"],"flags":[false,true]}`),
			Labels: []string{"y"}, Scores: []int64{7}, Lines: json.RawMessage(`[{"sku":"x","tags":["c"]}]`)},
		{ID: "c", Doc: json.RawMessage(`{"tags":[],"counts":[],"prices":[],"seen":[],"flags":[]}`), Labels: []string{}, Scores: []int64{}, Lines: json.RawMessage(`[]`)},
		{ID: "d", Doc: json.RawMessage(`{"tags":null,"counts":"7"}`)},
		{ID: "e", Doc: json.RawMessage(`{"tags":"a","counts":{"x":1}}`), Labels: []string{"a"}, Scores: []int64{3}, Lines: json.RawMessage(`[{"sku":"x","tags":"a"}]`)},
		{ID: "f", Labels: []string{}, Scores: []int64{}, Lines: json.RawMessage(`{"sku":"x","tags":["a"]}`)},
		{ID: "g", Doc: json.RawMessage(`{"tags":[1,null,"A",{"x":"a"},["a"]],"counts":[7.0,"7",5.5],"prices":[1e3,"1e3",true]}`), Labels: []string{"z", "x"}, Scores: []int64{9},
			Lines: json.RawMessage(`[{"sku":"y","tags":[1,"a"]}]`)},
		{ID: "h", Doc: json.RawMessage(`{"tags":["a"]}`), Labels: []string{"a", "a"}, Scores: []int64{5, 9}, Lines: json.RawMessage(`[{"tags":["a"]},"x"]`)},
		{ID: "i", Doc: json.RawMessage(`{"tags":[null],"flags":["true"]}`), Labels: []string{"z"}, Scores: []int64{100}, Lines: json.RawMessage(`[{"sku":"x","tags":[]}]`)},
	}
}

// ArrayFacets are the buckets array facets count over ArrayRecords: each
// row once per distinct element of the array's type.
var ArrayFacets = map[string]map[string]int64{
	"doc.tags":  {"a": 2, "b": 2, "c": 1, "A": 1},
	"doc.flags": {"true": 2, "false": 1},
	"labels":    {"x": 2, "y": 2, "a": 2, "z": 2},
}

// arrayCase is a filter on ArrayRecords, the rows it selects and the rows
// on which it is Unknown; its negation selects the rest.
type arrayCase struct {
	name, filter, want, unknown string
}

var arrayCases = []arrayCase{
	{"path some eq", `{"doc.tags":{"$some":{"$eq":"a"}}}`, "ah", ""},
	{"path some in", `{"doc.tags":{"$some":{"$in":["a","c"]}}}`, "abh", ""},
	{"path some in none", `{"doc.tags":{"$some":{"$in":[]}}}`, "", ""},
	{"path some like", `{"doc.tags":{"$some":{"$like":"b%"}}}`, "ab", ""},
	{"path some ilike", `{"doc.tags":{"$some":{"$ilike":"a"}}}`, "agh", ""},
	{"path some ne", `{"doc.tags":{"$some":{"$ne":"a"}}}`, "abg", ""},
	{"path some nin", `{"doc.tags":{"$some":{"$nin":["a","b"]}}}`, "bg", ""},
	{"path elements equal under $or", `{"doc.tags":{"$some":{"$or":[{"$eq":"a"},{"$in":["c","z"]}]}}}`, "abh", ""},
	{"path none eq", `{"doc.tags":{"$none":{"$eq":"a"}}}`, "bcdefgi", ""},
	{"path some any element", `{"doc.tags":{"$some":{}}}`, "abgh", ""},
	{"path none any element", `{"doc.tags":{"$none":{}}}`, "cdefi", ""},
	{"path contains", `{"doc.tags":{"$contains":["a","b"]}}`, "a", "cdefi"},
	{"path contains a repeated element", `{"doc.tags":{"$contains":["b"]}}`, "ab", "cdefi"},
	{"path contains nothing", `{"doc.tags":{"$contains":[]}}`, "abgh", "cdefi"},
	{"path null", `{"doc.tags":{"$null":true}}`, "cdefi", ""},
	{"path not null", `{"doc.tags":{"$not_null":true}}`, "abgh", ""},
	{"path one element in a range", `{"doc.counts":{"$some":{"$gte":5,"$lt":10}}}`, "bg", ""},
	{"path elements on either side", `{"doc.counts":{"$some":{"$or":[{"$lt":2},{"$gt":15}]}}}`, "a", ""},
	{"path an element unequal", `{"doc.counts":{"$some":{"$not":{"$eq":7}}}}`, "ag", ""},
	{"path an element in a range and a set", `{"doc.counts":{"$some":{"$and":[{"$gte":1},{"$in":[1,7]}]}}}`, "abg", ""},
	{"path contains a number", `{"doc.counts":{"$contains":[7]}}`, "bg", "cdefhi"},
	{"path decimal some eq", `{"doc.prices":{"$some":{"$eq":"10.5"}}}`, "a", ""},
	{"path decimal some gt", `{"doc.prices":{"$some":{"$gt":9.5}}}`, "ag", ""},
	{"path decimal some in", `{"doc.prices":{"$some":{"$in":["9.5",1000]}}}`, "abg", ""},
	{"path decimal contains", `{"doc.prices":{"$contains":["9.5","10.5"]}}`, "a", "cdefhi"},
	{"path timestamp some gte", `{"doc.seen":{"$some":{"$gte":"2026-01-02T03:04:05Z"}}}`, "a", ""},
	{"path bool some eq", `{"doc.flags":{"$some":{"$eq":false}}}`, "b", ""},
	{"path bool none eq", `{"doc.flags":{"$none":{"$eq":true}}}`, "cdefghi", ""},
	{"path bool contains", `{"doc.flags":{"$contains":[true]}}`, "ab", "cdefghi"},

	{"column some eq", `{"labels":{"$some":{"$eq":"y"}}}`, "ab", ""},
	{"column some in", `{"labels":{"$some":{"$in":["a","z"]}}}`, "eghi", ""},
	{"column some like", `{"labels":{"$some":{"$like":"%"}}}`, "abeghi", ""},
	{"column none any element", `{"labels":{"$none":{}}}`, "cdf", ""},
	{"column contains", `{"labels":{"$contains":["a"]}}`, "eh", "d"},
	{"column one element in a range", `{"scores":{"$some":{"$gte":5,"$lt":10}}}`, "bgh", ""},
	{"column elements on either side", `{"scores":{"$some":{"$or":[{"$eq":1},{"$eq":9}]}}}`, "agh", ""},
	{"column null", `{"labels":{"$null":true}}`, "d", ""},

	{"a line's tag", `{"lines":{"$some":{"tags":{"$some":{"$eq":"a"}}}}}`, "agh", ""},
	{"one line's sku and tag", `{"lines":{"$some":{"sku":"x","tags":{"$some":{"$eq":"a"}}}}}`, "a", ""},
	{"a line without tags", `{"lines":{"$some":{"tags":{"$none":{}}}}}`, "ehi", ""},
	{"no line's tags contain", `{"lines":{"$none":{"tags":{"$contains":["a"]}}}}`, "bcdefi", ""},
}

// arrayIDs are ArrayRecords' ids.
const arrayIDs = "abcdefghi"

// ArrayCases are filters on ArrayRecords' array members and columns, each
// also under $not: `$some` and `$none` are never Unknown, `$contains` is on
// a NULL array.
func ArrayCases() []Case {
	out := make([]Case, 0, 2*len(arrayCases)+len(arrayParseErrors))
	for _, c := range arrayCases {
		var negated strings.Builder
		for _, id := range arrayIDs {
			if !strings.ContainsRune(c.want, id) && !strings.ContainsRune(c.unknown, id) {
				negated.WriteRune(id)
			}
		}
		out = append(out,
			Case{Name: c.name, Filter: c.filter, Want: ids(c.want)},
			Case{Name: c.name + " under $not", Filter: `{"$not":` + c.filter + `}`, Want: ids(negated.String())})
	}
	return append(out, slices.Clone(arrayParseErrors)...)
}

// arrayParseErrors are element conditions the parser refuses.
var arrayParseErrors = []Case{
	{Name: "an element condition that is no object", Filter: `{"doc.tags":{"$some":"a"}}`, ParseErr: filter.ECodeInvalidJSON.String()},
	{Name: "an element's NULL test", Filter: `{"doc.tags":{"$some":{"$null":true}}}`, ParseErr: filter.ECodeInvalidOperator.String()},
	{Name: "an operator of another type", Filter: `{"doc.counts":{"$some":{"$like":"1%"}}}`, ParseErr: filter.ECodeInvalidOperator.String()},
	{Name: "a member of an element", Filter: `{"doc.tags":{"$some":{"x":"a"}}}`, ParseErr: filter.ECodeInvalidOperator.String()},
	{Name: "a value of another type", Filter: `{"doc.counts":{"$some":{"$eq":"seven"}}}`, ParseErr: filter.ECodeValueTypeMismatch.String()},
	{Name: "an empty $or", Filter: `{"doc.tags":{"$some":{"$or":[]}}}`, ParseErr: filter.ECodeInvalidJSON.String()},
	{Name: "an empty $not", Filter: `{"doc.tags":{"$some":{"$not":{}}}}`, ParseErr: filter.ECodeInvalidJSON.String()},
	{Name: "an element condition on a scalar", Filter: `{"id":{"$some":{"$eq":"a"}}}`, ParseErr: filter.ECodeInvalidOperator.String()},
}

// ArraySchema returns ArrayRecord's generated filter schema.
func ArraySchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[ArrayRecord](core.NewSchemaGenerator())
}

// RunArrays evaluates every ArrayCases case on backend, which evaluates
// against ArrayRecords.
func RunArrays(t *testing.T, schema *filter.ResourceSchema, backend Backend) {
	cfg := core.DefaultConfig()
	cfg.Clock = Now
	cfg.MaxCost = 100 // an element predicate on a line's tags nests two
	run(t, "", filter.NewFilterParser(schema, cfg), ArrayCases(), backend)
}
