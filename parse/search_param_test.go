//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type searchedPump struct {
	ID       string         `json:"id"       db:"id"       parse:"filter,sort"`
	Name     string         `json:"name"     db:"name"     parse:"filter,sort,search" filter_ops:"eq,like"`
	Category string         `json:"category" db:"category" parse:"filter,facet" facet:"terms:5"`
	Labels   map[string]any `json:"labels"   db:"labels"   parse:"filter"`
}

type searchedPart struct {
	ID     string `json:"id"      db:"id"      parse:"filter"`
	PumpID string `json:"pump_id" db:"pump_id" parse:"filter"`
}

// searchedPumps is the pumps resource on p advertising search as param,
// the default when param is empty.
func searchedPumps(t *testing.T, p *parse.Provider, param string, opts ...parse.ResourceOption) (*parse.Resource[searchedPump], error) {
	t.Helper()
	if param != "" {
		opts = append(opts, parse.ParserOptions(core.WithGeneratorOptions(core.WithSearchParam(param))))
	}
	return parse.NewResource[searchedPump](p, "pumps", append(opts, atOneInstant())...)
}

// The schema document states where a client puts the search, and the same
// resource reads it there: discovery, GET parsing and SQL agree for the
// default name and a custom one, a body reads the same request from its
// member, and "search" stays an alias of a custom name.
func TestSearchParam_DiscoveryParsingAndSQLAgree(t *testing.T) {
	for _, tt := range []struct {
		name, configured, param string
	}{
		{"default", "", filter.ParamSearch},
		{"custom", "q", "q"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := searchedPumps(t, onSQLite, tt.configured)
			require.NoError(t, err)
			reg := parse.NewSchemaRegistry()
			require.NoError(t, parse.RegisterResource(reg, res))
			doc := reg.SchemaFor("pumps")
			require.NotNil(t, doc)
			require.NotNil(t, doc.Search)
			assert.Equal(t, tt.param, doc.Search.Param)
			assert.Equal(t, filter.ParamSearch, doc.Search.Member, "a body's member does not follow the query parameter")

			get, err := res.ParseList(url.Values{doc.Search.Param: {"pump"}, filter.ParamSort: {"name"}})
			require.NoError(t, err)
			assert.Equal(t, "pump", get.Search())
			for _, on := range []struct {
				provider *parse.Provider
				arg      string
			}{{onSQLite, "%pump%"}, {onPostgres, "pump"}} {
				built, err := searchedPumps(t, on.provider, tt.configured)
				require.NoError(t, err)
				req, err := built.ParseList(url.Values{doc.Search.Param: {"pump"}, filter.ParamSort: {"name"}})
				require.NoError(t, err)
				plan, err := built.BuildList(req, parse.Scope{})
				require.NoError(t, err)
				assert.Contains(t, plan.Page.Args, on.arg, "%s searches the advertised parameter's value", on.provider.Name())
			}

			post, err := res.ParseListBody(strings.NewReader(`{"` + doc.Search.Member + `":"pump","sort":"name"}`))
			require.NoError(t, err)
			assert.Equal(t, get, post, "the body member is the same request")

			alias, err := res.ParseList(url.Values{filter.ParamSearch: {"pump"}, filter.ParamSort: {"name"}})
			require.NoError(t, err)
			assert.Equal(t, get, alias, "search is read whatever the schema advertises")

			facets, err := res.ParseFacets(url.Values{filter.ParamFacets: {"category"}, doc.Search.Param: {"pump"}})
			require.NoError(t, err)
			assert.Equal(t, "pump", facets.Search())

			_, err = res.ParseRequestFilter(url.Values{doc.Search.Param: {"pump"}, "category": {"tools"}})
			require.NoError(t, err, "the search parameter is no field filter")
		})
	}
}

// One request gives the search once, under either name.
func TestSearchParam_GivenOnce(t *testing.T) {
	res, err := searchedPumps(t, onSQLite, "q")
	require.NoError(t, err)
	for _, tt := range []struct {
		name  string
		q     url.Values
		param string
	}{
		{"under both names", url.Values{"q": {"a"}, filter.ParamSearch: {"b"}}, "q"},
		{"both names, one empty", url.Values{"q": {"a"}, filter.ParamSearch: {""}}, "q"},
		{"the advertised name twice", url.Values{"q": {"a", "b"}}, "q"},
		{"search twice", url.Values{filter.ParamSearch: {"a", "b"}}, filter.ParamSearch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := res.ParseList(tt.q)
			pe := requireParseError(t, err, filter.ECodeInvalidListParam.String())
			assert.Equal(t, tt.param, pe.Details[core.DetailParameter])
		})
	}

	_, err = res.ParseRequestFilter(url.Values{"q": {"a", "b"}})
	pe := requireParseError(t, err, filter.ECodeInvalidListParam.String())
	assert.Equal(t, "q", pe.Details[core.DetailParameter], "the search parameter is a list parameter")

	_, err = res.ParseListPost(url.Values{"q": {"a"}}, "application/json", strings.NewReader(`{}`))
	pe = requireParseError(t, err, filter.ECodeInvalidListParam.String())
	assert.Equal(t, "q", pe.Details[core.DetailParameter])
	assert.Contains(t, pe.Message, `"search"`, "the refusal names the body member")

	_, err = res.ParseListBody(strings.NewReader(`{"q":"pump"}`))
	pe = requireParseError(t, err, filter.ECodeInvalidListParam.String())
	assert.Equal(t, "q", pe.Details[core.DetailParameter], "a body has no member q")
}

// A search parameter a list request already gives another meaning is
// refused where the resource is built, naming both.
func TestSearchParam_CollisionsAreRefusedAtConstruction(t *testing.T) {
	for _, tt := range []struct {
		name, param string
		opts        []parse.ResourceOption
		want        string
	}{
		{"a list parameter", "sort", nil, `search parameter "sort" is the list parameter sort`},
		{"a field", "name", nil, `search parameter "name" is a flat filter on field "name"`},
		{"a field's operator", "name_like", nil, `search parameter "name_like" is a flat filter on field "name"`},
		{"the key", "id", nil, `search parameter "id" is a flat filter on field "id"`},
		{"an accepted parameter", "q", []parse.ResourceOption{parse.AcceptParams("q")}, `search parameter "q" is a parameter the endpoint reads itself (AcceptParams "q")`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := searchedPumps(t, onSQLite, tt.param, tt.opts...)
			require.ErrorContains(t, err, tt.want)
		})
	}

	res, err := searchedPumps(t, onSQLite, "q")
	require.NoError(t, err)
	parts, err := parse.NewResource[searchedPart](onSQLite, "pump_parts")
	require.NoError(t, err)
	err = res.Relate("q", parse.HasMany(parts, "pump_id"))
	require.ErrorContains(t, err, `parse: pumps: relation "q": search parameter "q" is a flat filter on field "q"`)
	require.NoError(t, res.Relate("parts", parse.HasMany(parts, "pump_id")), "another name relates")

	dotted, err := searchedPumps(t, onSQLite, "labels.env")
	require.NoError(t, err)
	_, err = dotted.WithPaths(filter.PathDecl{Path: "labels.env", Type: core.FieldTypeString})
	require.ErrorContains(t, err, `search parameter "labels.env" is a flat filter on field "labels.env"`)
}

// A parameter the endpoint reads itself that a list request already gives
// another meaning is refused where it would be: ParseList would skip a flat
// filter the schema document advertises and list every row, or read a list
// parameter the endpoint reads too.
func TestAcceptParams_CollisionsAreRefusedAtConstruction(t *testing.T) {
	for _, tt := range []struct {
		name, param, want string
	}{
		{"a list parameter", "sort", `accepted parameter "sort" (AcceptParams) is the list parameter sort`},
		{"a field", "category", `accepted parameter "category" (AcceptParams) is a flat filter on field "category"`},
		{"a field's operator", "name_like", `accepted parameter "name_like" (AcceptParams) is a flat filter on field "name"`},
		{"the key", "id", `accepted parameter "id" (AcceptParams) is a flat filter on field "id"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := searchedPumps(t, onSQLite, "", parse.AcceptParams(tt.param))
			require.ErrorContains(t, err, tt.want)
		})
	}

	res, err := searchedPumps(t, onSQLite, "", parse.AcceptParams("view"))
	require.NoError(t, err)
	parts, err := parse.NewResource[searchedPart](onSQLite, "pump_parts")
	require.NoError(t, err)
	err = res.Relate("view", parse.HasMany(parts, "pump_id"))
	require.ErrorContains(t, err, `parse: pumps: relation "view": accepted parameter "view" (AcceptParams) is a flat filter on field "view"`)
	require.NoError(t, res.Relate("parts", parse.HasMany(parts, "pump_id")), "another name relates")

	suffixed, err := searchedPumps(t, onSQLite, "", parse.AcceptParams("kits_in"))
	require.NoError(t, err)
	err = suffixed.Relate("kits", parse.HasMany(parts, "pump_id"))
	require.ErrorContains(t, err, `parse: pumps: relation "kits": accepted parameter "kits_in" (AcceptParams) is a flat filter on field "kits"`)

	dotted, err := searchedPumps(t, onSQLite, "", parse.AcceptParams("labels.env"))
	require.NoError(t, err)
	_, err = dotted.WithPaths(filter.PathDecl{Path: "labels.env", Type: core.FieldTypeString})
	require.ErrorContains(t, err, `accepted parameter "labels.env" (AcceptParams) is a flat filter on field "labels.env"`)

	req, err := res.ParseList(url.Values{"view": {"grid"}, "category": {"pump"}})
	require.NoError(t, err)
	assert.NotNil(t, req.Filter(), "a field beside an accepted parameter still filters")
}

// A name one call accepts is held to the rule AcceptParams is: a list
// parameter, or a flat filter on a field, is refused rather than skipped,
// which would list every row the filter narrows.
func TestQueryParamAccept_CollisionsAreRefusedPerCall(t *testing.T) {
	res, err := searchedPumps(t, onSQLite, "")
	require.NoError(t, err)
	q := url.Values{"category": {"pump"}, "name_like": {"P%"}, "id": {"p1"}}
	for _, tt := range []struct {
		name, param, want string
	}{
		{"a list parameter", "sort", `QueryParamAccept: "sort" is a list parameter`},
		{"a field", "category", `QueryParamAccept: "category" is a flat filter on field "category"`},
		{"a field's operator", "name_like", `QueryParamAccept: "name_like" is a flat filter on field "name"`},
		{"the key", "id", `QueryParamAccept: "id" is a flat filter on field "id"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := res.ParseRequestFilter(q, filter.QueryParamAccept(tt.param))
			require.ErrorContains(t, err, tt.want)
		})
	}
	f, err := res.ParseRequestFilter(url.Values{"category": {"pump"}, "view": {"grid"}}, filter.QueryParamAccept("view"))
	require.NoError(t, err)
	assert.NotNil(t, f.AST().Root, "a field beside an accepted parameter still filters")
}

func requireParseError(t *testing.T, err error, code string) *core.ParseError {
	t.Helper()
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "want a parse error, got %v", err)
	require.Equal(t, code, pe.Code, pe.Message)
	return pe
}
