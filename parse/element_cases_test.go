//go:build unit || integration

package parse_test

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Same-element predicates over JSON arrays: every
// predicate inside one $some holds for one element.

type lineItem struct {
	SKU  string `json:"sku" parse:"filter"`
	Qty  int64  `json:"qty" parse:"filter"`
	Dims struct {
		W float64 `json:"w" parse:"filter"`
	} `json:"dims" parse:"filter"`
}

type order struct {
	ID       string           `json:"id"        db:"id"        parse:"filter,sort" indexed:"true"`
	Items    []lineItem       `json:"items"     db:"items"     parse:"filter"`
	Notes    []map[string]any `json:"notes"     db:"notes"     parse:"filter" elements:"by:string;priority:int"`
	ParentID *string          `json:"parent_id" db:"parent_id" parse:"filter"`
	Meta     json.RawMessage  `json:"meta"      db:"meta"      parse:"filter" paths:"tags:[]string"`
}

// orderColumns are orderRows' columns.
var orderColumns = []string{"id", "items", "notes", "parent_id", "meta"}

// orderRows are the element fixture's rows: id, the items and notes
// documents as JSON text, the parent order and the meta document; nil is
// NULL. o2 and o3 are o1's children, o5 and o6 o4's.
var orderRows = [][5]any{
	{"o1", `[{"sku":"a","qty":1},{"sku":"b","qty":5,"dims":{"w":2.5}}]`, `[{"by":"ann","priority":1}]`, nil, nil},
	{"o2", `[{"sku":"a","qty":3}]`, `[{"by":"bob","priority":5},{"by":"ann","priority":4}]`, "o1", `{"tags":["x"]}`},
	{"o3", `[]`, nil, "o1", `{"tags":[]}`},
	{"o4", nil, `[]`, nil, `{"tags":["x"]}`},
	{"o5", `{"sku":"a","qty":9}`, `{"by":"ann"}`, "o4", `{"tags":"x"}`},
	{"o6", `["a",{"sku":"a","qty":4}]`, `["ann"]`, "o4", `{"tags":["y"]}`},
}

// orderResource is the fixture's resource on p; children is its
// self-relation, whose predicates read the orders table under an alias.
func orderResource(t *testing.T, p *parse.Provider) *parse.Resource[order] {
	t.Helper()
	orders := parse.MustResource[order](p, "orders")
	orders.MustRelate("children", parse.HasMany(orders, "parent_id", parse.Indexed()))
	return orders
}

var elementCases = []struct {
	name, filter string
	want         []string
}{
	{"one element holds every predicate", `{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}`, []string{"o2", "o6"}},
	{"none: documents without such an element", `{"items":{"$none":{"sku":"a"}}}`, []string{"o3", "o4", "o5"}},
	{"some element at all; an object is not an array", `{"items":{"$some":{}}}`, []string{"o1", "o2", "o6"}},
	{"nested member of an element", `{"items":{"$some":{"dims.w":{"$gte":2}}}}`, []string{"o1"}},
	{"declared element paths of a map array", `{"notes":{"$some":{"by":"ann","priority":{"$gte":4}}}}`, []string{"o2"}},
	{"with other conditions", `{"$or":[{"id":"o3"},{"notes":{"$some":{"by":"bob"}}}]}`, []string{"o2", "o3"}},
	// An element condition is compared by containment on PostgreSQL unless
	// a $not inside the element filter encloses it: EXISTS selects its
	// elements whatever negation encloses it.
	{"not over some", `{"$not":{"items":{"$some":{"sku":"a"}}}}`, []string{"o3", "o4", "o5"}},
	{"not over none", `{"$not":{"items":{"$none":{"sku":"a"}}}}`, []string{"o1", "o2", "o6"}},
	{"not inside some excludes members of no object", `{"items":{"$some":{"$not":{"sku":"a"}}}}`, []string{"o1"}},
}

// elementRelationCases are element predicates through the children
// self-relation, which rebases the related fields onto an alias while each
// element predicate binds its own element.
var elementRelationCases = []struct {
	name, filter string
	want         []string
}{
	{"an element path through a relation", `{"children":{"$some":{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}}}`, []string{"o1", "o4"}},
	{"an array path's element through a relation", `{"children":{"$some":{"meta.tags":{"$some":{"$eq":"x"}}}}}`, []string{"o1"}},
	{"no related row with an element", `{"children":{"$none":{"items":{"$some":{}}}}}`, []string{"o2", "o3", "o5", "o6"}},
}

// The reference evaluator selects the rows the SQL dialects select.
func TestElementCasesEvaluate(t *testing.T) {
	orders := orderResource(t, onPostgres)
	for _, tc := range elementCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			require.NoError(t, err)
			var got []string
			for _, r := range orderRows {
				row := filter.ColumnRow(func(column string) (any, bool) {
					_, name, _ := strings.Cut(column, ".")
					i := slices.Index(orderColumns, name)
					if i < 0 {
						return nil, false
					}
					return r[i], true
				})
				truth, err := filter.Evaluate(req.Filter().AST().Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					got = append(got, r[0].(string))
				}
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
