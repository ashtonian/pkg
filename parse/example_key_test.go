//go:build unit

package parse_test

import (
	"fmt"
	"net/url"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type StockItem struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
}

// A resource keyed by two columns: a list's keyset ends on both, so a page
// resumes after exactly one row; an update reaches one row by its whole
// key, and several rows by a set of keys bound as one parameter.
func ExamplePrimaryKey() {
	stock := parse.MustResource[StockItem]("stock", parse.PrimaryKey("warehouse_id", "sku"))

	q, _ := url.ParseQuery(`filter={"qty":{"$gt":0}}&sort=-qty&page_size=2`)
	req, err := stock.ParseList(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	list, err := stock.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(list.Page.SQL)
	fmt.Println(list.Page.Args)

	parsed, err := stock.ParseMergePatch([]byte(`{"qty":7}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, target := range []parse.Target{
		parse.ByKey(parse.Key{"w1", "a"}),
		parse.ByKeys(parse.Key{"w1", "a"}, parse.Key{"w2", "a"}),
	} {
		update, err := stock.BuildUpdate(filter.PostgresDialect{}, parsed, target, parse.Scope{})
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(update.Owner.SQL)
		fmt.Println(update.Owner.Args)
	}
	// Output:
	// SELECT "stock"."warehouse_id", "stock"."sku", "stock"."qty" FROM "stock" WHERE "stock"."qty" > $1 ORDER BY "stock"."qty" DESC, "stock"."warehouse_id" COLLATE pg_c_utf8 DESC, "stock"."sku" COLLATE pg_c_utf8 DESC LIMIT 3
	// [0]
	// UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id" = $2 AND "stock"."sku" = $3) RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
	// [7 w1 a]
	// UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id", "stock"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock", $2) AS "k") RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
	// [7 [{"sku":"a","warehouse_id":"w1"},{"sku":"a","warehouse_id":"w2"}]]
}
