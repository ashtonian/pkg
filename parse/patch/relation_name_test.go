//go:build unit

package patch_test

import (
	"encoding/json"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
	"github.com/stretchr/testify/require"
)

func TestHiddenRelationDeclaredPatchName(t *testing.T) {
	type row struct {
		TagIDs []string `json:"-" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
	}
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[row](g)
	require.NoError(t, err)
	require.Contains(t, schema.AllowedPaths, "/tag_ids")
	require.NotContains(t, schema.AllowedPaths, "/tag_i_ds")
	require.NotNil(t, schema.AllowedPaths["/tag_ids"].Relation)
	parser := patch.NewParser(schema, nil)
	parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/tag_ids","value":["a"]}]`))
	require.NoError(t, err)
	sql, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	require.NoError(t, err)
	require.Empty(t, sql.SetClause)
	require.Len(t, sql.RelationOps, 1)
	require.Equal(t, "tag_ids", sql.RelationOps[0].Field)
	_, err = parser.Parse([]byte(`[{"op":"replace","path":"/tag_i_ds","value":["a"]}]`))
	require.Error(t, err)
	encoded, err := json.Marshal(row{TagIDs: []string{"a"}})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(encoded))
	server, err := patch.GenerateServerPatchSchema[row](g)
	require.NoError(t, err)
	require.Contains(t, server.AllowedPaths, "/tag_ids")
}

func TestRelationPatchNameUsesVisibleTagFirst(t *testing.T) {
	type row struct {
		TagIDs []string `json:"visible_tags" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
		Name   string   `json:"name" parse:"patch" relation_name:"ignored"`
	}
	schema, err := patch.GeneratePatchSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	require.Contains(t, schema.AllowedPaths, "/visible_tags")
	require.Contains(t, schema.AllowedPaths, "/name")
	require.NotContains(t, schema.AllowedPaths, "/tag_ids")
	require.NotContains(t, schema.AllowedPaths, "/ignored")
}

func TestDuplicatePatchNamesFailConstruction(t *testing.T) {
	g := core.NewSchemaGenerator()
	type relationFirst struct {
		TagIDs []string `json:"-" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
		Scalar string   `json:"tag_ids" parse:"patch"`
	}
	type scalarFirst struct {
		Scalar string   `json:"tag_ids" parse:"patch"`
		TagIDs []string `json:"-" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
	}
	type relations struct {
		TagIDs   []string `json:"-" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_tags,row_id,tag_id"`
		OtherIDs []string `json:"-" db:"-" parse:"patch" relation_name:"tag_ids" relation:"row_others,row_id,other_id"`
	}
	type scalars struct {
		First  string `api:"name" parse:"patch"`
		Second string `api:"name" parse:"patch"`
	}
	for name, generate := range map[string]func() (*patch.PatchSchema, error){
		"relation_first": func() (*patch.PatchSchema, error) { return patch.GeneratePatchSchema[relationFirst](g) },
		"scalar_first":   func() (*patch.PatchSchema, error) { return patch.GeneratePatchSchema[scalarFirst](g) },
		"relations":      func() (*patch.PatchSchema, error) { return patch.GeneratePatchSchema[relations](g) },
		"scalars": func() (*patch.PatchSchema, error) {
			return patch.GeneratePatchSchema[scalars](core.NewSchemaGenerator(core.WithFieldNameTag("api")))
		},
	} {
		want := "duplicate patch path"
		if name == "scalars" {
			want = `both named "name"` // two fields with one name fail the field walk
		}
		t.Run(name, func(t *testing.T) { _, err := generate(); require.ErrorContains(t, err, want) })
	}
	type serverCollision struct {
		Scalar string `json:"name" parse:"patch"`
		Secret string `json:"-" db:"name" parse:"server"`
	}
	_, err := patch.GenerateServerPatchSchema[serverCollision](g)
	require.ErrorContains(t, err, "duplicate patch path")
}
