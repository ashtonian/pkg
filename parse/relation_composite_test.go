//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

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

func crResources(t *testing.T) (*parse.Resource[crStock], *parse.Resource[crMove], *parse.Resource[crTag]) {
	t.Helper()
	stock, err := parse.NewResource[crStock]("cr_stock", parse.PrimaryKey("warehouse_id", "sku"))
	require.NoError(t, err)
	moves, err := parse.NewResource[crMove]("cr_moves")
	require.NoError(t, err)
	tags, err := parse.NewResource[crTag]("cr_tags")
	require.NoError(t, err)
	return stock, moves, tags
}

// A relation's columns pair by position with the key they hold: as many,
// each named once, each a column of its table holding the same kind of key
// as its pair. Errors name the relation and the columns.
func TestRelate_CompositeKeyDeclarations(t *testing.T) {
	key := []string{"warehouse_id", "sku"}
	for _, tt := range []struct {
		name    string
		owner   string
		rel     func(stock *parse.Resource[crStock], moves *parse.Resource[crMove], tags *parse.Resource[crTag]) parse.Relation
		wantErr string
	}{
		{
			name:  "has-many on the owner's key",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], moves *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.HasMany(moves, key, parse.Includable(), parse.Facetable())
			},
		},
		{
			name:  "belongs-to the target's key",
			owner: "moves",
			rel: func(stock *parse.Resource[crStock], _ *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.BelongsTo(stock, key)
			},
		},
		{
			name:  "many-to-many from the key",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], _ *parse.Resource[crMove], tags *parse.Resource[crTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", key, "tag_id"), parse.Includable(), parse.Facetable(), parse.Writable())
			},
		},
		{
			name:  "many-to-many to the key",
			owner: "tags",
			rel: func(stock *parse.Resource[crStock], _ *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", key), parse.Includable(), parse.Facetable(), parse.Writable())
			},
		},
		{
			name:  "has-many foreign key shorter than the key",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], moves *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.HasMany(moves, "sku")
			},
			wantErr: `relation "rel": foreign key (sku) pairs by position with cr_stock's key (warehouse_id, sku), so it names as many columns`,
		},
		{
			name:  "belongs-to foreign key naming a column twice",
			owner: "moves",
			rel: func(stock *parse.Resource[crStock], _ *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.BelongsTo(stock, []string{"sku", "sku"})
			},
			wantErr: `foreign key names "sku" twice`,
		},
		{
			name:  "paired columns of different key kinds",
			owner: "moves",
			rel: func(stock *parse.Resource[crStock], _ *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.BelongsTo(stock, []string{"code", "sku"})
			},
			wantErr: "cr_moves.code is string and cr_stock.warehouse_id, its pair, is int",
		},
		{
			name:  "unknown foreign key column",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], moves *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.HasMany(moves, []string{"warehouse_id", "skew"})
			},
			wantErr: `foreign key "skew" is not a column of cr_moves`,
		},
		{
			name:  "pair table source shorter than the owner's key",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], _ *parse.Resource[crMove], tags *parse.Resource[crTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", "stock_id", "tag_id"))
			},
			wantErr: "pair table cr_stock_tags's source (stock_id) pairs by position with cr_stock's key (warehouse_id, sku), so it names as many columns",
		},
		{
			name:  "pair table target longer than the target's key",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], _ *parse.Resource[crMove], tags *parse.Resource[crTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", key, []string{"tag_id", "kind"}))
			},
			wantErr: "pair table cr_stock_tags's target (tag_id, kind) pairs by position with cr_tags's key (id), so it names as many columns",
		},
		{
			name:  "include on another owner column",
			owner: "stock",
			rel: func(_ *parse.Resource[crStock], moves *parse.Resource[crMove], _ *parse.Resource[crTag]) parse.Relation {
				return parse.HasMany(moves, "sku", parse.References("sku"), parse.Includable())
			},
			wantErr: "an include groups related keys by the owner's key (warehouse_id, sku), and the relation correlates on sku",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stock, moves, tags := crResources(t)
			owners := map[string]interface {
				Relate(string, parse.Relation) error
			}{"stock": stock, "moves": moves, "tags": tags}
			err := owners[tt.owner].Relate("rel", tt.rel(stock, moves, tags))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// A relation to a key of several columns names no one value: its field
// takes the NULL tests and the relation predicates, not a key comparison,
// and the schema document lists its operators so.
func TestRelate_CompositeTargetField(t *testing.T) {
	stock, moves, tags := crResources(t)
	require.NoError(t, moves.Relate("stock", parse.BelongsTo(stock, []string{"warehouse_id", "sku"})))
	require.NoError(t, tags.Relate("stock", parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", []string{"warehouse_id", "sku"}), parse.Indexed())))

	for _, doc := range []string{
		`{"stock":{"$is":{"name":"bolt"}}}`,
		`{"stock":{"$is_not":{"sku":"a"}}}`,
		`{"stock":{"$null":true}}`,
	} {
		_, err := moves.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":{"$some":{"warehouse_id":1}}}`, `{"stock":{"$none":{}}}`, `{"stock":{"$null":false}}`} {
		_, err := tags.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":"a"}`, `{"stock":{"$in":[[1,"a"]]}}`} {
		_, err := tags.ParseFilter(doc)
		require.Error(t, err, doc)
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	field := reg.SchemaFor("cr_tags").Filter.Fields["stock"]
	assert.Equal(t, core.FieldTypeArray, field.DataType)
	assert.Subset(t, field.Operators, []filter.FilterOperator{filter.OpNull, filter.OpNotNull, filter.OpSome, filter.OpNone})
	for _, op := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin} {
		assert.NotContains(t, field.Operators, op)
	}
}

// A membership whose target key has several columns takes each key as an
// array of a value per column, each normalized as its column, and a repeat
// once; the schema document describes each column.
func TestRelate_CompositeMembership(t *testing.T) {
	stock, _, tags := crResources(t)
	require.NoError(t, tags.Relate("stock", parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", []string{"warehouse_id", "sku"}), parse.Writable())))

	parsed, err := tags.ParseMergePatch([]byte(`{"stock":[[1,"a"],[1,"a"],[2,"b"]]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{core.Key{int64(1), "a"}, core.Key{int64(2), "b"}}, parsed.Operations[0].Value)

	for _, body := range []string{`{"stock":[[1]]}`, `{"stock":[[1,"a","x"]]}`, `{"stock":["1:a"]}`, `{"stock":[["one","a"]]}`} {
		_, err := tags.ParseMergePatch([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	path := reg.SchemaFor("cr_tags").Patch.Paths["/stock"]
	require.NotNil(t, path)
	assert.True(t, path.Relation)
	assert.Equal(t, []*parse.RelationKeyResponse{{DataType: core.FieldTypeInt, NumberKind: "int64"}, {DataType: core.FieldTypeString}}, path.Key)
}

type crPairTag struct {
	ID    string       `json:"id"    db:"id"    parse:"filter,sort"`
	Stock []crStockRef `json:"stock" db:"-"     relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter,include,facet,patch" indexed:"true"`
}

type crStockRef struct {
	WarehouseID int64  `db:"warehouse_id" json:",string"`
	SKU         string `db:"sku"`
}

// A pair table tagged with a target key of several columns is typed by the
// struct its field holds: the filter takes only the NULL tests, and the
// membership each key as an array of a value per column, as a declared
// relation's; the schema document describes each column, its wire form
// included.
func TestRelationTag_CompositeTarget(t *testing.T) {
	tags, err := parse.NewResource[crPairTag]("cr_tags")
	require.NoError(t, err)

	for _, doc := range []string{`{"stock":{"$null":true}}`, `{"stock":{"$null":false}}`, `{"stock":{"$exists":true}}`} {
		_, err := tags.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":"a"}`, `{"stock":{"$in":[[1,"a"]]}}`, `{"stock":{"$some":{}}}`} {
		_, err := tags.ParseFilter(doc)
		require.Error(t, err, doc)
	}

	parsed, err := tags.ParseMergePatch([]byte(`{"stock":[["1","a"],["1","a"],["2","b"]]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{core.Key{int64(1), "a"}, core.Key{int64(2), "b"}}, parsed.Operations[0].Value)
	for _, body := range []string{`{"stock":[["1"]]}`, `{"stock":["1:a"]}`, `{"stock":[["one","a"]]}`} {
		_, err := tags.ParseMergePatch([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	schema := reg.SchemaFor("cr_tags")
	field := schema.Filter.Fields["stock"]
	assert.Equal(t, core.FieldTypeArray, field.DataType)
	assert.ElementsMatch(t, []filter.FilterOperator{filter.OpNull, filter.OpNotNull, filter.OpExists}, field.Operators)
	path := schema.Patch.Paths["/stock"]
	require.NotNil(t, path)
	assert.True(t, path.Relation)
	assert.Equal(t, []*parse.RelationKeyResponse{{DataType: core.FieldTypeInt, NumberKind: "int64", Wire: core.WireString}, {DataType: core.FieldTypeString}}, path.Key)
}

// A tagged field whose target key has several columns refuses an element
// that is no struct, a column its struct does not hold and an operator that
// names one key value.
func TestRelationTag_CompositeTargetRefusals(t *testing.T) {
	type flat struct {
		ID    string   `json:"id"    db:"id"`
		Stock []string `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter"`
	}
	type missing struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+bin" parse:"filter"`
	}
	type compared struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter" filter_ops:"eq,null"`
	}
	_, err := parse.NewResource[flat]("cr_tags")
	require.ErrorContains(t, err, "target key warehouse_id+sku has 2 columns; the field holds string, not a struct holding a field per column")
	_, err = parse.NewResource[missing]("cr_tags")
	require.ErrorContains(t, err, "relation tag target column bin is no column of parse_test.crStockRef")
	_, err = parse.NewResource[compared]("cr_tags")
	require.ErrorContains(t, err, "relation target key warehouse_id+sku has 2 columns, which $eq cannot name")

	type nullTests struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter" filter_ops:"null,exists" indexed:"true"`
	}
	_, err = parse.NewResource[nullTests]("cr_tags")
	assert.NoError(t, err, "every NULL test asks whether the owner has pairs")
}
