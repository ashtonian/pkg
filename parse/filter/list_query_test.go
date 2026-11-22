//go:build unit || integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func mustListQuery(t testing.TB, fs *filter.ResourceSchema, ss *filter.SortSchema, d filter.Dialect) *filter.ListQuery {
	t.Helper()
	lq, err := filter.NewListQuery(fs, ss, d)
	if err != nil {
		t.Fatalf("NewListQuery: %v", err)
	}
	return lq
}

// withOwnerKeys gives every relation field the owner's string key as its
// correlation field, as parse.NewResource does.
func withOwnerKeys(schema *filter.ResourceSchema) {
	for _, f := range schema.Fields {
		if f.Relation != nil && f.RelatedBy == nil {
			key := f.Relation.OwnerKey()
			f.RelatedBy = &filter.FieldSchema{ExternalName: key, Table: schema.Name, Column: key, DataType: core.FieldTypeString}
		}
	}
}
