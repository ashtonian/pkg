//go:build unit

package filter_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// columnRecorder is a ColumnMapper that records the columns it is handed.
type columnRecorder struct{ columns []string }

func (r *columnRecorder) MapColumn(table, column string, _ core.FieldType) string {
	if !slices.Contains(r.columns, column) {
		r.columns = append(r.columns, column)
	}
	return "m_" + table + "." + column
}

type mappedFields struct {
	Name  string `json:"displayName" db:"display_nm" parse:"filter,sort"`
	Owner int    `json:"-"           db:"owner_ref"`
	Note  string `json:"noteText"    parse:"search"`
	Kind  string `json:"kindCode"`
}

// Every view hands a ColumnMapper the field's column: its column tag's name,
// else the convention on its external name, or on its Go name when hidden.
func TestColumnMapper_ReceivesTheColumn(t *testing.T) {
	rec := &columnRecorder{}
	g := core.NewSchemaGenerator(core.WithColumnMapper(rec), core.WithSQLTableName("items"))

	schema, err := filter.GenerateFilterSchema[mappedFields](g)
	require.NoError(t, err)
	_, err = filter.GenerateSortSchema[mappedFields](g)
	require.NoError(t, err)
	_, err = filter.GenerateSearchSchema[mappedFields](g)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"display_nm", "owner_ref", "note_text", "kind_code"}, rec.columns)
	assert.Equal(t, "m_items.display_nm", schema.Fields["displayName"].Expr)
	for name, want := range map[string]string{"owner_ref": "m_items.owner_ref", "kindCode": "m_items.kind_code", "kind_code": "m_items.kind_code"} {
		f, ok, err := schema.PolicyField(name)
		require.NoError(t, err, name)
		require.True(t, ok, name)
		assert.Equal(t, want, f.Expr, name)
	}
}
