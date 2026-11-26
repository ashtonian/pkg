//go:build unit || integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// queryParams is filter.NewQueryParamParser over schema with options it
// accepts.
func queryParams(t testing.TB, schema *filter.ResourceSchema, opts ...filter.QueryParamOption) *filter.QueryParamParser {
	t.Helper()
	qp, err := filter.NewQueryParamParser(schema, opts...)
	require.NoError(t, err)
	return qp
}

// newSchema builds a test schema, which must be valid, holding at most
// maxOperations operations (0: any number).
func newSchema(resource string, maxOperations int, paths map[string]*patch.PathSchema) *patch.PatchSchema {
	s, err := patch.NewPatchSchema(resource, paths)
	if err != nil {
		panic(err)
	}
	s.MaxOperations = maxOperations
	return s
}

// pathOf is the path schema s declares under key, or nil.
func pathOf(s *patch.PatchSchema, key string) *patch.PathSchema {
	ps, _ := s.Path(key)
	return ps
}
