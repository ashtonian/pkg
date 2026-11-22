//go:build unit || integration

package patch_test

import "github.com/ashtonian/pkg/parse/patch"

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
