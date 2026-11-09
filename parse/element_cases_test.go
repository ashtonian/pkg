//go:build unit || integration

package parse_test

import (
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
	ID    string           `json:"id"    db:"id"    parse:"filter,sort" indexed:"true"`
	Items []lineItem       `json:"items" db:"items" parse:"filter"`
	Notes []map[string]any `json:"notes" db:"notes" parse:"filter" elements:"by:string;priority:int"`
}

// orderRows are the element fixture's rows: id, then the items and notes
// documents as JSON text, nil for NULL.
var orderRows = [][3]any{
	{"o1", `[{"sku":"a","qty":1},{"sku":"b","qty":5,"dims":{"w":2.5}}]`, `[{"by":"ann","priority":1}]`},
	{"o2", `[{"sku":"a","qty":3}]`, `[{"by":"bob","priority":5},{"by":"ann","priority":4}]`},
	{"o3", `[]`, nil},
	{"o4", nil, `[]`},
	{"o5", `{"sku":"a","qty":9}`, `{"by":"ann"}`},
	{"o6", `["a",{"sku":"a","qty":4}]`, `["ann"]`},
}

func orderFixtureSQL(docType string) []string {
	literal := func(v any) string {
		if v == nil {
			return "NULL"
		}
		return "'" + v.(string) + "'"
	}
	values := make([]string, len(orderRows))
	for i, r := range orderRows {
		values[i] = "(" + literal(r[0]) + ", " + literal(r[1]) + ", " + literal(r[2]) + ")"
	}
	return []string{
		`CREATE TABLE orders (id TEXT PRIMARY KEY, items ` + docType + `, notes ` + docType + `)`,
		`INSERT INTO orders VALUES ` + strings.Join(values, ", "),
	}
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

func runElementCases(t *testing.T, d filter.Dialect, query func(t *testing.T, sql string, args []any) [][]any) {
	t.Helper()
	orders := parse.MustResource[order]("orders")
	for _, tc := range elementCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			require.NoError(t, err)
			plan, err := orders.BuildList(d, req, parse.Scope{})
			require.NoError(t, err)
			var got []string
			for _, row := range query(t, plan.Page.SQL, plan.Page.Args) {
				id, ok := row[0].(string)
				require.True(t, ok)
				got = append(got, id)
			}
			slices.Sort(got)
			assert.Equal(t, tc.want, got, plan.Page.SQL)
		})
	}

	for _, bad := range []string{
		`{"items":{"$some":{"colour":"red"}}}`,
		`{"items":{"$some":"a"}}`,
		`{"items":{"$is":{}}}`,
	} {
		_, err := orders.ParseList(url.Values{filter.ParamFilter: {bad}})
		assert.Error(t, err, bad)
	}
}

// The reference evaluator selects the rows the SQL dialects select.
func TestElementCasesEvaluate(t *testing.T) {
	orders := parse.MustResource[order]("orders")
	for _, tc := range elementCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			require.NoError(t, err)
			var got []string
			for _, r := range orderRows {
				row := filter.ColumnRow(func(column string) (any, bool) {
					i := slices.Index([]string{"orders.id", "orders.items", "orders.notes"}, column)
					if i < 0 {
						return nil, false
					}
					return r[i], true
				})
				truth, err := filter.Evaluate(req.Filter.AST.Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					got = append(got, r[0].(string))
				}
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
