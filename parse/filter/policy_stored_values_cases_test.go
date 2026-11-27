//go:build unit || integration

package filter_test

import (
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// stockCode is a codec: the driver binds and stores it upper-cased.
type stockCode string

func (c stockCode) Value() (driver.Value, error) { return strings.ToUpper(string(c)), nil }

// stockedCode is a stock_items row, its sku as the driver stored it.
type stockedCode struct {
	WarehouseID string
	SKU         string
	Qty         int64
}

// stockCodeSchema keys stock_items by (warehouse_id, sku).
func stockCodeSchema() *filter.ResourceSchema {
	field := func(name string, t core.FieldType) *filter.FieldSchema {
		field := &filter.FieldSchema{ExternalName: name, Table: "stock_items", Column: name, DataType: t}
		return field
	}
	schema := &filter.ResourceSchema{
		Name:         "stock_items",
		PrimaryKey:   []string{"warehouse_id", "sku"},
		Fields:       map[string]*filter.FieldSchema{"qty": field("qty", core.FieldTypeInt)},
		SystemFields: map[string]*filter.FieldSchema{"warehouse_id": field("warehouse_id", core.FieldTypeString), "sku": field("sku", core.FieldTypeString)},
	}
	return schema
}

// runPolicyStoredValues holds a policy's Go values to what the driver
// stores for them: a key position, a scalar operand and a set member that
// is a codec, a NULL-able type or a scanned interface names the stored row,
// negated or not, in SQL and in filter.Evaluate alike.
func runPolicyStoredValues(t *testing.T, db *sql.DB, d filter.Dialect) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `CREATE TABLE stock_items (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	rows := []stockedCode{{"w1", "A", 1}, {"w1", "B", 2}, {"w2", "A", 3}, {"w2", "B", 4}}
	insert := "INSERT INTO stock_items VALUES (" + d.Placeholder(1) + ", " + d.Placeholder(2) + ", " + d.Placeholder(3) + ")"
	for _, r := range rows {
		_, err := db.ExecContext(t.Context(), insert, r.WarehouseID, r.SKU, r.Qty)
		require.NoError(t, err)
	}
	key := []string{"warehouse_id", "sku"}
	scanned := func(v any) *any { return &v }
	for _, tc := range []struct {
		name string
		c    *filter.PolicyConstraint
		want []int64
	}{
		{"a key set", &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", stockCode("a")}, {"w2", stockCode("b")}}}, []int64{1, 4}},
		{"a negated key set", &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", stockCode("a")}}, Negate: true}, []int64{2, 3, 4}},
		{"a key", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w2", stockCode("a")}}, []int64{3}},
		{"a negated key", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w2", stockCode("a")}, Negate: true}, []int64{1, 2, 4}},
		{"a one-column key set", &filter.PolicyConstraint{Fields: key[1:], Operator: filter.OpIn, Value: []core.Key{{stockCode("b")}}}, []int64{2, 4}},
		{"an operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: stockCode("a")}, []int64{1, 3}},
		{"a negated operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: stockCode("a"), Negate: true}, []int64{2, 4}},
		{"a NULL-able operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: sql.NullString{String: "B", Valid: true}}, []int64{2, 4}},
		{"set members", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpIn, Value: []any{stockCode("b"), scanned(stockCode("z"))}}, []int64{2, 4}},
		{"negated set members", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpIn, Value: []any{scanned(stockCode("b"))}, Negate: true}, []int64{1, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := filter.ApplyPolicyConstraints(stockCodeSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{tc.c}})
			require.NoError(t, err)
			res, err := filter.NewSQLBuilder(stockCodeSchema(), d).Build(ast)
			require.NoError(t, err)
			rs, err := db.QueryContext(t.Context(), "SELECT qty FROM stock_items WHERE "+res.Where+" ORDER BY qty", res.Args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rs.Close()) }()
			var selected []int64
			for rs.Next() {
				var qty int64
				require.NoError(t, rs.Scan(&qty))
				selected = append(selected, qty)
			}
			require.NoError(t, rs.Err())
			assert.Equal(t, tc.want, selected, "SQL")

			var evaluated []int64
			for _, r := range rows {
				values := map[string]any{"warehouse_id": r.WarehouseID, "sku": r.SKU, "qty": r.Qty}
				truth, err := filter.Evaluate(ast.Root, filter.RowFunc(func(f *filter.FieldSchema) (any, bool, error) {
					v, ok := values[f.Column]
					return v, ok, nil
				}))
				require.NoError(t, err)
				if truth == filter.True {
					evaluated = append(evaluated, r.Qty)
				}
			}
			assert.Equal(t, tc.want, evaluated, "Evaluate")
		})
	}
}
