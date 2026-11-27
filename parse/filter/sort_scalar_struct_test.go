//go:build unit

package filter_test

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// sortCode is an application's codec: a struct its column holds as text.
type sortCode struct{ s string }

func (c sortCode) Value() (driver.Value, error) { return c.s, nil }

// codedItem sorts by a codec its storage tag declares a string.
type codedItem struct {
	ID   int64    `json:"id"   db:"id"`
	Code sortCode `json:"code" db:"code" parse:"sort" storage:"scalar:string"`
}

// A struct sort key orders as the scalar its column holds, which only its
// storage tag names: untagged, the database would order the column by its
// own rules (PostgreSQL a text column by its collation) while a cursor
// compares the codec's value by code point, so generation refuses it and
// names the tag that fixes it.
func TestGenerateSortSchema_StructKeyDeclaresItsScalar(t *testing.T) {
	type untagged struct {
		Code sortCode `json:"code" db:"code" parse:"sort"`
	}
	_, err := filter.GenerateSortSchema[untagged](core.NewSchemaGenerator())
	require.Error(t, err)
	require.ErrorContains(t, err, "field Code")
	require.ErrorContains(t, err, `storage:"scalar:<type>"`)

	sorts, err := filter.GenerateSortSchema[codedItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeString, sorts.Fields["code"].DataType)
}
