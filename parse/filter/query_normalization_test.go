//go:build unit

package filter_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// TestJSONDefaultOpsAndNinSymmetry verifies a bare
// parse:"filter" JSON field is filterable with a portable op set, and every
// type that admits $in also admits $nin.
func TestJSONDefaultOpsAndNinSymmetry(t *testing.T) {
	type Doc struct {
		Meta  map[string]any `json:"meta"  parse:"filter"`
		Name  string         `json:"name"  parse:"filter"`
		Count int            `json:"count" parse:"filter"`
	}
	schema, err := filter.GenerateFilterSchema[Doc](core.NewSchemaGenerator())
	if err != nil {
		t.Fatalf("GenerateFilterSchema: %v", err)
	}

	meta := schema.Fields["meta"]
	if meta == nil {
		t.Fatal("expected meta field in schema")
	}
	// Was silently empty (unfilterable) before P-13o; containment is
	// portable since .
	for _, want := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpContains, filter.OpExists, filter.OpNull} {
		if !slices.Contains(meta.AllowedOperators, want) {
			t.Errorf("JSON field missing default op %v; got %v", want, meta.AllowedOperators)
		}
	}

	// $in / $nin symmetry.
	for _, name := range []string{"name", "count"} {
		f := schema.Fields[name]
		if f == nil {
			t.Fatalf("expected %s field", name)
		}
		if slices.Contains(f.AllowedOperators, filter.OpIn) && !slices.Contains(f.AllowedOperators, filter.OpNin) {
			t.Errorf("%s allows $in but not $nin: %v", name, f.AllowedOperators)
		}
	}
}

// A programmatic AST bypasses parse-time validation; a box with swapped
// corners is rejected at build rather than reordered — reordering longitudes
// would turn an antimeridian-crossing intent into its complement.
func TestGeo_ProgrammaticInvalidBoxRejected(t *testing.T) {
	schema := &filter.ResourceSchema{
		Name: "sites",
		Fields: map[string]*filter.FieldSchema{
			"location": {
				ExternalName: "location", SQLExpr: "sites.location", DataType: core.FieldTypeGeo,
				AllowedOperators: []filter.FilterOperator{filter.OpWithin},
				GeoConfig: &filter.GeoFieldConfig{
					StorageType: filter.GeoStorageJSONB, SRID: 4326, LatKey: "lat", LonKey: "lon",
				},
			},
		},
	}
	inverted := &filter.FilterAST{Root: &filter.FilterNode{
		Field: schema.Fields["location"], Op: filter.OpWithin,
		Value: &filter.GeoBoundingBox{
			TopLeft:     filter.GeoPoint{Lat: 40.7, Lon: -73.9},
			BottomRight: filter.GeoPoint{Lat: 40.8, Lon: -74.1},
		},
	}}
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		_, err := filter.NewSQLBuilder(schema, d).Build(inverted)
		var pe *core.ParseError
		if !errors.As(err, &pe) || pe.Code != filter.ECodeInvalidGeoCoords.String() {
			t.Errorf("%s: err = %v, want %s", d.Name(), err, filter.ECodeInvalidGeoCoords.String())
		}
	}
}

// TestDeterministicSQLText: map iteration in parseNode /
// parseOperatorObject produced unstable SQL text across runs
// (pg_stat_statements bloat, flaky snapshots). 50 parses of the same filter
// must build byte-identical SQL.
func TestDeterministicSQLText(t *testing.T) {
	schema := testSchema()
	input := `{"status": "active", "name": {"$like": "%x%"}, "count": {"$gte": 1, "$lte": 10}}`

	cfg := core.DefaultConfig()
	cfg.MaxCost = 1000

	var first string
	for i := range 50 {
		p := filter.NewFilterParser(schema, cfg)
		ast, err := p.Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = res.Where
			continue
		}
		if res.Where != first {
			t.Fatalf("iteration %d: SQL text diverged:\n  %s\nvs\n  %s", i, res.Where, first)
		}
	}
}

// TestRepeatedQueryParamsBecomeIn: ?tag=a&tag=b silently dropped b.
func TestRepeatedQueryParamsBecomeIn(t *testing.T) {
	p := filter.NewQueryParamParser(testSchema())
	ast, err := p.Parse(url.Values{"status": {"active", "inactive"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{}).Build(ast)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Args) != 2 {
		t.Fatalf("args = %v; want both values bound", res.Args)
	}
	if !strings.Contains(res.Where, "IN") && !strings.Contains(res.Where, "= ANY") {
		t.Errorf("WHERE %q; want IN/ANY over both values", res.Where)
	}
}

// TestFieldNamedLikeSuffixNotSplit: the suffix grammar collided with
// real fields named *_like / *_gt etc.
func TestFieldNamedLikeSuffixNotSplit(t *testing.T) {
	schema := &filter.ResourceSchema{
		Name: "items",
		Fields: map[string]*filter.FieldSchema{
			"email_like": {
				ExternalName: "email_like", SQLExpr: "items.email_like", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
			},
		},
	}
	p := filter.NewQueryParamParser(schema)
	ast, err := p.Parse(url.Values{"email_like": {"x@y.z"}})
	if err != nil {
		t.Fatalf("exact field name split as suffix: %v", err)
	}
	if ast.Root == nil {
		t.Fatal("no condition produced")
	}
	if ast.Root.Op != filter.OpEq {
		t.Errorf("op = %s; want eq (exact field match takes precedence over _like suffix)", ast.Root.Op)
	}
}

// TestSearchFallbackEscapesLike: LIKE fallback didn't escape % / _ in
// the user query — "50%" matched as a wildcard.
func TestSearchFallbackEscapesLike(t *testing.T) {
	schema := &filter.SearchSchema{
		Fields: []filter.SearchField{{Name: "name", SQLExpr: "items.name", Weight: filter.SearchWeightA}},
	}
	where, args, err := filter.AndSearch(schema, filter.SQLiteDialect{}, "TRUE", nil, 0, "50%_x")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v; want one search operand", args)
	}
	arg, ok := args[0].(string)
	if !ok {
		t.Fatalf("arg = %T", args[0])
	}
	if !strings.Contains(arg, `\%`) || !strings.Contains(arg, `\_`) {
		t.Errorf("arg %q; want %% and _ escaped", arg)
	}
	if !strings.Contains(where, "ESCAPE") {
		t.Errorf("WHERE %q; want ESCAPE clause", where)
	}
}

// TestSearchSchemaPtrTimeDetection: the search generator didn't
// pointer-strip for time detection (the filter generator does) — a
// *time.Time search field reached the ColumnMapper with kind "ptr" instead
// of FieldTypeTimestamp.
func TestSearchSchemaPtrTimeDetection(t *testing.T) {
	type withTime struct {
		SeenAt time.Time `json:"seen_at" parse:"search"`
	}
	type withPtrTime struct {
		SeenAt *time.Time `json:"seen_at" parse:"search"`
	}
	rec := &recordingMapper{}
	gen := core.NewSchemaGenerator(core.WithColumnMapper(rec))
	if _, err := filter.GenerateSearchSchema[withTime](gen); err != nil {
		t.Fatal(err)
	}
	if _, err := filter.GenerateSearchSchema[withPtrTime](gen); err != nil {
		t.Fatal(err)
	}
	if len(rec.types) != 2 {
		t.Fatalf("mapper called %d times; want 2", len(rec.types))
	}
	if rec.types[0] != rec.types[1] {
		t.Errorf("field type diverges: time.Time=%q *time.Time=%q (ptr not stripped)", rec.types[0], rec.types[1])
	}
	if rec.types[0] != core.FieldTypeTimestamp {
		t.Errorf("time.Time mapped as %q; want %q", rec.types[0], core.FieldTypeTimestamp)
	}
}

type recordingMapper struct{ types []core.FieldType }

func (r *recordingMapper) MapColumn(table, field string, ft core.FieldType) string {
	r.types = append(r.types, ft)
	return table + "." + field
}

// TestEnumTagValuesTrimmed: enum:"a, b" admitted " b" and rejected "b".
func TestEnumTagValuesTrimmed(t *testing.T) {
	type withEnum struct {
		Status string `json:"status" parse:"filter" filter_ops:"eq" enum:"active, inactive"`
	}
	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[withEnum](gen)
	if err != nil {
		t.Fatal(err)
	}
	p := filter.NewFilterParser(schema, nil)
	if _, err := p.Parse(`{"status": "inactive"}`); err != nil {
		t.Errorf("trimmed enum value rejected: %v", err)
	}
	if _, err := p.Parse(`{"status": " inactive"}`); err == nil {
		t.Error("space-prefixed value accepted; want rejection")
	}
}

// TestErrorIndexZeroSurvivesWire: op index 0 vanished from wire errors
// via omitempty.
func TestErrorIndexZeroSurvivesWire(t *testing.T) {
	pe := &core.ParseError{
		Kind: core.ParseErrorKindPatch, Code: "E2077", Message: "x",
		Index: new(0),
	}
	b, err := json.Marshal(pe)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"index":0`) {
		t.Errorf("wire %s; want index:0 present", b)
	}

	noIdx := &core.ParseError{Kind: core.ParseErrorKindFilter, Code: "E2086", Message: "y"}
	b2, _ := json.Marshal(noIdx)
	if strings.Contains(string(b2), `"index"`) {
		t.Errorf("wire %s; want index absent when unset", b2)
	}
}

// The search language is interpolated into to_tsvector('…'); only a text
// search configuration name is accepted.
func TestSearchLanguageMustBeIdentifier(t *testing.T) {
	schema := &filter.SearchSchema{
		Fields:   []filter.SearchField{{Name: "name", SQLExpr: "items.name"}},
		Language: "english'); DROP TABLE items; --",
	}
	if _, _, err := filter.AndSearch(schema, filter.PostgresDialect{}, "TRUE", nil, 0, "q"); err == nil {
		t.Fatal("expected an error for a non-identifier language")
	}
	schema.Language = "pg_catalog.english"
	if _, _, err := filter.AndSearch(schema, filter.PostgresDialect{}, "TRUE", nil, 0, "q"); err != nil {
		t.Fatalf("qualified configuration name rejected: %v", err)
	}
}
