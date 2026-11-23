//go:build unit

package filter_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type rankedItem struct {
	ID    int64   `json:"id" db:"id"`
	Ranks []int64 `json:"-" parse:"filter" relation_name:"ranks" relation:"item_ranks,item_id,rank_id" filter_ops:"eq,in,gt,null" indexed:"true"`
}

// A pair table the relation tag declares compiles through the one relation
// builder; an operator beyond key equality builds in SQL and is refused
// outside it.
func TestPairTable_OperatorsBeyondEquality(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[rankedItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	// The owner's key, which a resource supplies (parse.NewResource).
	schema.Fields["ranks"].RelatedBy = []*filter.FieldSchema{{ExternalName: "id", Table: "ranked_items", Column: "id", DataType: core.FieldTypeInt}}
	parsed, err := filter.NewFilterParser(schema, nil).Parse(`{"ranks":{"$gt":3}}`)
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(parsed)
	require.NoError(t, err)
	assert.Equal(t, `EXISTS (SELECT 1 FROM "item_ranks" WHERE "item_ranks"."item_id" = "ranked_items"."id" AND "item_ranks"."rank_id" > $1)`, res.Where)
	assert.Equal(t, []any{int64(3)}, res.Args)

	_, err = filter.ResolveMemberships(context.Background(), filter.NewParsedFilter(parsed), noLookup{})
	assert.Equal(t, filter.ECodeInvalidRelation.String(), errorCode(t, err))
	assert.ErrorContains(t, err, "relation 'ranks' compares its key with $gt, which only SQL evaluates")

	for filterJSON, want := range map[string]string{
		`{"ranks":{"$in":[]}}`:      "1=0",
		`{"ranks":{"$null":true}}`:  `NOT EXISTS (SELECT 1 FROM "item_ranks" WHERE "item_ranks"."item_id" = "ranked_items"."id")`,
		`{"ranks":{"$null":false}}`: `EXISTS (SELECT 1 FROM "item_ranks" WHERE "item_ranks"."item_id" = "ranked_items"."id")`,
	} {
		parsed, err := filter.NewFilterParser(schema, nil).Parse(filterJSON)
		require.NoError(t, err, filterJSON)
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(parsed)
		require.NoError(t, err, filterJSON)
		assert.Equal(t, want, res.Where, filterJSON)
	}
}

type stockRef struct {
	WarehouseID int64  `db:"warehouse_id"`
	SKU         string `db:"sku"`
}

type pairTag struct {
	ID    string     `json:"id" db:"id"`
	Stock []stockRef `json:"stock" db:"-" parse:"filter" relation:"stock_tags,tag_id,warehouse_id+sku" indexed:"true"`
}

// A pair table compared by a target key of several columns asks only whether
// the owner has pairs; a comparison built without the parser, which one
// value cannot answer, is refused in SQL and outside it alike.
func TestPairTable_KeyOfSeveralColumns(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[pairTag](core.NewSchemaGenerator())
	require.NoError(t, err)
	field := schema.Fields["stock"]
	field.RelatedBy = []*filter.FieldSchema{{ExternalName: "id", Table: "pair_tags", Column: "id", DataType: core.FieldTypeString}}
	parsed, err := filter.NewFilterParser(schema, nil).Parse(`{"stock":{"$null":true}}`)
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(parsed)
	require.NoError(t, err)
	assert.Equal(t, `NOT EXISTS (SELECT 1 FROM "stock_tags" WHERE "stock_tags"."tag_id" = "pair_tags"."id")`, res.Where)

	compared := &filter.FilterAST{Root: &filter.FilterNode{Field: field, Op: filter.OpEq, Value: "1/a"}}
	_, err = filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(compared)
	require.ErrorContains(t, err, "relation 'stock' compares a key of several columns, which $eq cannot name; it takes $null")
	_, err = filter.ResolveMemberships(context.Background(), filter.NewParsedFilter(compared), noLookup{})
	assert.ErrorContains(t, err, "relation 'stock' compares a key of several columns, which $eq cannot name; it takes $null")
}

type noLookup struct{}

func (noLookup) LookupValues(context.Context, *filter.Lookup) ([]any, error) { return nil, nil }
