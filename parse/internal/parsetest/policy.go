//go:build unit || integration

package parsetest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// ServerFieldDocument has columns a request cannot filter by: a hidden
// owner, a visible field without the filter capability, and a filter field
// whose column differs from its name.
type ServerFieldDocument struct {
	ID       int    `json:"id"        db:"id"         parse:"filter,sort"`
	OwnerID  int    `json:"-"         db:"owner_id"`
	Region   string `json:"region"    db:"region_code" enum:"eu,us"`
	Title    string `json:"title"     db:"title"      parse:"filter,sort,facet"`
	FolderID int    `json:"folderId"  db:"folder_id"  parse:"filter"`
}

func ServerFieldDocuments(t *testing.T, p *parse.Provider) *parse.Resource[ServerFieldDocument] {
	t.Helper()
	r, err := parse.NewResource[ServerFieldDocument](p, "documents")
	require.NoError(t, err)
	return r
}

func PolicyOn(constraints ...*filter.PolicyConstraint) parse.Scope {
	return parse.Scope{Policy: &filter.PolicyConstraints{Constraints: constraints}}
}

// OwnerEUPolicy holds documents to owner 7's in the eu region, through a
// hidden column and a column without the filter capability.
func OwnerEUPolicy() parse.Scope {
	return PolicyOn(
		&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: 7},
		&filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "eu"},
	)
}
