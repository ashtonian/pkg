//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// A facets request reads facets, the filters, search and include_deleted,
// and refuses each list parameter that shapes a page instead of ignoring it.
func TestParseFacets_RefusesWhatShapesAPage(t *testing.T) {
	res := widgets(t)
	for _, name := range []string{filter.ParamCursor, filter.ParamSort, filter.ParamPageSize, filter.ParamIncludeTotal, filter.ParamInclude, filter.ParamFields} {
		_, err := res.ParseFacets(url.Values{filter.ParamFacets: {"name"}, name: {"x"}})
		assertListParamNamed(t, err, name)
	}

	_, err := res.ParseFacets(url.Values{filter.ParamFacets: {"name"}, filter.ParamFilter: {`{"name":"a"}`, `{"name":"b"}`}})
	assertListParamNamed(t, err, filter.ParamFilter)
	_, err = res.ParseFacets(url.Values{filter.ParamFacets: {"name"}, "colour": {"red"}})
	assert.ErrorIs(t, err, filter.ECodeUnknownField, "a misspelt filter cannot widen the counts")
}

// The counts alone are the counts a list page asking for them carries:
// the same statement under the same filter, search and scope.
func TestBuildFacets_TheStatementAListPageCarries(t *testing.T) {
	res := widgets(t)
	scope := parse.Scope{Tenant: "t1", IncludeDeleted: true}
	q := url.Values{
		filter.ParamFacets: {"name"}, filter.ParamFilter: {`{"rank":{"$gt":0}}`}, "name": {"a", "b"},
		filter.ParamSearch: {"w"}, filter.ParamIncludeDeleted: {"true"},
	}
	req, err := res.ParseFacets(q)
	require.NoError(t, err)
	got, err := res.BuildFacets(req, scope)
	require.NoError(t, err)

	q.Set(filter.ParamSort, "-rank")
	q.Set(filter.ParamPageSize, "2")
	page, err := res.ParseList(q)
	require.NoError(t, err)
	plan, err := res.BuildList(page, scope)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, plan.Facets, got)

	none, err := res.ParseFacets(url.Values{"name": {"a"}})
	require.NoError(t, err)
	got, err = res.BuildFacets(none, scope)
	require.NoError(t, err)
	assert.Nil(t, got, "no facets, no statement")
}
