//go:build unit || integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// serverFieldDocument has columns a request cannot filter by: a hidden
// owner, a visible field without the filter capability, and a filter field
// whose column differs from its name.
type serverFieldDocument struct {
	ID       int    `json:"id"        db:"id"         parse:"filter,sort"`
	OwnerID  int    `json:"-"         db:"owner_id"`
	Region   string `json:"region"    db:"region_code" enum:"eu,us"`
	Title    string `json:"title"     db:"title"      parse:"filter,sort,facet"`
	FolderID int    `json:"folderId"  db:"folder_id"  parse:"filter"`
}

func serverFieldDocuments(t *testing.T) *parse.Resource[serverFieldDocument] {
	t.Helper()
	r, err := parse.NewResource[serverFieldDocument]("documents")
	require.NoError(t, err)
	return r
}

func policyOn(constraints ...*filter.PolicyConstraint) parse.Scope {
	return parse.Scope{Policy: &filter.PolicyConstraints{Constraints: constraints}}
}
