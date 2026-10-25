//go:build unit

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestGenerateFilterSchema_CapabilityTags(t *testing.T) {
	type Row struct {
		ID     string `json:"id" parse:"filter"`
		Status string `json:"status" parse:"filter,facet"`
		Name   string `json:"name" parse:"filter,search,include"`
	}

	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[Row](gen)
	if err != nil {
		t.Fatalf("GenerateFilterSchema: %v", err)
	}

	for field, want := range map[string]bool{
		"status": true,
		"id":     false,
	} {
		fs, ok := schema.Fields[field]
		if !ok {
			t.Fatalf("field %q missing from schema", field)
		}
		if fs.Facetable != want {
			t.Errorf("field %q Facetable = %v, want %v", field, fs.Facetable, want)
		}
	}

	fs := schema.Fields["name"]
	if fs == nil {
		t.Fatal("field name missing")
	}
	if !fs.Searchable || !fs.Includable {
		t.Errorf("field name searchable/includable = %v/%v, want true/true", fs.Searchable, fs.Includable)
	}
}
