//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type quotedMember struct {
	Qty int64 `json:"qty,string" parse:"filter"`
}

type quotedDocument struct {
	ID    string         `json:"id" parse:"filter"`
	Spec  quotedMember   `json:"spec" parse:"filter"`
	Lines []quotedMember `json:"lines" parse:"filter"`
}

type quotedSortDocument struct {
	ID   string `json:"id" parse:"filter"`
	Spec struct {
		Qty int64 `json:"qty,string" parse:"sort"`
	} `json:"spec" parse:"sort"`
}

// A document holds a quoted member as a JSON string, which a number path
// reads as NULL on every backend: schema generation refuses the member
// rather than filter or sort on it silently.
func TestQuotedDocumentMemberRefused(t *testing.T) {
	_, err := filter.GenerateFilterSchema[quotedDocument](core.NewSchemaGenerator())
	require.Error(t, err)
	assert.ErrorContains(t, err, `json:",string"`)
	assert.ErrorContains(t, err, "qty")

	_, err = filter.GenerateSortSchema[quotedSortDocument](core.NewSchemaGenerator())
	require.Error(t, err)
	assert.ErrorContains(t, err, `json:",string"`)
}

// A column's quoted wire form reaches the schema; an unquoted one stays the
// data type's.
func TestQuotedColumnWire(t *testing.T) {
	type row struct {
		ID    int64 `json:"id,string" parse:"filter"`
		Count int64 `json:"count" parse:"filter"`
	}
	schema, err := filter.GenerateFilterSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, core.WireString, schema.Fields["id"].Wire)
	assert.Equal(t, core.Wire(""), schema.Fields["count"].Wire)
}
