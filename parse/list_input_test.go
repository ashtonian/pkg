//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type listEntryPoints interface {
	ParseList(url.Values) (*parse.ListRequest, error)
	ParseListInput(parse.ListInput) (*parse.ListRequest, error)
}

// listResources are the resources the list corpus names: widgets (tenant,
// soft delete, search, a relation; page size 2, at most 3), bare (no soft
// delete), unsearchable and accepting (reads `view` and `_` itself).
func listResources(t *testing.T) map[string]listEntryPoints {
	t.Helper()
	type unsearchable struct {
		ID   string `db:"id"   parse:"filter,sort"`
		Name string `db:"name" parse:"filter"`
	}
	bare, err := parse.NewResource[widget]("widgets")
	require.NoError(t, err)
	plain, err := parse.NewResource[unsearchable]("plains")
	require.NoError(t, err)
	accepting, err := parse.NewResource[widget]("widgets", parse.AcceptParams("_", "view"))
	require.NoError(t, err)
	return map[string]listEntryPoints{"widgets": widgets(t), "bare": bare, "unsearchable": plain, "accepting": accepting}
}

// listCorpus is the list corpus: each request as a query string and as the
// same ListInput. Both entry points must return equal requests, or equal
// errors with the code named.
var listCorpus = []struct {
	name     string
	resource string
	query    string
	input    parse.ListInput
	code     string
}{
	{name: "no parameters", resource: "widgets"},
	{
		name: "every list parameter", resource: "widgets",
		query: `filter={"rank":1}&name=beta&sort=-rank&cursor=abc&page_size=9&include_total=true` +
			`&include_deleted=1&search=b&facets=name,name&include=tags&fields=id,name,id`,
		input: parse.ListInput{
			Filter: `{"rank":1}`, FieldFilters: map[string][]string{"name": {"beta"}}, Sort: "-rank", Cursor: "abc",
			PageSize: 9, IncludeTotal: true, IncludeDeleted: true, Search: "b",
			Facets: []string{"name", "name"}, Include: []string{"tags"}, Fields: []string{"id", "name", "id"},
		},
	},
	{
		name: "operator suffixes and repeated equality", resource: "widgets",
		query: "rank_gte=2&name=alpha&name=beta",
		input: parse.ListInput{FieldFilters: map[string][]string{"rank_gte": {"2"}, "name": {"alpha", "beta"}}},
	},
	{
		name: "pattern operator", resource: "widgets",
		query: "name_ilike=%25a%25", input: parse.ListInput{FieldFilters: map[string][]string{"name_ilike": {"%a%"}}},
	},
	{
		name: "relation membership", resource: "widgets",
		query: "tags_in=red,blue", input: parse.ListInput{FieldFilters: map[string][]string{"tags_in": {"red,blue"}}},
	},
	{
		name: "cost limit", resource: "widgets", code: filter.ECodeQueryTooExpensive.String(),
		query: "rank_gte=2&name=alpha&name=beta&tags_in=red,blue&name_ilike=%25a%25",
		input: parse.ListInput{FieldFilters: map[string][]string{
			"rank_gte": {"2"}, "name": {"alpha", "beta"}, "tags_in": {"red,blue"}, "name_ilike": {"%a%"},
		}},
	},
	{
		name: "names trimmed, empties and repeats dropped", resource: "widgets",
		query: "facets=%20name%20,,&fields=,%20id&include=tags,%20tags&search=%20b%20",
		input: parse.ListInput{
			Facets: []string{" name ", "", ""}, Fields: []string{"", " id"},
			Include: []string{"tags", " tags"}, Search: " b ",
		},
	},
	{name: "page size within the maximum", resource: "widgets", query: "page_size=2", input: parse.ListInput{PageSize: 2}},
	{
		name: "accepted parameters are the endpoint's", resource: "accepting",
		query: "view=grid&_=123&name=beta",
		input: parse.ListInput{FieldFilters: map[string][]string{"name": {"beta"}}},
	},
	{
		name: "misspelt field filter", resource: "widgets", code: filter.ECodeUnknownField.String(),
		query: "nmae=beta", input: parse.ListInput{FieldFilters: map[string][]string{"nmae": {"beta"}}},
	},
	{
		name: "unknown facet", resource: "widgets", code: filter.ECodeUnknownField.String(),
		query: "facets=colour", input: parse.ListInput{Facets: []string{"colour"}},
	},
	{
		name: "field that is not facetable", resource: "widgets", code: filter.ECodeUnknownField.String(),
		query: "facets=rank", input: parse.ListInput{Facets: []string{"rank"}},
	},
	{
		name: "unknown include", resource: "widgets", code: filter.ECodeUnknownField.String(),
		query: "include=colour", input: parse.ListInput{Include: []string{"colour"}},
	},
	{
		name: "include that is not a relation", resource: "widgets", code: filter.ECodeInvalidRelation.String(),
		query: "include=name", input: parse.ListInput{Include: []string{"name"}},
	},
	{
		name: "unknown sort", resource: "widgets", code: filter.ECodeSortFieldNotAllowed.String(),
		query: "sort=colour", input: parse.ListInput{Sort: "colour"},
	},
	{
		name: "deleted rows of a table without", resource: "bare", code: filter.ECodeInvalidListParam.String(),
		query: "include_deleted=true", input: parse.ListInput{IncludeDeleted: true},
	},
	{
		name: "search without search fields", resource: "unsearchable", code: filter.ECodeInvalidListParam.String(),
		query: "search=x", input: parse.ListInput{Search: "x"},
	},
	{
		name: "malformed filter document", resource: "widgets", code: filter.ECodeInvalidJSON.String(),
		query: "filter=%7B", input: parse.ListInput{Filter: "{"},
	},
	{
		name: "malformed null operand", resource: "widgets", code: filter.ECodeValueTypeMismatch.String(),
		query: "name_null=maybe", input: parse.ListInput{FieldFilters: map[string][]string{"name_null": {"maybe"}}},
	},
	{
		name: "first error in parameter order", resource: "bare", code: filter.ECodeInvalidListParam.String(),
		query: "include_deleted=true&nmae=beta&sort=colour",
		input: parse.ListInput{IncludeDeleted: true, FieldFilters: map[string][]string{"nmae": {"beta"}}, Sort: "colour"},
	},
}

func TestListCorpus_BothEntryPointsAgree(t *testing.T) {
	resources := listResources(t)
	for _, tc := range listCorpus {
		t.Run(tc.name, func(t *testing.T) {
			r := resources[tc.resource]
			q, err := url.ParseQuery(tc.query)
			require.NoError(t, err)
			fromQuery, queryErr := r.ParseList(q)
			fromInput, inputErr := r.ParseListInput(tc.input)

			assert.Equal(t, queryErr, inputErr, "equal errors")
			assert.Equal(t, fromQuery, fromInput, "equal requests")
			if tc.code == "" {
				require.NoError(t, queryErr)
				return
			}
			var pe *core.ParseError
			require.ErrorAs(t, queryErr, &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}
}

func TestListCorpus_ReadsEveryParameter(t *testing.T) {
	req, err := widgets(t).ParseListInput(listCorpus[1].input)
	require.NoError(t, err)
	assert.Equal(t, 2, req.Filter.ConditionCount, "the JSON and field filters are one filter")
	assert.Equal(t, "rank", req.Sort.Fields[0].Name)
	assert.Equal(t, "abc", req.Cursor)
	assert.Equal(t, 3, req.PageSize, "clamped to the maximum")
	assert.True(t, req.IncludeTotal)
	assert.True(t, req.IncludeDeleted)
	assert.Equal(t, "b", req.Search)
	assert.Equal(t, []string{"name"}, req.Facets)
	assert.Equal(t, []string{"tags"}, req.Include)
	assert.Equal(t, []string{"id", "name"}, req.Fields)
}

// A query string spells some requests a ListInput cannot, and the reverse;
// each entry point refuses its own malformed spelling.
func TestParseList_RefusesMalformedText(t *testing.T) {
	r := widgets(t)
	for _, query := range []string{"page_size=ten", "page_size=0", "page_size=-1", "include_total=yes", "include_deleted=2"} {
		t.Run(query, func(t *testing.T) {
			q, err := url.ParseQuery(query)
			require.NoError(t, err)
			_, err = r.ParseList(q)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, filter.ECodeInvalidListParam.String(), pe.Code)
		})
	}
}

func TestParseListInput_RefusesANegativePageSize(t *testing.T) {
	_, err := widgets(t).ParseListInput(parse.ListInput{PageSize: -1})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidListParam.String(), pe.Code)
	assert.Equal(t, filter.ParamPageSize, pe.Details["parameter"])
}

// FieldFilters holds field filters only: a list parameter's name there names
// a field, or nothing.
func TestParseListInput_FieldFiltersAreNeverListParameters(t *testing.T) {
	_, err := widgets(t).ParseListInput(parse.ListInput{FieldFilters: map[string][]string{filter.ParamSort: {"-rank"}}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code, "widgets has no field named sort")

	type view struct {
		ID   string `json:"id"   db:"id"   parse:"filter,sort"`
		Sort string `json:"sort" db:"sort" parse:"filter"`
	}
	views, err := parse.NewResource[view]("views")
	require.NoError(t, err)
	req, err := views.ParseListInput(parse.ListInput{FieldFilters: map[string][]string{"sort": {"name"}}})
	require.NoError(t, err)
	require.NotNil(t, req.Filter)
	assert.Equal(t, 1, req.Filter.ConditionCount, "a field named sort is filtered on")
	assert.Nil(t, req.Sort)
}

func TestParseListInput_LeavesItsInputAlone(t *testing.T) {
	in := parse.ListInput{
		FieldFilters: map[string][]string{"name": {"beta"}},
		Facets:       []string{" name ", "name"}, Include: []string{"tags", "tags"}, Fields: []string{"id", "id"},
	}
	want := parse.ListInput{
		FieldFilters: map[string][]string{"name": {"beta"}},
		Facets:       []string{" name ", "name"}, Include: []string{"tags", "tags"}, Fields: []string{"id", "id"},
	}
	_, err := widgets(t).ParseListInput(in)
	require.NoError(t, err)
	assert.Equal(t, want, in)
}
