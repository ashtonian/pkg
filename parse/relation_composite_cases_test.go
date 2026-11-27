//go:build unit || integration

package parse_test

// Stock keyed by (warehouse_id, sku), its moves and its tags: the resources
// of the composite-key relation tests.
type crStock struct {
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Name        string `json:"name"         db:"name"         parse:"filter"`
}

type crMove struct {
	ID          int64  `json:"id"           db:"id"           parse:"filter,sort"`
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter"`
	Code        string `json:"code"         db:"code"         parse:"filter"`
}

type crTag struct {
	ID    string `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"label" db:"label" parse:"filter"`
}
