//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A list parameter given twice is E2067 naming it, from ParseList and
// ParseRequestFilter alike: q.Get would read the first and drop the rest.
// It is the query string's twin of a list body naming a member twice, which
// is E2086. A flat field parameter keeps its repeats, as a set.
func TestParseList_AListParameterIsGivenOnce(t *testing.T) {
	res := widgets(t)
	twice := map[string][]string{
		filter.ParamFilter:         {`{"name":"a"}`, `{"name":"b"}`},
		filter.ParamSort:           {"name", "-name"},
		filter.ParamCursor:         {"c1", "c2"},
		filter.ParamPageSize:       {"2", "3"},
		filter.ParamIncludeTotal:   {"true", "false"},
		filter.ParamIncludeDeleted: {"false", "true"},
		filter.ParamSearch:         {"a", "b"},
		filter.ParamFacets:         {"name", "name"},
		filter.ParamFields:         {"name", "rank"},
		filter.ParamInclude:        {"tags", "tags"},
	}
	for name, values := range twice {
		require.True(t, filter.IsListParam(name), name)
		t.Run(name, func(t *testing.T) {
			q := url.Values{name: values}
			_, err := res.ParseList(q)
			assertListParam(t, err, name)
			_, err = res.ParseRequestFilter(q)
			assertListParam(t, err, name)
		})
	}

	req, err := res.ParseList(url.Values{"name": {"a", "b"}})
	require.NoError(t, err)
	plan, err := res.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `"widgets"."name" = ANY(`, "a repeated field filter is a set")
}

func assertListParam(t *testing.T, err error, name string) {
	t.Helper()
	assertListParamNamed(t, err, name)
	assert.ErrorContains(t, err, "given 2 times")
}

// assertListParamNamed asserts err is E2067 naming parameter name.
func assertListParamNamed(t *testing.T, err error, name string) {
	t.Helper()
	require.ErrorIs(t, err, filter.ECodeInvalidListParam, name)
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok)
	assert.Equal(t, name, pe.Details[core.DetailParameter])
}
