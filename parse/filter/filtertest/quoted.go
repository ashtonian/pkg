package filtertest

import (
	"math"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// QuotedRecord is the quoted-integer fixture: N is an int64 tagged
// json:",string", whose filter operands are decimal text. Its values sit
// where a double loses them: both ends of int64 and either side of 2^53.
type QuotedRecord struct {
	ID string `json:"id" db:"id" parse:"filter" filter_ops:"eq,in"`
	N  *int64 `json:"n,string" db:"n" parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null"`
}

// QuotedRecords is the fixture data; f's N is NULL.
func QuotedRecords() []QuotedRecord {
	n := func(v int64) *int64 { return &v }
	return []QuotedRecord{
		{ID: "a", N: n(math.MaxInt64)},
		{ID: "b", N: n(-math.MaxInt64)},
		{ID: "c", N: n(9007199254740993)},
		{ID: "d", N: n(9007199254740992)},
		{ID: "e", N: n(0)},
		{ID: "f"},
		{ID: "g", N: n(math.MinInt64)},
	}
}

// QuotedCases are filters on QuotedRecords' N, each operand decimal text,
// and the rows each matches.
func QuotedCases() []Case {
	return slices.Clone(quotedCases)
}

var quotedCases = []Case{
	{Name: "eq int64 max", Filter: `{"n":"9223372036854775807"}`, Want: ids("a")},
	{Name: "eq the negated max", Filter: `{"n":"-9223372036854775807"}`, Want: ids("b")},
	{Name: "eq int64 min", Filter: `{"n":"-9223372036854775808"}`, Want: ids("g")},
	{Name: "eq past 2^53 tells neighbours apart", Filter: `{"n":"9007199254740993"}`, Want: ids("c")},
	{Name: "gt 2^53", Filter: `{"n":{"$gt":"9007199254740992"}}`, Want: ids("ac")},
	{Name: "gte 2^53", Filter: `{"n":{"$gte":"9007199254740992"}}`, Want: ids("acd")},
	{Name: "lt zero", Filter: `{"n":{"$lt":"0"}}`, Want: ids("bg")},
	{Name: "lte the negated max", Filter: `{"n":{"$lte":"-9223372036854775807"}}`, Want: ids("bg")},
	{Name: "in", Filter: `{"n":{"$in":["9007199254740993","0","1"]}}`, Want: ids("ce")},
	{Name: "nin excludes null", Filter: `{"n":{"$nin":["0","9223372036854775807"]}}`, Want: ids("bcdg")},
	{Name: "ne excludes null", Filter: `{"n":{"$ne":"0"}}`, Want: ids("abcdg")},
	{Name: "null", Filter: `{"n":{"$null":true}}`, Want: ids("f")},
	{Name: "a bare number is refused", Filter: `{"n":9007199254740993}`, ParseErr: "E2073"},
	{Name: "a bare number in a set is refused", Filter: `{"n":{"$in":["1",2]}}`, ParseErr: "E2073"},
	{Name: "text past int64 is refused", Filter: `{"n":"9223372036854775808"}`, ParseErr: "E2073"},
}

// QuotedSchema returns QuotedRecord's generated filter schema.
func QuotedSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[QuotedRecord](core.NewSchemaGenerator())
}

// RunQuoted evaluates every QuotedCases case on backend, which evaluates
// against QuotedRecords.
func RunQuoted(t *testing.T, backend Backend) {
	schema, err := QuotedSchema()
	if err != nil {
		t.Fatalf("filtertest: quoted schema: %v", err)
	}
	run(t, filter.NewFilterParser(schema, core.DefaultConfig()), QuotedCases(), backend)
}
