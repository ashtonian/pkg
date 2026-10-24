//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A relation has no row column: it can be neither sorted nor used as a
// cursor key, so a sort tag on one fails generation instead of producing an
// unpageable, unscoped count subquery.
func TestGenerateSortSchema_RejectsSortableRelation(t *testing.T) {
	type widget struct {
		ID   string   `db:"id" json:"id" parse:"sort"`
		Tags []string `db:"-" json:"-" parse:"filter,sort" filter_ops:"eq,in" relation:"widget_tags,widget_id,tag_id"`
	}
	_, err := filter.GenerateSortSchema[widget](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, "relation fields are not sortable")
}

func TestGenerateSortSchema_IgnoresRelationWithoutSortVerb(t *testing.T) {
	type widget struct {
		ID   string   `db:"id" json:"id" parse:"sort"`
		Tags []string `db:"-" json:"-" parse:"filter" relation:"widget_tags,widget_id,tag_id"`
	}
	schema, err := filter.GenerateSortSchema[widget](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Len(t, schema.Fields, 1)
	assert.Equal(t, "id", schema.Fields["id"].Column)
}
