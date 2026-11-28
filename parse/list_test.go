//go:build unit

package parse_test

import (
	"errors"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

func TestParseList_ReadsEveryListParameter(t *testing.T) {
	r := widgets(t)
	q, err := url.ParseQuery(`filter={"rank":1}&name=beta&sort=-rank&cursor=abc&page_size=9&include_total=true` +
		`&include_deleted=1&search=b&facets=name,name&include=tags&fields=id,name,id`)
	require.NoError(t, err)
	req, err := r.ParseList(q)
	require.NoError(t, err)
	assert.NotNil(t, req.Filter(), "flat and JSON filters are one filter")
	assert.Equal(t, 2, req.Filter().ConditionCount())
	assert.Equal(t, "rank", req.Sort().Fields[0].Name)
	assert.Equal(t, filter.SortDesc, req.Sort().Fields[0].Direction)
	assert.Equal(t, "abc", req.Cursor())
	assert.Equal(t, 3, req.PageSize(), "clamped to the maximum")
	assert.True(t, req.IncludeTotal())
	assert.True(t, req.IncludeDeleted())
	assert.Equal(t, "b", req.Search())
	assert.Equal(t, []string{"name"}, req.Facets())
	assert.Equal(t, []string{"tags"}, req.Include())
	assert.Equal(t, []string{"id", "name"}, req.Fields())

	empty, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	assert.Equal(t, 2, empty.PageSize(), "the default page size")
	assert.Nil(t, empty.Filter())
}

func TestParseList_RefusesWhatItCannotHonor(t *testing.T) {
	r := widgets(t)
	bare, err := parse.NewResource[widget](onSQLite, "widgets")
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		r     *parse.Resource[widget]
		query string
		code  string
	}{
		"non-numeric page size":           {r, "page_size=ten", filter.ECodeInvalidListParam.String()},
		"zero page size":                  {r, "page_size=0", filter.ECodeInvalidListParam.String()},
		"negative page size":              {r, "page_size=-1", filter.ECodeInvalidListParam.String()},
		"malformed flag":                  {r, "include_total=yes", filter.ECodeInvalidListParam.String()},
		"deleted rows of a table without": {bare, "include_deleted=true", filter.ECodeInvalidListParam.String()},
		"misspelt filter":                 {r, "nmae=beta", filter.ECodeUnknownField.String()},
		"unknown facet":                   {r, "facets=colour", filter.ECodeUnknownField.String()},
		"field that is not facetable":     {r, "facets=rank", filter.ECodeUnknownField.String()},
		"field that is not a relation":    {r, "include=name", filter.ECodeInvalidRelation.String()},
		"unknown sort":                    {r, "sort=colour", filter.ECodeSortFieldNotAllowed.String()},
	} {
		t.Run(name, func(t *testing.T) {
			q, buildErr := url.ParseQuery(tc.query)
			require.NoError(t, buildErr)
			_, buildErr = tc.r.ParseList(q)
			var pe *core.ParseError
			require.ErrorAs(t, buildErr, &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}

	type plain struct {
		ID   string `db:"id" parse:"filter,sort"`
		Name string `db:"name" parse:"filter"`
	}
	unsearchable, err := parse.NewResource[plain](onSQLite, "plains")
	require.NoError(t, err)
	_, err = unsearchable.ParseList(url.Values{"search": {"x"}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidListParam.String(), pe.Code)

	accepting, err := parse.NewResource[widget](onSQLite, "widgets", parse.AcceptParams("_", "view"))
	require.NoError(t, err)
	req, err := accepting.ParseList(url.Values{"_": {"123"}, "view": {"grid"}})
	require.NoError(t, err, "accepted parameters are the endpoint's")
	assert.Nil(t, req.Filter())
}

// A misspelt flat filter cannot widen a list through any entry point; list
// parameters and the names an endpoint accepts still pass.
func TestEveryEntryPoint_RefusesAMisspeltFilter(t *testing.T) {
	r, err := parse.NewResource[widget](onSQLite, "widgets", parse.AcceptParams("view"))
	require.NoError(t, err)
	entryPoints := map[string]func(url.Values) error{
		"Resource.ParseList": func(q url.Values) error { _, err := r.ParseList(q); return err },
		"Resource.ParseRequestFilter": func(q url.Values) error {
			_, err := r.ParseRequestFilter(q, filter.QueryParamAccept("view"))
			return err
		},
		"filter.QueryParamParser": func(q url.Values) error {
			qp, err := filter.NewQueryParamParser(r.FilterSchema(), filter.QueryParamAccept("view"))
			if err != nil {
				return err
			}
			_, err = qp.Parse(q)
			return err
		},
	}
	for name, parseQuery := range entryPoints {
		t.Run(name, func(t *testing.T) {
			var pe *core.ParseError
			require.ErrorAs(t, parseQuery(url.Values{"nam": {"x"}}), &pe)
			assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code)
			assert.NoError(t, parseQuery(url.Values{"name": {"x"}, "sort": {"-rank"}, "page_size": {"5"}, "view": {"grid"}}))
		})
	}
}

func TestBuildList_UsesTheDeclaredKeyOnRequestViews(t *testing.T) {
	type entry struct {
		Code string         `json:"code" db:"code"`
		Data map[string]any `json:"data" db:"data" parse:"filter"`
	}
	r := parse.MustResource[entry](onSQLite, "entries", parse.PrimaryKey("code"))
	view, err := r.WithPaths(filter.PathDecl{Path: "data.rank", Type: core.FieldTypeInt, Sort: true})
	require.NoError(t, err)
	for _, resource := range []*parse.Resource[entry]{r, view} {
		plan, buildErr := resource.BuildList(nil, parse.Scope{})
		require.NoError(t, buildErr)
		require.Len(t, plan.Keyset.Keys, 1)
		assert.Equal(t, "code", plan.Keyset.Keys[0].Name)
		assert.Contains(t, plan.Page.SQL, `ORDER BY "entries"."code" COLLATE BINARY ASC`)
	}
	req, err := view.ParseList(url.Values{filter.ParamSort: {"data.rank"}})
	require.NoError(t, err)
	plan, err := view.BuildList(req, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, plan.Keyset.Keys, 2)
	assert.Equal(t, "data.rank", plan.Keyset.Keys[0].Name)
	assert.Equal(t, "code", plan.Keyset.Keys[1].Name)
}

func TestResource_ParseFilter(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	result, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount() != 1 {
		t.Errorf("expected 1 condition, got %d", result.ConditionCount())
	}
}

func TestResource_ParseFilter_InvalidEnum(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	_, err := tp.ParseFilter(`{"status":"invalid"}`)
	if err == nil {
		t.Fatal("expected error for invalid enum")
	}
}

func TestResource_ParseFilterMap(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	result, err := tp.ParseFilterMap(map[string]any{"status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount() != 1 {
		t.Errorf("expected 1 condition, got %d", result.ConditionCount())
	}
}

func TestResource_ParseFilterMap_Empty(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	result, err := tp.ParseFilterMap(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount() != 0 {
		t.Errorf("expected empty parsed filter, got %d conditions", result.ConditionCount())
	}
}

func TestResource_ParseFilterMap_InvalidEnum(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	_, err := tp.ParseFilterMap(map[string]any{"status": "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

// TestResource_ParseFilterMap_EquivalentToParseFilter sanity-checks that
// the map and string paths produce structurally identical ParsedFilter
// values for the same input.
func TestResource_ParseFilterMap_EquivalentToParseFilter(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	fromString, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	fromMap, err := tp.ParseFilterMap(map[string]any{"status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if fromString.ConditionCount() != fromMap.ConditionCount() {
		t.Errorf("condition count mismatch: string=%d map=%d",
			fromString.ConditionCount(), fromMap.ConditionCount())
	}
}

func TestResource_ListQuery(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	lq, err := tp.ListQuery()
	if err != nil {
		t.Fatal(err)
	}
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListLimit(10),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Limit != 10 {
		t.Errorf("expected limit 10, got %d", result.Limit)
	}
}

// TestSearchParamDefault verifies a generated search schema advertises the
// default query parameter.
func TestSearchParamDefault(t *testing.T) {
	type Widget struct {
		Name string `json:"name" parse:"filter,search" filter_ops:"eq"`
	}
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Widget](reg)
	resp := reg.SchemaFor("widgets")
	if resp == nil || resp.Search == nil {
		t.Fatal("expected search schema")
	}
	// The advertised parameter is the one list handlers read.
	if resp.Search.Param != filter.ParamSearch {
		t.Errorf("default search param = %q, want %q", resp.Search.Param, filter.ParamSearch)
	}
}

// TestSearchParamCustom verifies WithSearchParam threads through the
// generator onto SearchSchema.Param.
func TestSearchParamCustom(t *testing.T) {
	type Doc struct {
		Body string `json:"body" parse:"search"`
	}
	gen := core.NewSchemaGenerator(core.WithSearchParam("query"))
	ss, err := filter.GenerateSearchSchema[Doc](gen)
	if err != nil {
		t.Fatalf("GenerateSearchSchema: %v", err)
	}
	if ss == nil {
		t.Fatal("expected search schema")
	}
	if ss.Param != "query" {
		t.Errorf("Param = %q, want query", ss.Param)
	}
}

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
	bare, err := parse.NewResource[widget](onSQLite, "widgets", atOneInstant())
	require.NoError(t, err)
	plain, err := parse.NewResource[unsearchable](onSQLite, "plains", atOneInstant())
	require.NoError(t, err)
	accepting, err := parse.NewResource[widget](onSQLite, "widgets", parse.AcceptParams("_", "view"), atOneInstant())
	require.NoError(t, err)
	points := map[string]listEntryPoints{"widgets": widgets(t), "bare": bare, "unsearchable": plain, "accepting": accepting}
	return points
}

// listCorpusCase is one list request of the corpus: its query string, its
// ListInput and the code both are refused with, if any.
type listCorpusCase struct {
	name     string
	resource string
	query    string
	input    parse.ListInput
	code     string
}

// listCorpus is the list corpus: each request as a query string and as the
// same ListInput. Both entry points must return equal requests, or equal
// errors with the code named.
func listCorpus() []listCorpusCase {
	cases := []listCorpusCase{
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
			name: "unknown response field", resource: "widgets", code: filter.ECodeUnknownField.String(),
			query: "fields=id,nmae", input: parse.ListInput{Fields: []string{"id", "nmae"}},
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
	return cases
}

func TestListCorpus_BothEntryPointsAgree(t *testing.T) {
	resources := listResources(t)
	for _, tc := range listCorpus() {
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
	req, err := widgets(t).ParseListInput(listCorpus()[1].input)
	require.NoError(t, err)
	assert.Equal(t, 2, req.Filter().ConditionCount(), "the JSON and field filters are one filter")
	assert.Equal(t, "rank", req.Sort().Fields[0].Name)
	assert.Equal(t, "abc", req.Cursor())
	assert.Equal(t, 3, req.PageSize(), "clamped to the maximum")
	assert.True(t, req.IncludeTotal())
	assert.True(t, req.IncludeDeleted())
	assert.Equal(t, "b", req.Search())
	assert.Equal(t, []string{"name"}, req.Facets())
	assert.Equal(t, []string{"tags"}, req.Include())
	assert.Equal(t, []string{"id", "name"}, req.Fields())
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
	views, err := parse.NewResource[view](onSQLite, "views")
	require.NoError(t, err)
	req, err := views.ParseListInput(parse.ListInput{FieldFilters: map[string][]string{"sort": {"name"}}})
	require.NoError(t, err)
	require.NotNil(t, req.Filter())
	assert.Equal(t, 1, req.Filter().ConditionCount(), "a field named sort is filtered on")
	assert.Nil(t, req.Sort())
}

// A field mask names the fields a response carries: a read column's, a
// computed column's, or one with no column, which the service fills. A
// typo, a hidden field, or a field whose column the resource does not read
// is refused alike in a query string, an input and a body, the field named.
func TestParseListInput_FieldMask(t *testing.T) {
	type account struct {
		ID     string   `json:"id"     db:"id"     parse:"filter,sort"`
		Email  string   `json:"email"  db:"email"  parse:"filter"`
		Label  string   `json:"label"  db:"label"`
		Hash   string   `json:"hash"   db:"hash"`
		Notes  string   `json:"notes"  db:"notes"  parse:"search"`
		Secret string   `json:"-"      db:"secret"`
		Avatar string   `json:"avatar" db:"-"`
		Roles  []string `json:"roles"  db:"-"      parse:"filter" relation:"account_roles,account_id,role_id"`
	}
	accounts, err := parse.NewResource[account](onSQLite, "accounts",
		parse.ReadColumns("id", "email", "label", "secret"), parse.QueryOnlyColumns("notes"),
		parse.ReadExpr("label", parse.SQL("upper(accounts.email)")))
	require.NoError(t, err)

	mask := []string{"roles", "id", "avatar", "label", "email"}
	req, err := accounts.ParseListInput(parse.ListInput{Fields: mask})
	require.NoError(t, err)
	assert.Equal(t, mask, req.Fields(), "valid names round-trip in order")
	assert.Equal(t, mask, req.FieldMembers(), "under the json tag a field is its member")

	for _, name := range []string{"nmae", "hash", "notes", "secret", "Secret"} {
		t.Run(name, func(t *testing.T) {
			fromQuery, err := accounts.ParseList(url.Values{filter.ParamFields: {"id," + name}})
			assert.Nil(t, fromQuery)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code)
			assert.Equal(t, map[string]any{"field": name}, pe.Details)
			assert.Equal(t, "response field '"+name+"' does not exist", pe.Message,
				"a withheld field reads as no field at all")

			_, inputErr := accounts.ParseListInput(parse.ListInput{Fields: []string{"id", name}})
			assert.Equal(t, err, inputErr)
			_, bodyErr := accounts.ParseListBody(strings.NewReader(`{"fields":["id","` + name + `"]}`))
			assert.Equal(t, err, bodyErr)
		})
	}
}

// Under another field-name tag a mask still names fields by it, and its
// members are the names encoding/json writes the same fields under: a
// renamed field, one json names by its Go name, a promoted field, and none
// for a field json leaves out.
func TestParseListInput_FieldMembersFollowTheJSONEncoding(t *testing.T) {
	type audit struct {
		CreatedBy string `bson:"created_by" json:"createdBy" db:"created_by"`
	}
	type record struct {
		audit
		ID    string `bson:"_id"   json:"id"  db:"id"    parse:"filter,sort"`
		Title string `bson:"title"            db:"title"`
		Score int    `bson:"score" json:"-"  db:"score"`
	}
	records, err := parse.NewResource[record](onSQLite, "records", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	require.NoError(t, err)

	tests := []struct {
		name    string
		fields  []string
		members []string
	}{
		{"renamed and Go-named", []string{"title", "_id"}, []string{"Title", "id"}},
		{"promoted", []string{"created_by"}, []string{"createdBy"}},
		{"a field json leaves out", []string{"score", "_id"}, []string{"id"}},
		{"only fields json leaves out", []string{"score"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := records.ParseListInput(parse.ListInput{Fields: tt.fields})
			require.NoError(t, err)
			assert.Equal(t, tt.fields, req.Fields())
			assert.Equal(t, tt.members, req.FieldMembers())
		})
	}
	_, err = records.ParseListInput(parse.ListInput{Fields: []string{"id"}})
	require.Error(t, err, "a mask names fields under the field-name tag, not their members")
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
	plan, err := res.BuildList(req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `"widgets"."name" IN (SELECT value AS v FROM json_each(?1))`, "a repeated field filter is a set")
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

func TestParseFilterWithCostPreservesResolvedOptions(t *testing.T) {
	t.Run("codec and clock", func(t *testing.T) {
		frozen := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		decodes := 0
		parser, err := parse.NewResource[limitsDevice](onSQLite, "filter_budget_devices", parse.ParserOptions(
			core.WithMaxCost(1),
			core.WithClock(func() time.Time { return frozen }),
			core.WithCodec(countingCodec{unmarshals: &decodes}),
		))
		require.NoError(t, err)
		parsed, err := parser.ParseFilterWithCost(`{"created_at":{"$gt":"now-7d"}}`, 10)
		require.NoError(t, err)
		require.Positive(t, decodes)
		require.Equal(t, core.FormatTimestamp(frozen.Add(-7*24*time.Hour)), parsed.AST().Root.Value)
	})
	for _, test := range []struct {
		name  string
		limit core.Option
		query string
	}{
		{"input bytes", core.WithMaxInputSize(4), `{"status":"active"}`},
		{"conditions", core.WithMaxConditions(1), `{"status":"active","count":1}`},
		{"array values", core.WithMaxArraySize(1), `{"count":{"$in":[1,2]}}`},
		{"depth", core.WithMaxDepth(1), `{"$and":[{"$or":[{"status":"active"},{"count":1}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parser, err := parse.NewResource[limitsDevice](onSQLite, "filter_budget_devices", parse.ParserOptions(core.WithMaxCost(1), test.limit))
			require.NoError(t, err)
			_, err = parser.ParseFilterWithCost(test.query, 1000)
			require.Error(t, err, "cost allowance must retain the resource's other limits")
		})
	}
}

func TestParseFilterWithCostRejectsInvalidBudget(t *testing.T) {
	parser, err := parse.NewResource[filterBudgetDocument](onSQLite, "budget_documents")
	require.NoError(t, err)
	for _, cost := range []int{0, -1} {
		_, err := parser.ParseFilterWithCost("", cost)
		require.Error(t, err, "invalid server policy is refused even for an empty filter")
	}
	_, err = parser.ParseFilterWithCost("", 1)
	require.NoError(t, err)
	_, err = parser.ParseFilter("")
	require.NoError(t, err)
}

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

// Cursor keys: every cursor a page mints decodes, sort keys are scalars, and
// string keys order and seek by code point on every dialect.

// A page whose boundary row holds a long sort value fails with E2065
// instead of returning a cursor the next request rejects.
func TestNewPage_RefusesACursorItCouldNotDecode(t *testing.T) {
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "name", Column: "name", DataType: core.FieldTypeString, Direction: filter.SortAsc}}}
	rows := []string{strings.Repeat("a", 4000), strings.Repeat("b", 4000)}

	_, err := filter.NewPage(rows, 1, plan, func(s, _ string) (any, error) { return s, nil })

	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "got %v", err)
	assert.Equal(t, filter.ECodeCursorTooLarge.String(), pe.Code)
}

// Every cursor a page can mint decodes.
func TestNewPage_MintedCursorsDecode(t *testing.T) {
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "name", Column: "name", DataType: core.FieldTypeString, Direction: filter.SortAsc}}}
	rows := []string{strings.Repeat("a", 2000), strings.Repeat("b", 2000)}

	page, err := filter.NewPage(rows, 1, plan, func(s, _ string) (any, error) { return s, nil })
	require.NoError(t, err)
	_, err = filter.DecodeCursor(string(page.PageInfo.EndCursor))
	assert.NoError(t, err)
}

// A JSON document or array cannot be a sort key; a struct whose value
// reduces to a scalar can.
func TestGenerateSortSchema_ScalarKeysOnly(t *testing.T) {
	type labelled struct {
		ID     string         `json:"id" db:"id" parse:"sort"`
		Labels map[string]any `json:"labels" db:"labels" parse:"sort"`
	}
	_, err := parse.NewResource[labelled](onSQLite, "labelleds")
	require.ErrorContains(t, err, "json fields are not sortable")

	type tagged struct {
		ID   string   `json:"id" db:"id" parse:"sort"`
		Tags []string `json:"tags" db:"tags" parse:"sort"`
	}
	_, err = parse.NewResource[tagged](onSQLite, "taggeds")
	require.ErrorContains(t, err, "array fields are not sortable")

	type priced struct {
		ID    string          `json:"id" db:"id" parse:"sort"`
		Price decimal.Decimal `json:"price" db:"price" parse:"sort"`
	}
	p, err := parse.NewResource[priced](onSQLite, "priceds")
	require.NoError(t, err)
	assert.Contains(t, p.SortSchema().Fields, "price")
}

// String keys order and seek by code point on every dialect.
func TestListQuery_StringKeysOrderByCodePoint(t *testing.T) {
	schema := &filter.ResourceSchema{Name: "probe", PrimaryKey: []string{"rank"}}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {Table: "probe", Column: "name", DataType: core.FieldTypeString},
		"rank": {Table: "probe", Column: "rank", DataType: core.FieldTypeInt},
	}}
	for _, tc := range []struct {
		dialect filter.Dialect
		collate string
	}{
		{filter.PostgresDialect{}, `"probe"."name" COLLATE pg_c_utf8`},
		{filter.PostgresDialect{Collation: filter.CollationC}, `"probe"."name" COLLATE "C"`},
		{filter.SQLiteDialect{}, `"probe"."name" COLLATE BINARY`},
	} {
		lq, err := filter.NewListQuery(schema, sorts, tc.dialect)
		require.NoError(t, err)
		q, err := lq.Build(filter.ListSort("name,rank"))
		require.NoError(t, err)
		assert.Contains(t, q.OrderBy, tc.collate+" ASC", tc.dialect.Name())
		assert.NotContains(t, q.OrderBy, `"probe"."rank" COLLATE`, "non-string keys keep their own order")
	}
}

func TestCursorValuesValidateAgainstActiveKeys(t *testing.T) {
	tests := []struct {
		name     string
		dt       core.FieldType
		kind     reflect.Kind
		nullable bool
		value    any
		want     any
		invalid  bool
	}{
		{name: "integer boolean", dt: core.FieldTypeInt, value: true, invalid: true},
		{name: "integer string", dt: core.FieldTypeInt, value: "42", invalid: true},
		{name: "integer null", dt: core.FieldTypeInt, invalid: true},
		{name: "fractional integer", dt: core.FieldTypeInt, value: 1.5, invalid: true},
		{name: "inexact integer", dt: core.FieldTypeInt, value: float64(1 << 54), invalid: true},
		{name: "int8 overflow", dt: core.FieldTypeInt, kind: reflect.Int8, value: int64(128), invalid: true},
		{name: "unsigned negative", dt: core.FieldTypeInt, kind: reflect.Uint8, value: int64(-1), invalid: true},
		{name: "unsigned overflow", dt: core.FieldTypeInt, kind: reflect.Uint8, value: uint64(256), invalid: true},
		{name: "float32 overflow", dt: core.FieldTypeFloat, kind: reflect.Float32, value: math.MaxFloat64, invalid: true},
		{name: "invalid UUID", dt: core.FieldTypeUUID, value: "invalid", invalid: true},
		{name: "invalid timestamp", dt: core.FieldTypeTimestamp, value: "yesterday", invalid: true},
		{name: "boolean string", dt: core.FieldTypeBool, value: "true", invalid: true},
		{name: "numeric string", dt: core.FieldTypeString, value: int64(42), invalid: true},
		{name: "max integer", dt: core.FieldTypeInt, value: int64(math.MaxInt64), want: int64(math.MaxInt64)},
		{name: "max unsigned", dt: core.FieldTypeInt, kind: reflect.Uint64, value: uint64(math.MaxUint64), want: uint64(math.MaxUint64)},
		{name: "small unsigned", dt: core.FieldTypeInt, kind: reflect.Uint8, value: uint8(255), want: uint64(255)},
		{name: "integral float", dt: core.FieldTypeInt, value: 42.0, want: int64(42)},
		{name: "float", dt: core.FieldTypeFloat, value: 1.5, want: 1.5},
		{name: "nullable integer", dt: core.FieldTypeInt, nullable: true},
		{name: "uuid", dt: core.FieldTypeUUID, value: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", want: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		{name: "timestamp", dt: core.FieldTypeTimestamp, value: "2026-01-01T01:00:00+01:00", want: "2026-01-01T00:00:00.000000000Z"},
		{name: "boolean", dt: core.FieldTypeBool, value: true, want: true},
		{name: "string", dt: core.FieldTypeString, value: "key", want: "key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &filter.PageToken{Fields: []filter.CursorField{{Name: "key", Direction: filter.SortAsc, Value: tt.value}}}
			encoded, err := filter.EncodeCursor(token)
			require.NoError(t, err)
			decoded, err := filter.DecodeCursor(encoded)
			require.NoError(t, err)
			err = filter.ValidateCursor(decoded, []filter.CursorKey{{Name: "key", Direction: filter.SortAsc, DataType: tt.dt, NumberKind: tt.kind, Nullable: tt.nullable}})
			if tt.invalid {
				var pe *core.ParseError
				require.ErrorAs(t, err, &pe)
				require.Equal(t, filter.ECodeInvalidCursor.String(), pe.Code)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, decoded.Fields[0].Value)
		})
	}
}

func TestCursorValidationFailureDoesNotNormalizeEarlierValues(t *testing.T) {
	token := &filter.PageToken{Fields: []filter.CursorField{
		{Name: "uuid", Direction: filter.SortAsc, Value: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"},
		{Name: "id", Direction: filter.SortAsc, Value: false},
	}}
	err := filter.ValidateCursor(token, []filter.CursorKey{
		{Name: "uuid", Direction: filter.SortAsc, DataType: core.FieldTypeUUID},
		{Name: "id", Direction: filter.SortAsc, DataType: core.FieldTypeInt},
	})
	require.Error(t, err)
	require.Equal(t, "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", token.Fields[0].Value)
	require.Error(t, filter.ValidateCursor(nil, nil))
}

func TestGeneratedSortSchemaValidatesCursorRangeAndNullableValues(t *testing.T) {
	type row struct {
		ID      uint8     `json:"id" db:"id" parse:"filter,sort"`
		Created time.Time `json:"created" db:"created" parse:"sort"`
		UUID    uuid.UUID `json:"uuid" db:"uuid" parse:"sort"`
		Rank    *int16    `json:"rank" db:"rank" parse:"sort"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	require.NoError(t, err)
	require.Equal(t, reflect.Uint8, p.SortSchema().Fields["id"].NumberKind)
	require.Equal(t, reflect.Int16, p.SortSchema().Fields["rank"].NumberKind)
	for _, dialect := range []filter.Dialect{filter.SQLiteDialect{}, filter.PostgresDialect{}} {
		for _, value := range []any{true, "bad", nil, int64(256)} {
			encoded, encodeErr := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: value}}})
			require.NoError(t, encodeErr)
			lq, encodeErr := filter.NewListQuery(p.FilterSchema(), p.SortSchema(), dialect)
			require.NoError(t, encodeErr)
			_, encodeErr = lq.Build(filter.ListCursor(encoded))
			require.Error(t, encodeErr)
			// Filter-only PKs retain their numeric range too.
			lq, encodeErr = filter.NewListQuery(p.FilterSchema(), nil, dialect)
			require.NoError(t, encodeErr)
			_, encodeErr = lq.Build(filter.ListCursor(encoded))
			require.Error(t, encodeErr)
		}
	}
	sort, err := filter.ParseSort(p.SortSchema(), "created,uuid,rank")
	require.NoError(t, err)
	keys := filter.Keyset(sort, filter.CursorKey{Name: "id", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Uint8})
	plan := filter.CursorPlan{Keys: keys}
	values := map[string]any{"id": uint8(255), "created": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "uuid": uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), "rank": (*int16)(nil)}
	token, err := plan.Token(false, func(column string) (any, error) { return values[column], nil })
	require.NoError(t, err)
	encoded, err := filter.EncodeCursor(token)
	require.NoError(t, err)
	token, err = filter.DecodeCursor(encoded)
	require.NoError(t, err)
	require.NoError(t, filter.ValidateCursor(token, keys))
	require.Equal(t, uint64(255), token.Fields[3].Value)
}

func TestSystemFieldCursorTieBreakerValidatesType(t *testing.T) {
	for _, dt := range []core.FieldType{core.FieldTypeInt, core.FieldTypeUUID} {
		schema := &filter.ResourceSchema{Name: "rows", PrimaryKey: []string{"id"}, SystemFields: map[string]*filter.FieldSchema{
			"id": {Table: "rows", Column: "id", DataType: dt, NumberKind: reflect.Uint8},
		}}
		for _, dialect := range []filter.Dialect{filter.SQLiteDialect{}, filter.PostgresDialect{}} {
			values := []any{true, nil, "not-a-key", int64(256)}
			for _, value := range values {
				encoded, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: value}}})
				require.NoError(t, err)
				lq, err := filter.NewListQuery(schema, nil, dialect)
				require.NoError(t, err)
				_, err = lq.Build(filter.ListCursor(encoded))
				var pe *core.ParseError
				require.ErrorAs(t, err, &pe, "type %s, value %v, error %v", dt, value, err)
				require.Equal(t, filter.ECodeInvalidCursor.String(), pe.Code)
			}
			var valid any = int64(255)
			if dt == core.FieldTypeUUID {
				valid = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			}
			encoded, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: "id", Direction: filter.SortAsc, Value: valid}}})
			require.NoError(t, err)
			lq, err := filter.NewListQuery(schema, nil, dialect)
			require.NoError(t, err)
			result, err := lq.Build(filter.ListCursor(encoded))
			require.NoError(t, err)
			require.Contains(t, result.OrderBy, `"rows"."id"`)
		}
	}
}

func TestBuildList_GovernedRelationsFollowTheRow(t *testing.T) {
	r := parsetest.GovernedDocuments(t, onPostgres)
	build := func(t *testing.T, scope parse.Scope) (*parse.ListPlan, error) {
		t.Helper()
		req, err := r.ParseListInput(parse.ListInput{
			Filter: `{"notes":{"$some":{"reactions":{"$some":{"kind":"up"}}}},"labels":"lb","topics":"z"}`,
			Facets: []string{"notes", "labels"}, Include: []string{"notes", "labels"},
		})
		require.NoError(t, err)
		return r.BuildList(req, scope)
	}

	t.Run("every relation tenant column is compared with the row's", func(t *testing.T) {
		plan, err := build(t, parse.Scope{Governance: parsetest.GovernanceA()})
		require.NoError(t, err, "a governed list needs no Scope.Tenant")
		for _, want := range []string{
			`"notes"."tenant_id" = "documents"."tenant_id"`,           // depth one
			`"reactions"."tenant_id" = "documents"."tenant_id"`,       // depth two
			`"rel1__"."tenant_id" = "documents"."tenant_id"`,          // the pair table
			`"labels"."tenant_id" = "documents"."tenant_id"`,          // the pair's target
			`"document_topics"."tenant_id" = "documents"."tenant_id"`, // the pair table compared by key
		} {
			assert.Contains(t, plan.Page.SQL, want)
		}
		assert.Contains(t, plan.Facets.SQL, `"documents"."tenant_id" AS __owner_tenant`)
		assert.Contains(t, plan.Facets.SQL, `"j"."tenant_id" = base.__owner_tenant`)
		bound := 0
		for _, arg := range plan.Page.Args {
			if arg == "A" {
				bound++
			}
		}
		assert.Equal(t, 1, bound, "A is bound once, by the governance tree: relations bind no tenant")

		queries, err := r.IncludeQueries(plan, []parse.Key{{"a1"}, {"b1"}})
		require.NoError(t, err)
		require.Len(t, queries, 2)
		for _, q := range queries {
			assert.Contains(t, q.SQL, `JOIN "documents" owner__ ON "owner__"."id" = "j"."document_id"`, q.Field)
			assert.Contains(t, q.SQL, `"j"."tenant_id" = "owner__"."tenant_id"`, q.Field)
		}
	})

	t.Run("a scope's tenant is not used under governance", func(t *testing.T) {
		governed, err := build(t, parse.Scope{Governance: parsetest.GovernanceA()})
		require.NoError(t, err)
		withTenant, err := build(t, parse.Scope{Governance: parsetest.GovernanceA(), Tenant: "not-checked"})
		require.NoError(t, err)
		assert.Equal(t, governed.Page, withTenant.Page)
	})

	// A tree with no constraint anywhere in it constrains nothing, however
	// it nests, so it is no governance: it must never stand in for the
	// tenant floor and admit every tenant's rows.
	t.Run("empty governance is the tenant floor", func(t *testing.T) {
		floor, err := build(t, parse.Scope{Tenant: "A"})
		require.NoError(t, err)
		for name, empty := range map[string]*filter.PolicyConstraints{
			"no groups":           {},
			"an empty And group":  {And: []*filter.PolicyConstraints{{}}},
			"an empty Or group":   {Or: []*filter.PolicyConstraints{{}}},
			"nested empty groups": {And: []*filter.PolicyConstraints{{Or: []*filter.PolicyConstraints{{}, nil}}, nil}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := build(t, parse.Scope{Governance: empty})
				require.ErrorIs(t, err, parse.ErrTenantRequired)
				plan, err := build(t, parse.Scope{Governance: empty, Tenant: "A"})
				require.NoError(t, err)
				assert.Equal(t, floor.Page, plan.Page)
				assert.Contains(t, plan.Page.Args, "A")
				assert.NotContains(t, plan.Page.SQL, `= "documents"."tenant_id"`)
			})
		}
	})

	t.Run("a resource without a tenant column holds relations to Scope.Tenant", func(t *testing.T) {
		labels := parse.MustResource[parsetest.GovLabel](onPostgres, "labels", parse.TenantColumn("tenant_id"))
		boards := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "boards")
		boards.MustRelate("labels", parse.ManyToMany(labels,
			parse.Junction{Table: "board_labels", Source: []string{"device_id"}, Target: []string{"label_id"}, Tenant: "tenant_id"}, parse.Indexed()))
		req, err := boards.ParseListInput(parse.ListInput{Filter: `{"labels":"la"}`})
		require.NoError(t, err)
		plan, err := boards.BuildList(req, parse.Scope{Governance: parsetest.GovEq("id", int64(1)), Tenant: "A"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.Args, "A")
		_, err = boards.BuildList(req, parse.Scope{Governance: parsetest.GovEq("id", int64(1))})
		assert.ErrorContains(t, err, "tenant-scoped", "its relations still need a tenant")
	})
}

func TestPolicy_NamesColumnsRequestsCannotFilter(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
	tests := []struct {
		name       string
		constraint *filter.PolicyConstraint
		wantSQL    string
		wantArg    any
	}{
		{"a hidden field by column", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: 7}, `"documents"."owner_id" = $1`, int64(7)},
		{"a field without the filter capability by name", &filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "eu"}, `"documents"."region_code" = $1`, "eu"},
		{"the same field by column", &filter.PolicyConstraint{Field: "region_code", Operator: filter.OpEq, Value: "us"}, `"documents"."region_code" = $1`, "us"},
		{"a filter field by column", &filter.PolicyConstraint{Field: "folder_id", Operator: filter.OpEq, Value: 3}, `"documents"."folder_id" = $1`, int64(3)},
		{"an operator the field's request operators lack", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpIn, Value: []any{7, 8}}, `"documents"."owner_id" = ANY($1)`, []int64{7, 8}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := docs.BuildList(nil, parsetest.PolicyOn(tt.constraint))
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, tt.wantSQL)
			assert.Equal(t, []any{tt.wantArg}, plan.Page.Args[:1])
		})
	}
}

func TestPolicy_ServerFieldsStayOutOfRequests(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
	for _, q := range []url.Values{
		{filter.ParamFilter: {`{"owner_id":7}`}},
		{filter.ParamFilter: {`{"region":"eu"}`}},
		{filter.ParamFilter: {`{"folder_id":3}`}},
		{"owner_id": {"7"}},
		{"region": {"eu"}},
	} {
		_, err := docs.ParseList(q)
		require.ErrorIs(t, err, filter.ECodeUnknownField, q.Encode())
	}

	fields := map[string]bool{}
	for _, f := range docs.FilterSchema().Fields {
		fields[f.ExternalName] = true
	}
	assert.Equal(t, map[string]bool{"id": true, "title": true, "folderId": true}, fields, "the schema document advertises request fields only")
}

func TestPolicy_ServerFieldsKeepTheirTypes(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)

	_, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpLike, Value: "7%"}))
	require.ErrorIs(t, err, filter.ErrPolicyInvalid, "an operator the type has not")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven"}))
	require.ErrorIs(t, err, filter.ErrPolicyInvalid, "a value the type refuses")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "apac"}))
	require.ErrorIs(t, err, filter.ErrPolicyInvalid, "a value outside the field's enum")

	plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven", UnknownIfInvalid: true}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "WHERE NULL", "an invalid value under UnknownIfInvalid admits no row")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner", Operator: filter.OpEq, Value: 7}))
	assert.ErrorIs(t, err, filter.ErrPolicyInvalid, "a name no column has still fails closed")
}

// serverFieldRows are serverFieldFixture's rows as a memory backend holds
// them, by column.
func serverFieldRows() []map[string]any {
	rows := []map[string]any{
		{"id": 1, "owner_id": 7, "region_code": "eu", "title": "alpha", "folder_id": 1},
		{"id": 2, "owner_id": 7, "region_code": "us", "title": "beta", "folder_id": 1},
		{"id": 3, "owner_id": 8, "region_code": "eu", "title": "alpha", "folder_id": 2},
		{"id": 4, "owner_id": 7, "region_code": "eu", "title": "gamma", "folder_id": 2},
	}
	return rows
}

// TestPolicyServerFields_Memory evaluates the policy the SQLite and
// PostgreSQL cases run over the same rows, with the reference evaluator, as
// a memory backend does.
func TestPolicyServerFields_Memory(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
	ast, err := filter.ApplyPolicyConstraints(docs.FilterSchema(), nil, parsetest.OwnerEUPolicy().Policy)
	require.NoError(t, err)
	var ids []int
	for _, row := range serverFieldRows() {
		truth, err := filter.Evaluate(ast.Root, filter.ColumnRow(func(column filter.ColumnRef) (any, bool) {
			v, ok := row[column.Column]
			return v, ok && column.Table == "documents"
		}))
		require.NoError(t, err)
		if truth == filter.True {
			ids = append(ids, row["id"].(int))
		}
	}
	slices.Sort(ids)
	assert.Equal(t, []int{1, 4}, ids)
}

// The region types hide a column named like a visible field without the
// filter capability, declared before it and after it.
type regionHiddenFirst struct {
	ID     int    `json:"id"     db:"id"`
	Legacy string `json:"-"      db:"region"`
	Region string `json:"region" db:"region_code"`
}

type regionHiddenLast struct {
	ID     int    `json:"id"     db:"id"`
	Region string `json:"region" db:"region_code"`
	Legacy string `json:"-"      db:"region"`
}

// The title types hide a column named like a request field.
type titleHiddenFirst struct {
	ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
	Old   string `json:"-"     db:"title"`
	Label string `json:"title" db:"label" parse:"filter"`
}

type titleHiddenLast struct {
	ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"title" db:"label" parse:"filter"`
	Old   string `json:"-"     db:"title"`
}

// tagsRelation has a relation field and a column under one name.
type tagsRelation struct {
	ID      int      `json:"id"   db:"id"`
	Tags    []string `json:"-"    db:"-"    parse:"filter" filter_ops:"eq,in" relation:"document_tags,document_id,tag_id"`
	TagText string   `json:"-"    db:"tags"`
}

// policySQL compiles c as the policy of a list of T stored in documents.
func policySQL[T any](t *testing.T, c *filter.PolicyConstraint) (string, error) {
	t.Helper()
	r, err := parse.NewResource[T](onPostgres, "documents")
	require.NoError(t, err)
	plan, err := r.BuildList(nil, parsetest.PolicyOn(c))
	if err != nil {
		return "", err
	}
	return plan.Page.SQL, nil
}

// A policy name that is one field's name and another field's column is
// refused whichever field is declared first, naming both columns.
func TestPolicy_AmbiguousNamesAreRefused(t *testing.T) {
	tests := []struct {
		name    string
		compile func(*testing.T, *filter.PolicyConstraint) (string, error)
		field   string
		wantErr []string
		wantSQL string
	}{
		{"a visible field's name and a hidden column, hidden first", policySQL[regionHiddenFirst], "region", []string{"field Region (column region_code)", "field Legacy (column region)"}, ""},
		{"a visible field's name and a hidden column, hidden last", policySQL[regionHiddenLast], "region", []string{"field Region (column region_code)", "field Legacy (column region)"}, ""},
		{"the visible field by its column", policySQL[regionHiddenFirst], "region_code", nil, `"documents"."region_code" = $1`},
		{"a request field's name and a hidden column, hidden first", policySQL[titleHiddenFirst], "title", []string{"field Label (column label)", "field Old (column title)"}, ""},
		{"a request field's name and a hidden column, hidden last", policySQL[titleHiddenLast], "title", []string{"field Label (column label)", "field Old (column title)"}, ""},
		{"the request field by its column", policySQL[titleHiddenLast], "label", nil, `"documents"."label" = $1`},
		{"a relation's name and a hidden column", policySQL[tagsRelation], "tags", []string{"relation field Tags", "field TagText (column tags)"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, err := tt.compile(t, &filter.PolicyConstraint{Field: tt.field, Operator: filter.OpEq, Value: "x"})
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Contains(t, sql, tt.wantSQL)
				return
			}
			require.ErrorIs(t, err, filter.ErrAmbiguousPolicyField)
			for _, want := range tt.wantErr {
				assert.ErrorContains(t, err, want)
			}
		})
	}

	t.Run("a request still filters by the request field", func(t *testing.T) {
		r, err := parse.NewResource[titleHiddenFirst](onPostgres, "documents")
		require.NoError(t, err)
		req, err := r.ParseList(url.Values{"title": {"x"}})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.SQL, `"documents"."label" = $1`)
	})
}

// untypedPolicyFields has fields without the filter capability whose types
// a filter field could not take.
type untypedPolicyFields struct {
	ID   int      `json:"id"   db:"id"   parse:"filter"`
	Tags []string `json:"tags" db:"tags" storage:"jsonb"`
	Spot string   `json:"-"    db:"spot" geo:"plane"`
}

// A field a filter field could not type does not stop the resource from
// building; a policy that names it gets the field's error.
func TestPolicy_UntypedFieldsKeepTheirErrors(t *testing.T) {
	docs, err := parse.NewResource[untypedPolicyFields](onPostgres, "documents")
	require.NoError(t, err)

	tests := []struct {
		name    string
		field   string
		wantErr string
	}{
		{"a visible field by name", "tags", "field Tags: storage applies to json and object fields, not array"},
		{"a hidden field by column", "spot", `field Spot: geo storage "plane"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: tt.field, Operator: filter.OpNull}))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}

	plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "id", Operator: filter.OpEq, Value: 1}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `"documents"."id" = $1`, "the resource's other fields still resolve")
}

type serverFieldComment struct {
	ID         int `json:"id"         db:"id"`
	DocumentID int `json:"documentId" db:"document_id"`
}

// A relation may not take a name policies resolve: it would change what a
// policy on that name constrains.
func TestRelate_RefusesPolicyNames(t *testing.T) {
	comments := parse.MustResource[serverFieldComment](onPostgres, "comments")
	for _, name := range []string{"owner_id", "region", "region_code", "folder_id"} {
		t.Run(name, func(t *testing.T) {
			docs := parsetest.ServerFieldDocuments(t, onPostgres)
			require.ErrorContains(t, docs.Relate(name, parse.HasMany(comments, "document_id")), "server policies")

			plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: name, Operator: filter.OpNotNull}))
			require.NoError(t, err)
			assert.NotContains(t, plan.Page.SQL, "EXISTS", "a policy on the name still constrains the column")
		})
	}

	t.Run("an ambiguous name", func(t *testing.T) {
		r, err := parse.NewResource[regionHiddenFirst](onPostgres, "documents")
		require.NoError(t, err)
		assert.ErrorContains(t, r.Relate("region", parse.HasMany(comments, "document_id")), "server policies")
	})

	t.Run("a name no field has", func(t *testing.T) {
		assert.NoError(t, parsetest.ServerFieldDocuments(t, onPostgres).Relate("comments", parse.HasMany(comments, "document_id")))
	})
}

// The reference evaluator selects the rows the SQL dialects select.
func TestElementCasesEvaluate(t *testing.T) {
	orders := parsetest.OrderResource(t, onPostgres)
	for _, tc := range parsetest.ElementCases() {
		t.Run(tc.Name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.Filter}})
			require.NoError(t, err)
			var got []string
			for _, r := range parsetest.OrderRows() {
				row := filter.ColumnRow(func(column filter.ColumnRef) (any, bool) {
					i := slices.Index(parsetest.OrderColumns(), column.Column)
					if i < 0 {
						return nil, false
					}
					return r[i], true
				})
				truth, err := filter.Evaluate(req.Filter().AST().Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					got = append(got, r[0].(string))
				}
			}
			assert.Equal(t, tc.Want, got)
		})
	}
}
