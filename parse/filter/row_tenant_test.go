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

// rowTenantSchemas are widgets, holding their tenant as ownerType, with
// notes (a has-many whose tenant column is relatedType) and keyed tags
// (a pair table, which declares no types).
func rowTenantSchemas(ownerType, relatedType core.FieldType) (*filter.ResourceSchema, *filter.FieldSchema) {
	notes := &filter.ResourceSchema{
		Name: "notes",
		Fields: map[string]*filter.FieldSchema{
			"body": {ExternalName: "body", SQLExpr: "notes.body", Column: "body", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}},
		},
		SystemFields: map[string]*filter.FieldSchema{
			"id":        {ExternalName: "id", SQLExpr: "notes.id", Column: "id", DataType: core.FieldTypeString},
			"tenant_id": {ExternalName: "tenant_id", SQLExpr: "notes.tenant_id", Column: "tenant_id", DataType: relatedType},
		},
	}
	key := &filter.FieldSchema{ExternalName: "id", SQLExpr: "widgets.id", Column: "id", DataType: core.FieldTypeString}
	rel := &filter.FieldSchema{
		ExternalName: "notes", DataType: core.FieldTypeString, Includable: true,
		Relation: &core.RelationSchema{Kind: core.HasMany, JoinTable: "notes", SourceColumn: "widget_id", TargetColumn: "id", PrimaryKey: "id", TenantColumn: "tenant_id"},
		Related:  notes, RelatedBy: key,
	}
	widgets := &filter.ResourceSchema{
		Name:       "widgets",
		PrimaryKey: "id",
		Fields: map[string]*filter.FieldSchema{
			"notes": rel,
			"tags": {
				ExternalName: "tags", DataType: core.FieldTypeString,
				Relation: &core.RelationSchema{JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id", PrimaryKey: "id", TenantColumn: "tenant_id"},
			},
		},
		SystemFields: map[string]*filter.FieldSchema{
			"id":        key,
			"tenant_id": {ExternalName: "tenant_id", SQLExpr: "widgets.tenant_id", Column: "tenant_id", DataType: ownerType},
		},
	}
	return widgets, rel
}

func someNote(widgets *filter.ResourceSchema) *filter.FilterAST {
	notes := widgets.Fields["notes"]
	return &filter.FilterAST{Root: &filter.FilterNode{Op: filter.OpAnd, Children: []*filter.FilterNode{
		{Field: notes, Op: filter.OpSome, Children: []*filter.FilterNode{{Field: notes.Related.Fields["body"], Op: filter.OpEq, Value: "urgent"}}},
		{Field: widgets.Fields["tags"], Op: filter.OpEq, Value: "red"},
	}}}
}

func TestRowTenant_ComparesEachRelationWithTheRow(t *testing.T) {
	tests := []struct {
		name                 string
		owner, related       core.FieldType
		wantNotes, wantPairs string
	}{
		{"one type", core.FieldTypeString, core.FieldTypeString,
			"notes.tenant_id = widgets.tenant_id", "rel__.tenant_id = widgets.tenant_id"},
		{"a uuid owner and a string related column compare by the uuid's text", core.FieldTypeUUID, core.FieldTypeString,
			"notes.tenant_id = CAST(widgets.tenant_id AS TEXT)", "rel__.tenant_id = widgets.tenant_id"},
		{"a string owner and a uuid related column compare by the uuid's text", core.FieldTypeString, core.FieldTypeUUID,
			"CAST(notes.tenant_id AS TEXT) = widgets.tenant_id", "rel__.tenant_id = widgets.tenant_id"},
		{"integers of any width compare as they are", core.FieldTypeInt, core.FieldTypeInt,
			"notes.tenant_id = widgets.tenant_id", "rel__.tenant_id = widgets.tenant_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			widgets, _ := rowTenantSchemas(tt.owner, tt.related)
			res, err := filter.NewSQLBuilder(widgets, filter.PostgresDialect{}, filter.WithRowTenant("tenant_id")).Build(someNote(widgets))
			require.NoError(t, err)
			assert.Contains(t, res.Where, tt.wantNotes)
			assert.Contains(t, res.Where, tt.wantPairs, "a pair table is taken to hold the owner's type")
			assert.Equal(t, []any{"urgent", "red"}, res.Args, "no tenant is bound")
		})
	}
}

func TestRowTenant_RefusesAmbiguousScopes(t *testing.T) {
	widgets, rel := rowTenantSchemas(core.FieldTypeString, core.FieldTypeString)
	for name, opts := range map[string][]filter.SQLBuilderOption{
		"with a bound tenant":       {filter.WithRowTenant("tenant_id"), filter.WithTenant("A")},
		"with unscoped relations":   {filter.WithUnscopedRelations(), filter.WithRowTenant("tenant_id")},
		"a column that is no ident": {filter.WithRowTenant("tenant_id; --")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := filter.NewSQLBuilder(widgets, filter.PostgresDialect{}, opts...).Build(someNote(widgets))
			assert.Error(t, err)
		})
	}

	_, err := mustListQuery(t, widgets, nil, filter.PostgresDialect{}).Build(filter.ListRowTenant("tenant_id"), filter.ListTenant("A"))
	assert.Error(t, err, "a list holds relations one way")
	_, err = filter.BuildFacetQuery(widgets, filter.PostgresDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "notes"}},
		filter.WithFacetRowTenant("tenant_id"), filter.WithFacetTenant("A"))
	assert.Error(t, err, "facets hold relations one way")
	_, err = filter.BuildIncludeQuery(rel, filter.PostgresDialect{}, []any{"w1"}, filter.WithIncludeRowTenant(nil, "tenant_id"))
	assert.ErrorContains(t, err, "owner's schema", "an include joins the owner's table")
	_, err = filter.BuildIncludeQuery(rel, filter.PostgresDialect{}, []any{"w1"}, filter.WithIncludeRowTenant(widgets, "tenant_id"), filter.WithIncludeTenant("A"))
	assert.Error(t, err, "an include holds relations one way")
}

func TestRowTenant_Include(t *testing.T) {
	widgets, rel := rowTenantSchemas(core.FieldTypeUUID, core.FieldTypeString)
	q, err := filter.BuildIncludeQuery(rel, filter.PostgresDialect{}, []any{"w1", "w2"}, filter.WithIncludeRowTenant(widgets, "tenant_id"))
	require.NoError(t, err)
	assert.Contains(t, q.SQL, "JOIN widgets owner__ ON owner__.id = j.widget_id")
	assert.Contains(t, q.SQL, "j.tenant_id = CAST(owner__.tenant_id AS TEXT)")
	assert.Equal(t, []any{"w1", "w2"}, q.Args, "no tenant is bound")

	_, err = filter.ResolveInclude(context.Background(), rel, []any{"w1"}, nil, filter.WithIncludeRowTenant(widgets, "tenant_id"))
	assert.ErrorContains(t, err, "WithIncludeTenant", "a lookup resolves one tenant's owners at a time")
}
