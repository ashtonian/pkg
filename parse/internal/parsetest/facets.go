//go:build unit || integration

package parsetest

import (
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// FacetSchema is a small schema with a mix of terms and range facets,
// covering the cases BuildFacetQuery has to handle: text/uuid/int casting,
// per-field LIMIT, and range pre-bucketing.
func FacetSchema() *filter.ResourceSchema {
	maxLow := 50.0
	maxMid := 100.0
	schema := &filter.ResourceSchema{
		Name:       "items",
		PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "items", Column: "id", DataType: core.FieldTypeString},
		},
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", Table: "items", Column: "status", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
				Facetable:        true,
				FacetSchema:      &filter.FacetSchema{Type: filter.FacetTerms, Limit: 25},
			},
			"owner_id": {
				ExternalName: "owner_id", Table: "items", Column: "owner_id", DataType: core.FieldTypeUUID,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
				Facetable:        true,
				// No FacetSchema → defaults to terms; default limit 50.
			},
			"price": {
				ExternalName: "price", Table: "items", Column: "price", DataType: core.FieldTypeFloat,
				AllowedOperators: []filter.FilterOperator{filter.OpGt, filter.OpLte},
				Facetable:        true,
				FacetSchema: &filter.FacetSchema{
					Type: filter.FacetTypeRange,
					Ranges: []filter.FacetRange{
						{Key: "0-50", Min: new(float64(0)), Max: &maxLow},
						{Key: "50-100", Min: &maxLow, Max: &maxMid},
						{Key: "100+", Min: &maxMid},
					},
				},
			},
			"name": {
				ExternalName: "name", Table: "items", Column: "name", DataType: core.FieldTypeString,
				// Not facetable.
			},
			"in_stock": {
				ExternalName: "in_stock", Table: "items", Column: "in_stock", DataType: core.FieldTypeBool,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
				Facetable:        true,
			},
		},
	}
	return schema
}
