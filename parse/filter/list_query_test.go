//go:build unit || integration

package filter_test

import (
	"testing"

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
