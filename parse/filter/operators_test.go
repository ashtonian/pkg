//go:build unit

package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A schema cannot advertise an operator its type has no meaning for
// .
func TestGenerateFilterSchema_RejectsInapplicableOperators(t *testing.T) {
	type likeOnInt struct {
		N int `json:"n" parse:"filter" filter_ops:"eq,like"`
	}
	_, err := GenerateFilterSchema[likeOnInt](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, `operator "like" does not apply to int fields`)

	type eqOnArray struct {
		Tags []string `json:"tags" parse:"filter" filter_ops:"eq"`
	}
	_, err = GenerateFilterSchema[eqOnArray](core.NewSchemaGenerator())
	assert.Error(t, err)

	type containsOnRelation struct {
		Tags []string `json:"-" parse:"filter" filter_ops:"contains" relation:"item_tags,item_id,tag_id"`
	}
	_, err = GenerateFilterSchema[containsOnRelation](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, "relation (string target)")

	type bad struct {
		N int `json:"n" parse:"filter"`
	}
	g := core.NewSchemaGenerator(func(o *core.GeneratorOptions) {
		o.DefaultFilterOps = map[core.FieldType][]string{core.FieldTypeInt: {"eq", "contains"}}
	})
	_, err = GenerateFilterSchema[bad](g)
	assert.Error(t, err, "DefaultFilterOps are held to the same table")
}

func TestGenerateFilterSchema_DefaultsFollowTypeModel(t *testing.T) {
	type doc struct {
		Labels  map[string]any  `json:"labels"  parse:"filter"`
		Tags    []string        `json:"tags"    parse:"filter"`
		Tiers   []any           `json:"tiers"   parse:"filter"`
		Counts  []int64         `json:"counts"  parse:"filter"`
		Profile struct{ A int } `json:"profile" parse:"filter"`
	}
	s, err := GenerateFilterSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeJSON, s.Fields["labels"].DataType)
	assert.Equal(t, []FilterOperator{OpEq, OpNe, OpContains, OpHasKey, OpExists, OpNull}, s.Fields["labels"].AllowedOperators)
	assert.Equal(t, core.FieldTypeArray, s.Fields["tags"].DataType)
	assert.Equal(t, core.FieldTypeString, s.Fields["tags"].Elem)
	assert.Equal(t, []FilterOperator{OpContains, OpNull}, s.Fields["tags"].AllowedOperators)
	assert.Equal(t, core.FieldTypeJSON, s.Fields["tiers"].DataType, "[]any is a document, not a native array")
	assert.Equal(t, core.FieldTypeInt, s.Fields["counts"].Elem)
	assert.Empty(t, s.Fields["profile"].AllowedOperators, "struct fields opt in explicitly")
}
