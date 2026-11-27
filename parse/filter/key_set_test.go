//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// stockSchema keys stock_items by (warehouse_id, sku) and filters qty.
func stockSchema() *filter.ResourceSchema {
	field := func(name string, t core.FieldType) *filter.FieldSchema {
		field := &filter.FieldSchema{ExternalName: name, Table: "stock_items", Column: name, DataType: t,
			AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn}}
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

// A key set compares each column as a filter compares its type: on SQLite a
// decimal through parse_decimal_key on both sides, and on PostgreSQL the
// set's text as numeric. String, UUID and integer columns compare as
// stored.
func TestKeyIn_TypedColumns(t *testing.T) {
	bind := func(args *[]any, d filter.Dialect) func(any) string {
		return func(v any) string { *args = append(*args, v); return d.Placeholder(len(*args)) }
	}
	dec := core.TypeInfo{Type: core.FieldTypeDecimal}
	at := core.TypeInfo{Type: core.FieldTypeTimestamp}
	str := core.TypeInfo{Type: core.FieldTypeString}
	for _, tc := range []struct {
		name    string
		dialect filter.Dialect
		columns []string
		types   []core.TypeInfo
		keys    []core.Key
		member  bool
		sql     string
		args    []any
	}{
		{
			name: "postgres decimal", dialect: filter.PostgresDialect{}, columns: []string{"price"}, types: []core.TypeInfo{dec},
			keys: []core.Key{{"1.50"}, {"12345678901234567890"}}, member: true,
			sql: `"p"."price" = ANY($1::text[]::numeric[])`, args: []any{[]string{"1.5", "12345678901234567890"}},
		},
		{
			name: "sqlite decimal", dialect: filter.SQLiteDialect{}, columns: []string{"price"}, types: []core.TypeInfo{dec},
			keys: []core.Key{{"1.50"}}, member: false,
			sql: `parse_decimal_key("p"."price") NOT IN (SELECT parse_decimal_key(value) FROM json_each(?1))`, args: []any{`["1.5"]`},
		},
		{
			name: "postgres device and instant", dialect: filter.PostgresDialect{}, columns: []string{"device_id", "at"}, types: []core.TypeInfo{str, at},
			keys: []core.Key{{"d1", "2026-01-02T04:04:05+01:00"}}, member: true,
			sql:  `("p"."device_id", "p"."at") IN (SELECT "k"."device_id", "k"."at" FROM jsonb_populate_recordset(NULL::"readings", $1) AS "k")`,
			args: []any{`[{"at":"2026-01-02T03:04:05.000000000Z","device_id":"d1"}]`},
		},
		{
			name: "sqlite device and instant", dialect: filter.SQLiteDialect{}, columns: []string{"device_id", "at"}, types: []core.TypeInfo{str, at},
			keys: []core.Key{{"d1", "2026-01-02T04:04:05+01:00"}}, member: true,
			sql:  `("p"."device_id", "p"."at") IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?1))`,
			args: []any{`[["d1","2026-01-02T03:04:05.000000000Z"]]`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args []any
			table := "readings"
			if tc.columns[0] == "price" {
				table = "prices"
			}
			sql, err := tc.dialect.KeyIn(table, "p", tc.columns, tc.types, tc.keys, tc.member, bind(&args, tc.dialect))
			require.NoError(t, err)
			assert.Equal(t, tc.sql, sql)
			assert.Equal(t, tc.args, args)
		})
	}
	_, err := filter.SQLiteDialect{}.KeyIn("t", "t", []string{"doc"}, []core.TypeInfo{{Type: core.FieldTypeJSON}}, []core.Key{{"{}"}}, true, func(any) string { return "?" })
	assert.ErrorContains(t, err, "doc")
}

// PostgreSQL holds a timestamp at microseconds, rounding a finer one as it
// parses its text (half to even), so a key is held there at that
// precision; SQLite holds the canonical text as given.
func TestStoredKeyValue(t *testing.T) {
	at := core.TypeInfo{Type: core.FieldTypeTimestamp}
	for _, tc := range []struct {
		in, postgres string
	}{
		{"2026-01-02T03:04:05.123456400Z", "2026-01-02T03:04:05.123456000Z"},
		{"2026-01-02T03:04:05.123456600Z", "2026-01-02T03:04:05.123457000Z"},
		{"2026-01-02T03:04:05.000000500Z", "2026-01-02T03:04:05.000000000Z"},
		{"2026-01-02T03:04:05.000001500Z", "2026-01-02T03:04:05.000002000Z"},
		{"2026-01-02T03:04:05.999999700Z", "2026-01-02T03:04:06.000000000Z"},
		{"2026-01-02T03:04:05.000000000Z", "2026-01-02T03:04:05.000000000Z"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := filter.PostgresDialect{}.StoredKeyValue(at, tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.postgres, got)
			got, err = filter.SQLiteDialect{}.StoredKeyValue(at, tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.in, got)
		})
	}
	got, err := filter.PostgresDialect{}.StoredKeyValue(core.TypeInfo{Type: core.FieldTypeDecimal}, "1.5")
	require.NoError(t, err)
	assert.Equal(t, "1.5", got, "only a timestamp is held at another precision")
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

// A key of several columns compares stored columns: a computed column has no
// place in the row type the set is typed by, and its stored column, when one
// shares its name, holds another value than the expression a constraint on
// the field alone reads. Such a key is refused when the policy compiles,
// while one column compiles through the expression as Field does.
func TestKeyConstraint_RefusesAComputedColumnOfSeveral(t *testing.T) {
	schema := stockSchema()
	schema.Computed = []filter.ComputedColumn{{Column: "sku", SQL: `lower("stock_items"."code")`}}
	apply := func(c *filter.PolicyConstraint) (*filter.FilterAST, error) {
		return filter.ApplyPolicyConstraints(schema, nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
	}
	for _, c := range []*filter.PolicyConstraint{
		{Fields: []string{"warehouse_id", "sku"}, Operator: filter.OpIn, Value: []core.Key{{"w1", "a"}}},
		{Fields: []string{"warehouse_id", "sku"}, Operator: filter.OpEq, Value: core.Key{"w1", "a"}},
	} {
		_, err := apply(c)
		require.ErrorIs(t, err, filter.ErrPolicyInvalid)
		require.ErrorContains(t, err, "sku is a computed column")
	}

	ast, err := apply(&filter.PolicyConstraint{Fields: []string{"sku"}, Operator: filter.OpIn, Value: []core.Key{{"a"}}})
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(schema, filter.SQLiteDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Contains(t, res.Where, `lower("stock_items"."code")`)
}
