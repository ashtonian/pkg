//go:build unit

package filter_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// --- Test Helpers ---

func testSchema() *filter.ResourceSchema {
	return &filter.ResourceSchema{
		Name: "items",
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", SQLExpr: "items.status", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn},
				EnumValues:       []string{"active", "inactive", "suspended"},
			},
			"name": {
				ExternalName: "name", SQLExpr: "items.name", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpLike, filter.OpILike},
			},
			"count": {
				ExternalName: "count", SQLExpr: "items.count", DataType: core.FieldTypeInt,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte},
			},
			"created_at": {
				ExternalName: "created_at", SQLExpr: "items.created_at", DataType: core.FieldTypeTimestamp,
				AllowedOperators: []filter.FilterOperator{filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte},
				Indexed:          true,
			},
		},
		SystemFields: map[string]*filter.FieldSchema{
			"tenant_id": {
				ExternalName: "tenant_id", SQLExpr: "items.tenant_id", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
			},
		},
	}
}

func testSortSchema() *filter.SortSchema {
	return &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"name":       {SQLExpr: "items.name"},
			"created_at": {SQLExpr: "items.created_at", Indexed: true},
		},
		DefaultField: "created_at",
		DefaultDir:   filter.SortDesc,
		MaxFields:    3,
	}
}

// --- FilterParser Tests ---

func TestFilterParser_SimpleEq(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if result.Where != "items.status = $1" {
		t.Errorf("expected items.status = $1, got %q", result.Where)
	}
	if len(result.Args) != 1 || result.Args[0] != "active" {
		t.Errorf("expected [active], got %v", result.Args)
	}
}

func TestFilterParser_MultiCondition(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":"active","count":{"$gt":10}}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 args, got %d", len(result.Args))
	}
	if !strings.Contains(result.Where, "AND") {
		t.Errorf("expected AND in WHERE, got %q", result.Where)
	}
}

func TestFilterParser_InOperator(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":{"$in":["active","inactive"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "IN") {
		t.Errorf("expected IN, got %q", result.Where)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 args for $in, got %d", len(result.Args))
	}
}

func TestFilterParser_UnknownField(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	_, err := fp.Parse(`{"unknown":"x"}`)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestFilterParser_InvalidEnum(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	_, err := fp.Parse(`{"status":"invalid_value"}`)
	if err == nil {
		t.Fatal("expected error for invalid enum")
	}
}

func TestFilterParser_EmptyInput(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if ast.Root != nil {
		t.Error("expected nil root for empty input")
	}
}

func TestFilterParser_OrOperator(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"$or":[{"status":"active"},{"status":"inactive"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "OR") {
		t.Errorf("expected OR, got %q", result.Where)
	}
}

// --- SQL Dialect Tests ---

// --- Sort Tests ---

func TestParseSort_Default(t *testing.T) {
	sr, err := filter.ParseSort(testSortSchema(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Fields) != 1 || sr.Fields[0].Name != "created_at" {
		t.Errorf("expected default sort created_at, got %v", sr.Fields)
	}
	if sr.Fields[0].Direction != filter.SortDesc {
		t.Errorf("expected desc, got %s", sr.Fields[0].Direction)
	}
}

func TestParseSort_Explicit(t *testing.T) {
	sr, err := filter.ParseSort(testSortSchema(), "name,-created_at")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(sr.Fields))
	}
	if sr.Fields[0].Direction != filter.SortAsc {
		t.Errorf("expected asc for name")
	}
	if sr.Fields[1].Direction != filter.SortDesc {
		t.Errorf("expected desc for created_at")
	}
	sql := sr.ToSQL(filter.PostgresDialect{})
	if sql != "ORDER BY items.name ASC, items.created_at DESC" {
		t.Errorf("unexpected SQL: %q", sql)
	}
}

func TestParseSort_InvalidField(t *testing.T) {
	_, err := filter.ParseSort(testSortSchema(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown sort field")
	}
}

// --- QueryParamParser Tests ---

func TestQueryParamParser_SimpleEq(t *testing.T) {
	qp := filter.NewQueryParamParser(testSchema())
	ast, err := qp.Parse(url.Values{"status": {"active"}})
	if err != nil {
		t.Fatal(err)
	}
	if ast.Root == nil {
		t.Fatal("expected non-nil root")
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "items.status = $1") {
		t.Errorf("expected eq condition, got %q", result.Where)
	}
}

func TestQueryParamParser_Suffixes(t *testing.T) {
	qp := filter.NewQueryParamParser(testSchema())
	ast, err := qp.Parse(url.Values{"count_gt": {"10"}, "count_lte": {"100"}})
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 args, got %d", len(result.Args))
	}
}

func TestQueryParamParser_InSuffix(t *testing.T) {
	qp := filter.NewQueryParamParser(testSchema())
	ast, err := qp.Parse(url.Values{"status_in": {"active,inactive"}})
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "IN") {
		t.Errorf("expected IN, got %q", result.Where)
	}
}

func TestQueryParamParser_IgnoresReserved(t *testing.T) {
	qp := filter.NewQueryParamParser(testSchema())
	ast, err := qp.Parse(url.Values{"sort": {"-name"}, "page_size": {"10"}, "cursor": {"abc"}, "include_total": {"true"}, "filter": {"{}"}})
	if err != nil {
		t.Fatal(err)
	}
	if ast.Root != nil {
		t.Error("expected nil root when only reserved params")
	}
}

// --- ListQuery Tests ---

func TestListQuery_Basic(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListSort("-created_at"),
		filter.ListLimit(25),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "items.status = $1") {
		t.Errorf("expected filter, got %q", result.Where)
	}
	if !strings.Contains(result.OrderBy, "DESC") {
		t.Errorf("expected DESC sort, got %q", result.OrderBy)
	}
	if result.Limit != 25 {
		t.Errorf("expected limit 25, got %d", result.Limit)
	}
	if result.FetchLimit != 26 {
		t.Errorf("expected fetch limit 26, got %d", result.FetchLimit)
	}
}

func TestListQuery_WithCount(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), nil, filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListWithCount(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.CountSQL == "" {
		t.Error("expected CountSQL")
	}
	if !strings.Contains(result.CountSQL, "SELECT COUNT(*)") {
		t.Errorf("expected SELECT COUNT(*), got %q", result.CountSQL)
	}
	if len(result.CountArgs) != 1 {
		t.Errorf("expected 1 count arg, got %d", len(result.CountArgs))
	}
}

func testSearchSchema() *filter.SearchSchema {
	return &filter.SearchSchema{
		Fields: []filter.SearchField{
			{Name: "name", SQLExpr: "items.name", Weight: filter.SearchWeightA},
		},
		Language: "english",
	}
}

func TestListQuery_WithSearch(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), nil, filter.PostgresDialect{}).
		WithSearchSchema(testSearchSchema())
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListSearch("thermostat"),
		filter.ListWithCount(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	// WHERE composes the filter AND the full-text search predicate.
	if !strings.Contains(result.Where, "to_tsvector") || !strings.Contains(result.Where, "plainto_tsquery") {
		t.Errorf("expected FTS predicate in WHERE, got %q", result.Where)
	}
	// Args: 1 filter (status) + 1 search (query), search last.
	if len(result.Args) != 2 {
		t.Fatalf("expected 2 args (filter + search), got %d: %v", len(result.Args), result.Args)
	}
	if result.Args[1] != "thermostat" {
		t.Errorf("expected search query as last arg, got %v", result.Args[1])
	}
	// The count also folds in the search so the total matches the page set.
	if !strings.Contains(result.CountSQL, "to_tsvector") {
		t.Errorf("expected search in CountSQL, got %q", result.CountSQL)
	}
	if len(result.CountArgs) != 2 {
		t.Errorf("expected 2 count args (filter + search), got %d", len(result.CountArgs))
	}
}

func TestListQuery_SearchEmptyNoOp(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), nil, filter.PostgresDialect{}).
		WithSearchSchema(testSearchSchema())
	result, err := lq.Build(filter.ListSearch(""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Where, "to_tsvector") {
		t.Errorf("empty search must not add a predicate, got %q", result.Where)
	}
}

func TestListQuery_WithCursor(t *testing.T) {
	cursor, err := filter.EncodeCursor(&filter.PageToken{
		Fields: []filter.CursorField{{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lq := filter.NewListQuery(testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListCursor(cursor),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "items.created_at < $2") {
		t.Errorf("expected cursor condition, got %q", result.Where)
	}
}

func TestListQuery_PolicyConstraints(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), nil, filter.PostgresDialect{})
	pc := &filter.PolicyConstraints{
		Constraints: []*filter.PolicyConstraint{
			{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"},
		},
	}
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListPolicy(pc),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "items.tenant_id = $2") {
		t.Errorf("expected policy constraint, got %q", result.Where)
	}
}

func TestListQuery_Facets(t *testing.T) {
	schema := &filter.ResourceSchema{
		Name: "products",
		Fields: map[string]*filter.FieldSchema{
			"category": {
				ExternalName: "category", SQLExpr: "products.category",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
				Facetable:        true,
			},
		},
	}
	lq := filter.NewListQuery(schema, nil, filter.PostgresDialect{})
	result, err := lq.Build(filter.ListFacets("category"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Facets == nil {
		t.Fatal("expected a facet query")
	}
	if !strings.Contains(result.Facets.SQL, "GROUP BY") {
		t.Errorf("expected GROUP BY in facet SQL, got %q", result.Facets.SQL)
	}
}

// --- Cursor Tests ---

func TestCursorRoundTrip(t *testing.T) {
	token := &filter.PageToken{Fields: []filter.CursorField{
		{Name: "created_at", Value: "2026-01-01", Direction: filter.SortDesc},
		{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	}}
	encoded, err := filter.EncodeCursor(token)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := filter.DecodeCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Fields) != 2 || decoded.Fields[0].Name != "created_at" {
		t.Errorf("expected 2 fields led by created_at, got %+v", decoded.Fields)
	}
	if decoded.Fields[0].Direction != filter.SortDesc {
		t.Errorf("expected desc, got %s", decoded.Fields[0].Direction)
	}
}

func TestNewPage_HasMore(t *testing.T) {
	items := []string{"a", "b", "c"} // 3 items, limit=2 → has more
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "id", SQLExpr: "id", Column: "id", Direction: filter.SortAsc}}}
	page, err := filter.NewPage(items, 2, plan, func(s string, _ string) (any, error) { return s, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(page.Items))
	}
	if !page.PageInfo.HasNextPage {
		t.Error("expected HasNextPage=true")
	}
	if page.PageInfo.EndCursor.IsZero() {
		t.Error("expected non-empty EndCursor")
	}
}

// --- Schema Generation Tests ---

func TestGenerateFilterSchema(t *testing.T) {
	type Device struct {
		ID     string `json:"id"     parse:"filter" filter_ops:"eq,in" indexed:"true"`
		Name   string `json:"name"   parse:"filter" filter_ops:"eq,like" label:"Device Name" group:"basics" order:"1"`
		Status string `json:"status" parse:"filter,facet" filter_ops:"eq" facet:"terms" enum:"active,offline"`
	}
	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[Device](gen)
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != "devices" {
		t.Errorf("expected devices, got %s", schema.Name)
	}
	if _, ok := schema.Fields["id"]; !ok {
		t.Error("missing id field")
	}
	nameField := schema.Fields["name"]
	if nameField.Label != "Device Name" {
		t.Errorf("expected label 'Device Name', got %q", nameField.Label)
	}
	if nameField.Group != "basics" {
		t.Errorf("expected group 'basics', got %q", nameField.Group)
	}
	statusField := schema.Fields["status"]
	if !statusField.Facetable {
		t.Error("expected status to be facetable")
	}
	if len(statusField.EnumValues) != 2 {
		t.Errorf("expected 2 enums, got %d", len(statusField.EnumValues))
	}
}

func TestGenerateSortSchema(t *testing.T) {
	type Device struct {
		Name      string `json:"name" parse:"filter,sort" filter_ops:"eq"`
		CreatedAt string `json:"created_at" parse:"filter,sort" filter_ops:"gt" sort_default:"desc"`
	}
	gen := core.NewSchemaGenerator()
	ss, err := filter.GenerateSortSchema[Device](gen)
	if err != nil {
		t.Fatal(err)
	}
	if ss.DefaultField != "created_at" {
		t.Errorf("expected default field created_at, got %q", ss.DefaultField)
	}
	if ss.DefaultDir != filter.SortDesc {
		t.Errorf("expected desc, got %s", ss.DefaultDir)
	}
}

func TestGenerateSearchSchema(t *testing.T) {
	type Device struct {
		Name        string `json:"name" parse:"filter,search" search_weight:"A"`
		Description string `json:"description" parse:"search" search_weight:"B"`
	}
	gen := core.NewSchemaGenerator()
	ss, err := filter.GenerateSearchSchema[Device](gen)
	if err != nil {
		t.Fatal(err)
	}
	if ss == nil {
		t.Fatal("expected non-nil SearchSchema")
	}
	if len(ss.Fields) != 2 {
		t.Errorf("expected 2 search fields, got %d", len(ss.Fields))
	}
}

// Only text is searchable: a JSON document tagged search would compile to
// to_tsvector over jsonb, which PostgreSQL refuses at query time.
func TestGenerateSearchSchema_RefusesNonText(t *testing.T) {
	type Device struct {
		Attrs map[string]any `json:"attrs" parse:"search"`
	}
	_, err := filter.GenerateSearchSchema[Device](core.NewSchemaGenerator())
	if err == nil || !strings.Contains(err.Error(), "only string fields are searchable") {
		t.Fatalf("expected a schema error, got %v", err)
	}
}

// --- WithStartParam Tests ---

func TestSQLBuilder_WithStartParam(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":"active","count":{"$gt":10}}`)
	if err != nil {
		t.Fatal(err)
	}

	// Without offset: params start at $1.
	b0 := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	r0, err := b0.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r0.Where, "$1") {
		t.Errorf("expected $1 without offset, got %q", r0.Where)
	}
	if r0.ParamCount != 2 {
		t.Errorf("expected ParamCount=2, got %d", r0.ParamCount)
	}

	// With offset=5: params start at $6.
	b5 := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{}, filter.WithStartParam(5))
	r5, err := b5.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r5.Where, "$1") || strings.Contains(r5.Where, "$5") {
		t.Errorf("should not contain $1-$5 with offset=5, got %q", r5.Where)
	}
	if !strings.Contains(r5.Where, "$6") {
		t.Errorf("expected $6 with offset=5, got %q", r5.Where)
	}
	if r5.ParamCount != 2 {
		t.Errorf("expected ParamCount=2 with offset, got %d", r5.ParamCount)
	}
	if len(r5.Args) != 2 {
		t.Errorf("args should still be 2, got %d", len(r5.Args))
	}
}

func TestSQLBuilder_WithStartParam_SQLite(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	b := filter.NewSQLBuilder(testSchema(), filter.SQLiteDialect{}, filter.WithStartParam(3))
	result, err := b.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "?4") {
		t.Errorf("expected ?4 with offset=3, got %q", result.Where)
	}
}

func TestSQLBuilder_WithStartParam_Zero(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit zero should behave same as default.
	b := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{}, filter.WithStartParam(0))
	result, err := b.Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "$1") {
		t.Errorf("expected $1 with zero offset, got %q", result.Where)
	}
}

func TestSQLBuilder_ParamCount_Empty(t *testing.T) {
	b := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	result, err := b.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ParamCount != 0 {
		t.Errorf("expected ParamCount=0 for nil AST, got %d", result.ParamCount)
	}
}

// --- ListStartParam Tests ---

func TestListQuery_WithStartParam(t *testing.T) {
	lq := filter.NewListQuery(testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListStartParam(3),
		filter.ListLimit(10),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Filter param should start at $4 (offset 3 + 1).
	if !strings.Contains(result.Where, "$4") {
		t.Errorf("expected $4 with startParam=3, got %q", result.Where)
	}
	if strings.Contains(result.Where, "$1") {
		t.Errorf("should not contain $1 with startParam=3, got %q", result.Where)
	}
}

// --- QueryParamParser strictness (GEN-05) ---

// A parameter naming no field is E2071 unless it is a list parameter or one
// the caller accepts; QueryParamLenient ignores it instead.
func TestQueryParamParser_RefusesUnknownParameters(t *testing.T) {
	accept := []filter.QueryParamOption{filter.QueryParamAccept("debug")}
	for _, tc := range []struct {
		name    string
		opts    []filter.QueryParamOption
		params  url.Values
		code    string // empty: the parameters parse
		filters bool
	}{
		{"a typo", nil, url.Values{"nam": {"x"}}, "E2071", false},
		{"a typo with an operator suffix", nil, url.Values{"cont_gt": {"1"}}, "E2071", false},
		{"a field", nil, url.Values{"status": {"active"}}, "", true},
		{"list parameters", nil, url.Values{"sort": {"-name"}, "page_size": {"10"}, "search": {"x"}, "fields": {"id"}}, "", false},
		{"an accepted name", accept, url.Values{"debug": {"1"}, "status": {"active"}}, "", true},
		{"a typo beside an accepted name", accept, url.Values{"debug": {"1"}, "nam": {"x"}}, "E2071", false},
		{"lenient", []filter.QueryParamOption{filter.QueryParamLenient()}, url.Values{"nam": {"x"}, "status": {"active"}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := filter.NewQueryParamParser(testSchema(), tc.opts...).Parse(tc.params)
			if tc.code != "" {
				var pe *core.ParseError
				require.ErrorAs(t, err, &pe)
				assert.Equal(t, tc.code, pe.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.filters, ast.Root != nil)
		})
	}
}

// --- Integer Validation Tests ---

func TestFilterParser_IntRejectsFloat(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	_, err := fp.Parse(`{"count":{"$gt":1.5}}`)
	if err == nil {
		t.Fatal("expected error for non-integer float value on int field")
	}
	if !strings.Contains(err.Error(), "integer") {
		t.Errorf("error should mention integer, got: %s", err.Error())
	}
}

func TestFilterParser_IntAcceptsWholeFloat(t *testing.T) {
	fp := filter.NewFilterParser(testSchema(), nil)
	ast, err := fp.Parse(`{"count":{"$gt":10}}`)
	if err != nil {
		t.Fatalf("whole number should be accepted for int field: %v", err)
	}
	if ast.Root == nil {
		t.Fatal("expected non-nil root")
	}
}

// --- Immutable Tag Tests ---

func TestGenerateFilterSchema_Immutable(t *testing.T) {
	type Entity struct {
		ID   string `json:"id"   parse:"filter" filter_ops:"eq" immutable:"true"`
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[Entity](gen)
	if err != nil {
		t.Fatal(err)
	}
	if !schema.Fields["id"].Immutable {
		t.Error("expected id to be immutable")
	}
	if schema.Fields["name"].Immutable {
		t.Error("expected name to NOT be immutable")
	}
}
