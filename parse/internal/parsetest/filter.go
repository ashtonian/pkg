//go:build unit || integration

package parsetest

import (
	"encoding/json"
	"testing"

	"github.com/ashtonian/pkg/parse"
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

type Order struct {
	ID       string           `json:"id"        db:"id"        parse:"filter,sort" indexed:"true"`
	Items    []lineItem       `json:"items"     db:"items"     parse:"filter"`
	Notes    []map[string]any `json:"notes"     db:"notes"     parse:"filter" elements:"by:string;priority:int"`
	ParentID *string          `json:"parent_id" db:"parent_id" parse:"filter"`
	Meta     json.RawMessage  `json:"meta"      db:"meta"      parse:"filter" paths:"tags:[]string"`
}

// OrderColumns are OrderRows' columns.
func OrderColumns() []string {
	columns := []string{"id", "items", "notes", "parent_id", "meta"}
	return columns
}

// OrderRows are the element fixture's rows: id, the items and notes
// documents as JSON text, the parent order and the meta document; nil is
// NULL. o2 and o3 are o1's children, o5 and o6 o4's.
func OrderRows() [][5]any {
	rows := [][5]any{
		{"o1", `[{"sku":"a","qty":1},{"sku":"b","qty":5,"dims":{"w":2.5}}]`, `[{"by":"ann","priority":1}]`, nil, nil},
		{"o2", `[{"sku":"a","qty":3}]`, `[{"by":"bob","priority":5},{"by":"ann","priority":4}]`, "o1", `{"tags":["x"]}`},
		{"o3", `[]`, nil, "o1", `{"tags":[]}`},
		{"o4", nil, `[]`, nil, `{"tags":["x"]}`},
		{"o5", `{"sku":"a","qty":9}`, `{"by":"ann"}`, "o4", `{"tags":"x"}`},
		{"o6", `["a",{"sku":"a","qty":4}]`, `["ann"]`, "o4", `{"tags":["y"]}`},
	}
	return rows
}

// OrderResource is the fixture's resource on p; children is its
// self-relation, whose predicates read the orders table under an alias.
func OrderResource(t *testing.T, p *parse.Provider) *parse.Resource[Order] {
	t.Helper()
	orders := parse.MustResource[Order](p, "orders")
	orders.MustRelate("children", parse.HasMany(orders, "parent_id", parse.Indexed()))
	return orders
}

// ElementCase is an element predicate over the orders and the ids of the
// orders it selects.
type ElementCase struct {
	Name, Filter string
	Want         []string
}

// ElementCases are element predicates over the orders' items and arrays.
func ElementCases() []ElementCase {
	cases := []ElementCase{
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
	return cases
}
