//go:build unit

package filter_test

// Hot-path benchmarks: filter parsing, SQL building, list composition,
// cursors. Each fails on a setup or per-iteration error, so a green run
// measures the intended work, never an error path.
//
//	go test -tags=unit -run '^$' -bench . ./filter/

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type benchItem struct {
	ID        string         `json:"id"         db:"id"         parse:"filter,sort" filter_ops:"eq,in" indexed:"true"`
	Status    string         `json:"status"     db:"status"     parse:"filter,facet" filter_ops:"eq,ne,in" enum:"active,inactive,suspended"`
	Name      string         `json:"name"       db:"name"       parse:"filter,sort" filter_ops:"eq,like,ilike,contains"`
	Count     int64          `json:"count"      db:"count"      parse:"filter" filter_ops:"eq,gt,gte,lt,lte"`
	Price     float64        `json:"price"      db:"price"      parse:"filter" filter_ops:"gte,lte"`
	Labels    map[string]any `json:"labels"     db:"labels"     parse:"filter"`
	CreatedAt time.Time      `json:"created_at" db:"created_at" parse:"filter,sort" filter_ops:"gt,gte,lt,lte" indexed:"true" sort_default:"desc"`
	TenantID  string         `json:"-"          db:"tenant_id"`
}

func benchSchemas(b *testing.B) (*filter.ResourceSchema, *filter.SortSchema) {
	b.Helper()
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[benchItem](g)
	if err != nil {
		b.Fatal(err)
	}
	fs.PrimaryKey = "id"
	fs.SystemFields = map[string]*filter.FieldSchema{
		"tenant_id": {ExternalName: "tenant_id", SQLExpr: fs.Name + ".tenant_id", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}},
	}
	ss, err := filter.GenerateSortSchema[benchItem](g)
	if err != nil {
		b.Fatal(err)
	}
	return fs, ss
}

// benchConfig admits the complex workloads: the benchmarks time parsing
// and building, not the cost policy that would reject them by default.
func benchConfig() *core.ParserConfig {
	cfg := core.DefaultConfig()
	cfg.MaxCost = 1000
	return cfg
}

func benchParse(b *testing.B, fs *filter.ResourceSchema, input string) *filter.FilterAST {
	b.Helper()
	ast, err := filter.NewFilterParser(fs, benchConfig()).Parse(input)
	if err != nil || ast.Root == nil {
		b.Fatalf("parse %s: %v", input, err)
	}
	return ast
}

func benchFilterParse(b *testing.B, input string) {
	fs, _ := benchSchemas(b)
	fp := filter.NewFilterParser(fs, benchConfig())
	benchParse(b, fs, input)
	for b.Loop() {
		if _, err := fp.Parse(input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFilterParser_SimpleEq(b *testing.B) {
	benchFilterParse(b, `{"status":"active"}`)
}

func BenchmarkFilterParser_MultiCondition(b *testing.B) {
	benchFilterParse(b, `{"status":"active","count":{"$gt":10},"price":{"$gte":50,"$lte":200}}`)
}

func BenchmarkFilterParser_OrComplex(b *testing.B) {
	benchFilterParse(b, `{"$or":[{"status":"active","count":{"$gt":10}},{"status":"inactive","price":{"$lte":100}},{"name":{"$like":"test%"}}]}`)
}

func BenchmarkFilterParser_InArray(b *testing.B) {
	benchFilterParse(b, `{"status":{"$in":["active","inactive","suspended"]}}`)
}

func BenchmarkFilterParser_JSONContains(b *testing.B) {
	benchFilterParse(b, `{"labels":{"$contains":{"env":"prod","tier":{"level":2}}}}`)
}

func benchBuild(b *testing.B, d filter.Dialect, input string, wantArgs int) {
	fs, _ := benchSchemas(b)
	ast := benchParse(b, fs, input)
	builder := filter.NewSQLBuilder(fs, d)
	res, err := builder.Build(ast)
	if err != nil || len(res.Args) != wantArgs {
		b.Fatalf("build %s: %d args, err %v; want %d args", input, len(res.Args), err, wantArgs)
	}
	for b.Loop() {
		if _, err := builder.Build(ast); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLBuilder_SimpleEq(b *testing.B) {
	benchBuild(b, filter.PostgresDialect{}, `{"status":"active"}`, 1)
}

func BenchmarkSQLBuilder_MultiCondition(b *testing.B) {
	benchBuild(b, filter.PostgresDialect{}, `{"status":"active","count":{"$gt":10},"price":{"$gte":50,"$lte":200}}`, 4)
}

func BenchmarkSQLBuilder_OrComplex(b *testing.B) {
	benchBuild(b, filter.PostgresDialect{}, `{"$or":[{"status":"active","count":{"$gt":10}},{"status":"inactive"},{"name":{"$like":"test%"}}]}`, 4)
}

func BenchmarkSQLBuilder_SQLiteDialect(b *testing.B) {
	benchBuild(b, filter.SQLiteDialect{}, `{"status":"active","name":{"$ilike":"test%"}}`, 2)
}

func BenchmarkSQLBuilder_SQLiteJSONContains(b *testing.B) {
	benchBuild(b, filter.SQLiteDialect{}, `{"labels":{"$contains":{"env":"prod","tier":{"level":2}}}}`, 5)
}

func benchQueryParams(b *testing.B, params url.Values) {
	fs, _ := benchSchemas(b)
	qp := filter.NewQueryParamParser(fs)
	if ast, err := qp.Parse(params); err != nil || ast.Root == nil {
		b.Fatalf("query params: %v", err)
	}
	for b.Loop() {
		if _, err := qp.Parse(params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQueryParamParser_Simple(b *testing.B) {
	benchQueryParams(b, url.Values{"status": {"active"}, "count_gt": {"10"}})
}

func BenchmarkQueryParamParser_Complex(b *testing.B) {
	benchQueryParams(b, url.Values{
		"status":    {"active"},
		"count_gt":  {"10"},
		"count_lte": {"100"},
		"price_gte": {"50"},
		"price_lte": {"200"},
	})
}

func benchList(b *testing.B, check func(*filter.ListResult) bool, opts ...filter.ListOption) {
	fs, ss := benchSchemas(b)
	lq := filter.NewListQuery(fs, ss, filter.PostgresDialect{})
	res, err := lq.Build(opts...)
	if err != nil || !check(res) {
		b.Fatalf("list build: %+v, %v", res, err)
	}
	for b.Loop() {
		if _, err := lq.Build(opts...); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListQuery_FilterSortLimit(b *testing.B) {
	benchList(b, func(r *filter.ListResult) bool { return strings.Contains(r.Where, "status") && r.OrderBy != "" },
		filter.ListFilter(`{"status":"active"}`), filter.ListSort("-created_at"), filter.ListLimit(25))
}

func BenchmarkListQuery_WithCountAndFacets(b *testing.B) {
	benchList(b, func(r *filter.ListResult) bool { return r.CountSQL != "" && r.Facets != nil },
		filter.ListFilter(`{"status":"active"}`), filter.ListSort("-created_at"), filter.ListLimit(25),
		filter.ListWithCount(true), filter.ListFacets("status"))
}

func BenchmarkListQuery_WithCursor(b *testing.B) {
	fs, ss := benchSchemas(b)
	lq := filter.NewListQuery(fs, ss, filter.PostgresDialect{})
	first, err := lq.Build(filter.ListSort("-created_at"), filter.ListLimit(25))
	if err != nil {
		b.Fatal(err)
	}
	row := benchItem{ID: "0198a3c2-7a1e-7c00-8000-000000000001", CreatedAt: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	token, err := first.Cursor.Token(false, func(col string) (any, error) { return filter.CursorValueByTag(row, "db", col) })
	if err != nil {
		b.Fatal(err)
	}
	cursor, err := filter.EncodeCursor(token)
	if err != nil {
		b.Fatal(err)
	}
	benchList(b, func(r *filter.ListResult) bool { return len(r.Args) >= 2 },
		filter.ListFilter(`{"status":"active"}`), filter.ListSort("-created_at"), filter.ListLimit(25),
		filter.ListCursor(cursor))
}

func BenchmarkListQuery_WithPolicy(b *testing.B) {
	pc := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"}}}
	benchList(b, func(r *filter.ListResult) bool { return strings.Contains(r.Where, "tenant_id") },
		filter.ListFilter(`{"status":"active"}`), filter.ListPolicy(pc), filter.ListLimit(25))
}

func BenchmarkParseSort(b *testing.B) {
	_, ss := benchSchemas(b)
	if sr, err := filter.ParseSort(ss, "name,-created_at"); err != nil || len(sr.Fields) != 2 {
		b.Fatalf("sort: %v", err)
	}
	for b.Loop() {
		if _, err := filter.ParseSort(ss, "name,-created_at"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateFilterSchema(b *testing.B) {
	g := core.NewSchemaGenerator()
	for b.Loop() {
		if _, err := filter.GenerateFilterSchema[benchItem](g); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCursorRoundTrip(b *testing.B) {
	token := &filter.PageToken{Fields: []filter.CursorField{
		{Name: "created_at", Value: "2026-01-01T00:00:00Z", Direction: filter.SortDesc},
		{Name: "id", Value: int64(9007199254740995), Direction: filter.SortDesc},
	}}
	for b.Loop() {
		encoded, err := filter.EncodeCursor(token)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := filter.DecodeCursor(encoded); err != nil {
			b.Fatal(err)
		}
	}
}
