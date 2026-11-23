//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// sharedColumn has two request fields over one column, one by convention
// and one by tag, which a schema generated outside parse.NewResource allows.
type sharedColumn struct {
	Code   string `json:"sharedCode" parse:"filter"`
	Number int    `json:"number"     db:"shared_code" parse:"filter"`
}

// A column two fields hold is refused as a policy name every time, and each
// field stays reachable by its own name.
func TestPolicyField_SharedColumnIsRefused(t *testing.T) {
	for range 32 {
		schema, err := filter.GenerateFilterSchema[sharedColumn](core.NewSchemaGenerator())
		require.NoError(t, err)

		_, ok, err := schema.PolicyField("shared_code")
		assert.True(t, ok)
		require.ErrorIs(t, err, filter.ErrAmbiguousPolicyField)
		assert.ErrorContains(t, err, "the column of field Code (column shared_code) and the column of field Number (column shared_code)")

		for _, name := range []string{"sharedCode", "number"} {
			f, ok, err := schema.PolicyField(name)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, name, f.ExternalName)
		}
	}
}

// A hand-built schema whose Fields and PolicyFields give one name two
// fields is refused rather than resolved to the request field.
func TestPolicyField_FieldsAndPolicyFieldsOverlap(t *testing.T) {
	request := &filter.FieldSchema{ExternalName: "owner", Table: "items", Column: "owner_name", DataType: core.FieldTypeString}
	hidden := &filter.FieldSchema{ExternalName: "owner", Table: "items", Column: "owner_id", DataType: core.FieldTypeInt}
	tests := []struct {
		name    string
		policy  *filter.FieldSchema
		want    *filter.FieldSchema
		wantErr error
	}{
		{"two fields", hidden, nil, filter.ErrAmbiguousPolicyField},
		{"one field both hold", request, request, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := &filter.ResourceSchema{
				Name:         "items",
				Fields:       map[string]*filter.FieldSchema{"owner": request},
				PolicyFields: map[string]*filter.FieldSchema{"owner": tt.policy},
			}
			f, ok, err := schema.PolicyField("owner")
			assert.True(t, ok)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Same(t, tt.want, f)
		})
	}
}
