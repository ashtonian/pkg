//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// stockSchema keys stock_items by (warehouse_id, sku) and filters qty.
func stockSchema() *filter.ResourceSchema {
	field := func(name string, t core.FieldType) *filter.FieldSchema {
		return &filter.FieldSchema{ExternalName: name, Table: "stock_items", Column: name, DataType: t,
			AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn}}
	}
	return &filter.ResourceSchema{
		Name:         "stock_items",
		PrimaryKey:   []string{"warehouse_id", "sku"},
		Fields:       map[string]*filter.FieldSchema{"qty": field("qty", core.FieldTypeInt)},
		SystemFields: map[string]*filter.FieldSchema{"warehouse_id": field("warehouse_id", core.FieldTypeString), "sku": field("sku", core.FieldTypeString)},
	}
}

func keyWhere(t *testing.T, d filter.Dialect, c *filter.PolicyConstraint) (*filter.SQLResult, error) {
	t.Helper()
	ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
	if err != nil {
		return nil, err
	}
	return filter.NewSQLBuilder(stockSchema(), d).Build(ast)
}

// A key constraint of one column compiles as the constraint on Field does;
// of several, $eq is an AND of equalities and $in one bound set.
func TestKeyConstraint_SQL(t *testing.T) {
	key := []string{"warehouse_id", "sku"}
	for _, tc := range []struct {
		name    string
		dialect filter.Dialect
		c       *filter.PolicyConstraint
		where   string
		args    []any
	}{
		{"one column $eq", filter.PostgresDialect{}, &filter.PolicyConstraint{Fields: key[:1], Operator: filter.OpEq, Value: core.Key{"w1"}},
			`"stock_items"."warehouse_id" = $1`, []any{"w1"}},
		{"one column $in", filter.PostgresDialect{}, &filter.PolicyConstraint{Fields: key[:1], Operator: filter.OpIn, Value: []core.Key{{"w1"}, {"w2"}}},
			`"stock_items"."warehouse_id" = ANY($1)`, []any{[]string{"w1", "w2"}}},
		{"$eq", filter.PostgresDialect{}, &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w1", "a"}},
			`("stock_items"."warehouse_id" = $1 AND "stock_items"."sku" = $2)`, []any{"w1", "a"}},
		{"postgres $in", filter.PostgresDialect{}, &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", "a"}, {"w2", "b"}}},
			`("stock_items"."warehouse_id", "stock_items"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock_items", $1) AS "k")`,
			[]any{`[{"sku":"a","warehouse_id":"w1"},{"sku":"b","warehouse_id":"w2"}]`}},
		{"sqlite $in", filter.SQLiteDialect{}, &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", "a"}, {"w2", "b"}}},
			`("stock_items"."warehouse_id", "stock_items"."sku") IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?1))`,
			[]any{`[["w1","a"],["w2","b"]]`}},
		{"negated $in", filter.SQLiteDialect{}, &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", "a"}}, Negate: true},
			`NOT (("stock_items"."warehouse_id", "stock_items"."sku") IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?1)))`,
			[]any{`[["w1","a"]]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := keyWhere(t, tc.dialect, tc.c)
			require.NoError(t, err)
			assert.Equal(t, tc.where, res.Where)
			assert.Equal(t, tc.args, res.Args)
		})
	}
}

func TestKeyConstraint_RefusesMalformedKeys(t *testing.T) {
	key := []string{"warehouse_id", "sku"}
	for _, tc := range []struct {
		name string
		c    *filter.PolicyConstraint
		want string
	}{
		{"short key", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w1"}}, "key 0 has 1 values; the key has 2 columns"},
		{"long key in a set", &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", "a"}, {"w1", "a", "x"}}}, "key 1 has 3 values"},
		{"no key value", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: "w1"}, "value string is no core.Key"},
		{"set under $eq", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: []core.Key{{"w1", "a"}}}, "$eq takes one core.Key"},
		{"other operator", &filter.PolicyConstraint{Fields: key, Operator: filter.OpGt, Value: core.Key{"w1", "a"}}, `operator "gt"`},
		{"field and fields", &filter.PolicyConstraint{Field: "qty", Fields: key, Operator: filter.OpEq, Value: core.Key{"w1", "a"}}, "Fields alone"},
		{"repeated column", &filter.PolicyConstraint{Fields: []string{"sku", "sku"}, Operator: filter.OpEq, Value: core.Key{"a", "a"}}, `names "sku" twice`},
		{"unknown column", &filter.PolicyConstraint{Fields: []string{"sku", "bin"}, Operator: filter.OpEq, Value: core.Key{"a", "b"}}, `field "bin" is not a field`},
		{"mistyped value", &filter.PolicyConstraint{Fields: []string{"sku", "qty"}, Operator: filter.OpEq, Value: core.Key{"a", "many"}}, "qty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keyWhere(t, filter.PostgresDialect{}, tc.c)
			require.ErrorIs(t, err, filter.ErrPolicyInvalid)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// A key set selects the rows whose columns together equal one key, not the
// rows matching each column alone, on SQLite and in memory alike.
func TestKeyConstraint_SelectsWholeKeys(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE stock_items (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	rows := []stockItem{{"w1", "a", 1}, {"w1", "b", 2}, {"w2", "a", 3}, {"w2", "b", 4}}
	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO stock_items VALUES (?, ?, ?)`, r.WarehouseID, r.SKU, r.Qty)
		require.NoError(t, err)
	}
	c := &filter.PolicyConstraint{Fields: []string{"warehouse_id", "sku"}, Operator: filter.OpIn, Value: []core.Key{{"w1", "b"}, {"w2", "a"}, {"w9", "a"}}}
	res, err := keyWhere(t, filter.SQLiteDialect{}, c)
	require.NoError(t, err)
	var got []int64
	rs, err := db.Query("SELECT qty FROM stock_items WHERE "+res.Where+" ORDER BY qty", res.Args...)
	require.NoError(t, err)
	for rs.Next() {
		var qty int64
		require.NoError(t, rs.Scan(&qty))
		got = append(got, qty)
	}
	require.NoError(t, rs.Close())
	assert.Equal(t, []int64{2, 3}, got)

	ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
	require.NoError(t, err)
	var inMemory []int64
	for _, r := range rows {
		values := map[string]any{"warehouse_id": r.WarehouseID, "sku": r.SKU, "qty": r.Qty}
		truth, err := filter.Evaluate(ast.Root, filter.RowFunc(func(f *filter.FieldSchema) (any, bool, error) {
			v, ok := values[f.Column]
			return v, ok, nil
		}))
		require.NoError(t, err)
		if truth == filter.True {
			inMemory = append(inMemory, r.Qty)
		}
	}
	assert.Equal(t, []int64{2, 3}, inMemory)
}
