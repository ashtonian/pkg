//go:build unit

package parse_test

import (
	"net/url"
	"testing"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type stdlibKeyed struct {
	ID   uuid.UUID `json:"id"   db:"id"   parse:"filter,sort"`
	Name string    `json:"name" db:"name" parse:"filter,sort"`
}

type googleKeyed struct {
	ID   googleuuid.UUID `json:"id"   db:"id"   parse:"filter,sort"`
	Name string          `json:"name" db:"name" parse:"filter,sort"`
}

// A resource keyed by the standard library's uuid.UUID lists like one keyed
// by github.com/google/uuid: the same filter SQL, the same cursors.
func TestUUIDKeyedResources_ListAlike(t *testing.T) {
	const id = "0198a3c2-7a1e-7c00-8000-000000000001"
	q := url.Values{"id": {"0198A3C2-7A1E-7C00-8000-000000000001"}, "sort": {"name"}, "page_size": {"1"}}

	std, err := parse.NewResource[stdlibKeyed]("rows")
	require.NoError(t, err)
	stdReq, err := std.ParseList(q)
	require.NoError(t, err)
	stdPlan, err := std.BuildList(filter.PostgresDialect{}, stdReq, parse.Scope{})
	require.NoError(t, err)
	stdPage, err := std.Page([]stdlibKeyed{{uuid.MustParse(id), "a"}, {uuid.MustParse(id), "b"}}, stdPlan)
	require.NoError(t, err)

	g, err := parse.NewResource[googleKeyed]("rows")
	require.NoError(t, err)
	gReq, err := g.ParseList(q)
	require.NoError(t, err)
	gPlan, err := g.BuildList(filter.PostgresDialect{}, gReq, parse.Scope{})
	require.NoError(t, err)
	gPage, err := g.Page([]googleKeyed{{googleuuid.MustParse(id), "a"}, {googleuuid.MustParse(id), "b"}}, gPlan)
	require.NoError(t, err)

	assert.Equal(t, gPlan.Page.SQL, stdPlan.Page.SQL)
	assert.Equal(t, gPlan.Page.Args, stdPlan.Page.Args)
	assert.Contains(t, stdPlan.Page.Args, id, "the filter value binds as canonical text")
	assert.Equal(t, gPage.PageInfo.EndCursor, stdPage.PageInfo.EndCursor)
}
