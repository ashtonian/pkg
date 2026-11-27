//go:build unit

package core_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// TestField_RelationName names a relation field as every schema addresses
// it: its JSON name, else its relation_name tag, else its Go name in
// snake_case; a field that is no relation has none.
func TestField_RelationName(t *testing.T) {
	type row struct {
		TagIDs      []string `json:"-" relation:"row_tags,row_id,tag_id" relation_name:"tag_ids"`
		OwnerGroups []string `json:"-" relation:"row_groups,row_id,group_id"`
		Visible     []string `json:"visible" relation:"row_labels,row_id,label_id" relation_name:"ignored"`
		Name        string   `json:"-" relation_name:"not_a_relation"`
	}
	fields, err := core.StructFieldsOf(reflect.TypeFor[row](), "json", "db")
	require.NoError(t, err)
	names := map[string]string{}
	for f := range fields.Fields() {
		names[f.GoName()] = f.RelationName()
	}
	assert.Equal(t, map[string]string{"TagIDs": "tag_ids", "OwnerGroups": "owner_groups", "Visible": "visible", "Name": ""}, names)
}
