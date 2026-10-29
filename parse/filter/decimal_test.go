//go:build unit

package filter_test

import (
	"database/sql/driver"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// fixed is a decimal library type parse does not recognize by shape: its
// column declares it with storage:"scalar:decimal".
type fixed struct{ text string }

func (f fixed) Value() (driver.Value, error) { return f.text, nil }

type priced struct {
	ID    string           `json:"id"    db:"id"    parse:"filter,sort"`
	Price decimal.Decimal  `json:"price" db:"price" parse:"filter,sort"`
	Cost  *decimal.Decimal `json:"cost"  db:"cost"  parse:"filter,sort"`
	Fee   fixed            `json:"fee"   db:"fee"   parse:"filter,sort" storage:"scalar:decimal"`
}

func pricedSchemas(t *testing.T) (*filter.ResourceSchema, *filter.SortSchema) {
	t.Helper()
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[priced](g)
	require.NoError(t, err)
	fs.PrimaryKey = "id"
	ss, err := filter.GenerateSortSchema[priced](g)
	require.NoError(t, err)
	return fs, ss
}

func TestDecimal_Schema(t *testing.T) {
	fs, ss := pricedSchemas(t)
	for _, name := range []string{"price", "cost", "fee"} {
		f := fs.Fields[name]
		require.NotNil(t, f, name)
		assert.Equal(t, core.FieldTypeDecimal, f.DataType, name)
		assert.Equal(t, []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin},
			f.AllowedOperators, name)
		assert.Equal(t, core.FieldTypeDecimal, ss.Fields[name].DataType, name)
	}
	assert.True(t, ss.Fields["cost"].Nullable)
	for _, m := range filter.FilterMetaFromSchema(fs).Fields {
		if m.Name == "price" {
			assert.Equal(t, "decimal", m.Type)
		}
	}
}

func TestDecimal_Literals(t *testing.T) {
	fs, _ := pricedSchemas(t)
	parser := filter.NewFilterParser(fs, nil)
	tests := []struct {
		filter string
		want   any
	}{
		{`{"price":"10.50"}`, "10.5"},
		{`{"price":10.5}`, "10.5"},
		{`{"price":"1.5e1"}`, "15"},
		{`{"price":"0.10000000000000001"}`, "0.10000000000000001"},
		{`{"price":{"$in":["2.0",3,"-0"]}}`, []any{"2", "3", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			ast, err := parser.Parse(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, ast.Root.Value)
		})
	}
	for _, bad := range []string{`{"price":"ten"}`, `{"price":true}`, `{"price":"1e5000"}`, `{"price":{"$in":["1","x"]}}`} {
		_, err := parser.Parse(bad)
		assertParseCode(t, err, filter.ECodeValueTypeMismatch)
	}
}

func TestDecimal_SQLBindsExactNumbers(t *testing.T) {
	fs, _ := pricedSchemas(t)
	ast, err := filter.NewFilterParser(fs, nil).Parse(`{"$and":[{"price":{"$gt":"9.50"}},{"fee":{"$in":["1",2.25]}}]}`)
	require.NoError(t, err)
	tests := []struct {
		dialect filter.Dialect
		where   string
	}{
		{filter.PostgresDialect{}, "(priceds.price > $1::numeric AND priceds.fee IN ($2::numeric, $3::numeric))"},
		{filter.SQLiteDialect{}, "(priceds.price > CAST(?1 AS NUMERIC) AND priceds.fee IN (CAST(?2 AS NUMERIC), CAST(?3 AS NUMERIC)))"},
	}
	for _, tt := range tests {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			res, err := filter.NewSQLBuilder(fs, tt.dialect).Build(ast)
			require.NoError(t, err)
			assert.Equal(t, tt.where, res.Where)
			assert.Equal(t, []any{"9.5", "1", "2.25"}, res.Args)
		})
	}
}

func TestDecimal_QueryParams(t *testing.T) {
	fs, _ := pricedSchemas(t)
	ast, err := filter.NewQueryParamParser(fs).Parse(url.Values{"price_gte": {"10.50"}, "cost": {"1", "2.0"}})
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(fs, filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Equal(t, "(priceds.cost IN ($1::numeric, $2::numeric) AND priceds.price >= $3::numeric)", res.Where)
	assert.Equal(t, []any{"1", "2", "10.5"}, res.Args)
}

// Keys order as numbers, across signs, magnitudes and scales, and never as
// their text ("10" sorts after "9.5").
func TestDecimal_CompareKeyValues(t *testing.T) {
	key := filter.CursorKey{Name: "price", DataType: core.FieldTypeDecimal}
	ascending := []any{"-100", "-1", "-0.5", "0", "0.5", "2", "9.5", "10", "10.25", "100", nil}
	for i, a := range ascending {
		for j, b := range ascending {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			assert.Equal(t, want, filter.CompareKeyValues(key, a, b), "%v vs %v", a, b)
		}
	}
}

func TestDecimal_KeyValue(t *testing.T) {
	key := filter.CursorKey{Name: "price", Column: "price", DataType: core.FieldTypeDecimal}
	tests := []struct {
		name string
		raw  any
		want any
	}{
		{"shopspring", decimal.RequireFromString("10.50"), "10.5"},
		{"shopspring pointer", new(decimal.RequireFromString("-2")), "-2"},
		{"nil pointer", (*decimal.Decimal)(nil), nil},
		{"numeric text keeping its scale", "10.500", "10.5"},
		{"SQLite REAL", 10.25, "10.25"},
		{"SQLite INTEGER", int64(100), "100"},
		{"valuer", fixed{text: "3.10"}, "3.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filter.KeyValue(key, tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	_, err := filter.KeyValue(key, "cheap")
	assert.Error(t, err)

	got, err := filter.CursorValue(decimal.RequireFromString("1.50"))
	require.NoError(t, err)
	assert.Equal(t, "1.5", got)
}

func TestDecimal_ValidateCursor(t *testing.T) {
	keys := []filter.CursorKey{{Name: "price", DataType: core.FieldTypeDecimal, Direction: filter.SortAsc}}
	token := &filter.PageToken{Fields: []filter.CursorField{{Name: "price", Value: "10.50", Direction: filter.SortAsc}}}
	require.NoError(t, filter.ValidateCursor(token, keys))
	assert.Equal(t, "10.5", token.Fields[0].Value)

	token = &filter.PageToken{Fields: []filter.CursorField{{Name: "price", Value: "ten", Direction: filter.SortAsc}}}
	assertParseCode(t, filter.ValidateCursor(token, keys), filter.ECodeInvalidCursor)
}

func TestDecimal_SeekBindsExactNumbers(t *testing.T) {
	fs, ss := pricedSchemas(t)
	cur := mintCursor(t, false,
		filter.CursorField{Name: "price", Value: "9.5", Direction: filter.SortAsc},
		filter.CursorField{Name: "id", Value: "b", Direction: filter.SortAsc},
	)
	tests := []struct {
		dialect filter.Dialect
		where   string
	}{
		{filter.PostgresDialect{}, `(priceds.price, priceds.id COLLATE pg_c_utf8) > ($1::numeric, $2)`},
		{filter.SQLiteDialect{}, `((priceds.price > CAST(?1 AS NUMERIC)) OR (priceds.price = CAST(?1 AS NUMERIC) AND priceds.id COLLATE BINARY > ?2))`},
	}
	for _, tt := range tests {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			res, err := filter.NewListQuery(fs, ss, tt.dialect).Build(filter.ListSort("price,id"), filter.ListCursor(cur))
			require.NoError(t, err)
			assert.Contains(t, res.Where, tt.where)
			assert.Equal(t, []any{"9.5", "b"}, res.Args)
		})
	}
}

func TestDecimal_Paths(t *testing.T) {
	type doc struct {
		ID    string         `json:"id"    db:"id"    parse:"filter"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter" paths:"price:decimal:filter+sort"`
	}
	fs, err := filter.GenerateFilterSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	f := fs.Fields["attrs.price"]
	require.NotNil(t, f)
	assert.Equal(t, core.FieldTypeDecimal, f.DataType)

	for _, tt := range []struct {
		doc     string
		want    any
		present bool
	}{
		{`{"price":10.50}`, "10.5", true},
		{`{"price":"10.50"}`, "10.5", true},
		{`{"price":"-0.25"}`, "-0.25", true},
		{`{"price":"1e3"}`, nil, false},
		{`{"price":"10."}`, nil, false},
		{`{"price":"010"}`, nil, false},
		{`{"price":true}`, nil, false},
		{`{"price":null}`, nil, false},
		{`{}`, nil, false},
	} {
		got, present, err := filter.PathValue(tt.doc, f)
		require.NoError(t, err, tt.doc)
		assert.Equal(t, tt.present, present, tt.doc)
		assert.Equal(t, tt.want, got, tt.doc)
	}
}

func TestDecimal_StorageScalar(t *testing.T) {
	type badScalar struct {
		Fee fixed `json:"fee" db:"fee" parse:"filter" storage:"scalar:money"`
	}
	_, err := filter.GenerateFilterSchema[badScalar](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, "a scalar is string, int, float, decimal, bool, timestamp or uuid")
}
