//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestGenerateFilterSchemaExplicitHiddenRelationName(t *testing.T) {
	type row struct {
		TagIDs []string `json:"-" db:"-" parse:"filter,facet,include" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
		Groups []string `json:"-" db:"-" parse:"filter" relation:"row_groups,row_id,group_id"`
	}
	schema, err := filter.GenerateFilterSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	require.Contains(t, schema.Fields, "tag_ids")
	require.NotContains(t, schema.Fields, "tag_i_ds")
	require.Contains(t, schema.Fields, "groups", "handwritten relations keep the existing fallback")
	require.True(t, schema.Fields["tag_ids"].Facetable)
	require.True(t, schema.Fields["tag_ids"].Includable)
	cfg := core.DefaultConfig()
	cfg.MaxCost = 100
	_, err = filter.NewFilterParser(schema, cfg).Parse(`{"tag_ids":{"$in":["one","two"]}}`)
	require.NoError(t, err)
}

func TestGenerateFilterSchemaRelationNameDoesNotOverrideVisibleName(t *testing.T) {
	type row struct {
		Tags []string `json:"tags" db:"-" parse:"filter" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
	}
	schema, err := filter.GenerateFilterSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	require.Contains(t, schema.Fields, "tags")
	require.NotContains(t, schema.Fields, "tag_ids")
}

func TestGenerateFilterSchemaRejectsRelationNameCollision(t *testing.T) {
	type relationFirst struct {
		TagIDs []string `json:"-" db:"-" parse:"filter" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
		Alias  string   `json:"tag_ids" db:"alias" parse:"filter"`
	}
	type scalarFirst struct {
		Alias  string   `json:"tag_ids" db:"alias" parse:"filter"`
		TagIDs []string `json:"-" db:"-" parse:"filter" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
	}
	_, err := filter.GenerateFilterSchema[relationFirst](core.NewSchemaGenerator())
	require.ErrorContains(t, err, `duplicate filter field name "tag_ids"`)
	_, err = filter.GenerateFilterSchema[scalarFirst](core.NewSchemaGenerator())
	require.ErrorContains(t, err, `duplicate filter field name "tag_ids"`)
}
