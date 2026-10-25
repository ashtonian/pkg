//go:build unit

package parse_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type TestDevice struct {
	ID     string `json:"id"     parse:"filter"       filter_ops:"eq,in" indexed:"true"`
	Name   string `json:"name"   parse:"filter,patch" filter_ops:"eq,like" patch_ops:"replace"`
	Status string `json:"status" parse:"filter,patch" filter_ops:"eq,ne" patch_ops:"replace" enum:"active,offline"`
}

func TestResource_ParsingSchemas(t *testing.T) {
	tp, err := parse.NewResource[TestDevice]("test_devices")
	if err != nil {
		t.Fatal(err)
	}
	if tp.FilterSchema() == nil {
		t.Error("expected filter schema")
	}
	if tp.PatchSchema() == nil {
		t.Error("expected patch schema")
	}
	if tp.SortSchema() == nil {
		t.Error("expected sort schema")
	}
}

func TestResource_ParseFilter(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	result, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount != 1 {
		t.Errorf("expected 1 condition, got %d", result.ConditionCount)
	}
}

func TestResource_ParseFilter_InvalidEnum(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	_, err := tp.ParseFilter(`{"status":"invalid"}`)
	if err == nil {
		t.Fatal("expected error for invalid enum")
	}
}

func TestResource_ParseFilterMap(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	result, err := tp.ParseFilterMap(map[string]any{"status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount != 1 {
		t.Errorf("expected 1 condition, got %d", result.ConditionCount)
	}
}

func TestResource_ParseFilterMap_Empty(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	result, err := tp.ParseFilterMap(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConditionCount != 0 {
		t.Errorf("expected empty parsed filter, got %d conditions", result.ConditionCount)
	}
}

func TestResource_ParseFilterMap_InvalidEnum(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	_, err := tp.ParseFilterMap(map[string]any{"status": "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

// TestResource_ParseFilterMap_EquivalentToParseFilter sanity-checks that
// the map and string paths produce structurally identical ParsedFilter
// values for the same input.
func TestResource_ParseFilterMap_EquivalentToParseFilter(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	fromString, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		t.Fatal(err)
	}
	fromMap, err := tp.ParseFilterMap(map[string]any{"status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if fromString.ConditionCount != fromMap.ConditionCount {
		t.Errorf("condition count mismatch: string=%d map=%d",
			fromString.ConditionCount, fromMap.ConditionCount)
	}
}

func TestResource_ParsePatch(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	result, err := tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"new"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 {
		t.Errorf("expected 1 op, got %d", len(result.Operations))
	}
}

func TestResource_ParsePatch_UnknownPath(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	_, err := tp.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":"x"}]`))
	if err == nil {
		t.Fatal("expected error for field not in patch schema")
	}
}

func TestResource_ApplyPatch(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"New"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New" {
		t.Errorf("expected New, got %s", updated.Name)
	}
	if updated.ID != "d1" {
		t.Errorf("ID should be unchanged")
	}
}

func TestResource_ListQuery(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	lq := tp.ListQuery(filter.PostgresDialect{})
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

func TestResource_Conventions(t *testing.T) {
	type Item struct {
		ID   string `bson:"id"   parse:"filter" filter_ops:"eq" sql:"item_id"`
		Name string `bson:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace" sql:"item_name"`
	}

	tp, err := parse.NewResource[Item]("item", parse.PrimaryKey("item_id"), parse.ParserOptions(core.WithGeneratorOptions(
		core.WithFieldNameTag("bson"),
		core.WithColumnTag("sql"),
	)))
	if err != nil {
		t.Fatal(err)
	}

	if tp.FilterSchema().Name != "item" {
		t.Errorf("expected table 'item', got %q", tp.FilterSchema().Name)
	}
	if tp.FilterSchema().Fields["id"].SQLExpr != "item.item_id" {
		t.Errorf("expected item.item_id, got %q", tp.FilterSchema().Fields["id"].SQLExpr)
	}
	if tp.PatchSchema().AllowedPaths["/name"].SQLColumn != "item_name" {
		t.Errorf("expected item_name, got %q", tp.PatchSchema().AllowedPaths["/name"].SQLColumn)
	}
}

func TestSchemaRegistry_Register(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[TestDevice](reg)

	fs, ok := reg.FilterSchema("test_devices")
	if !ok {
		t.Fatal("expected filter schema")
	}
	if _, found := fs.Fields["status"]; !found {
		t.Error("expected status field")
	}

	ps, ok := reg.PatchSchema("test_devices")
	if !ok {
		t.Fatal("expected patch schema")
	}
	if _, ok := ps.AllowedPaths["/name"]; !ok {
		t.Error("expected /name path")
	}
}

// TestResourceNamesSorted verifies the registry returns resource names in
// a deterministic (sorted) order rather than Go's random map-iteration order,
// so a schema-index API listing is stable.
func TestResourceNamesSorted(t *testing.T) {
	type Zebra struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Alpha struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Mango struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Delta struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Zebra](reg)
	parse.MustRegister[Alpha](reg)
	parse.MustRegister[Mango](reg)
	parse.MustRegister[Delta](reg)
	names := reg.ResourceNames()
	if !slices.IsSorted(names) {
		t.Errorf("ResourceNames not sorted: %v", names)
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

func TestSchemaRegistry_SchemaFor(t *testing.T) {
	type Product struct {
		Name     string `json:"name"     parse:"filter,patch,search" filter_ops:"eq" patch_ops:"replace" label:"Name"`
		Category string `json:"category" parse:"filter,facet" filter_ops:"eq" facet:"terms" enum:"a,b"`
	}

	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Product](reg)

	resp := reg.SchemaFor("products")
	if resp == nil {
		t.Fatal("expected schema")
	}
	if resp.Filter == nil {
		t.Error("expected filter")
	}
	if resp.Patch == nil {
		t.Error("expected patch")
	}
	if resp.Search == nil {
		t.Error("expected search")
	}
	if resp.Filter.Fields["name"].Label != "Name" {
		t.Error("expected label")
	}
	if !resp.Filter.Fields["category"].Facetable {
		t.Error("expected facetable")
	}
}

func TestSchemaRegistry_NotFound(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	if reg.SchemaFor("nope") != nil {
		t.Error("expected nil for unregistered resource")
	}
}

// --- ApplyValidatedPatch fast path vs slow path ---

func TestApplyPatch_DirectFastPath(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}

	// Merge patch — all top-level replaces → uses direct reflection path.
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"New"},{"op":"replace","path":"/status","value":"offline"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New" {
		t.Errorf("expected New, got %s", updated.Name)
	}
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}
	if updated.ID != "d1" {
		t.Error("ID should be unchanged")
	}
}

func TestApplyPatch_MergePatchFastPath(t *testing.T) {
	tp := parse.MustResource[TestDevice]("test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}

	parsed, err := tp.ParseMergePatch([]byte(`{"name":"Merged"}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tp.ApplyValidatedPatch(parsed, original)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Merged" {
		t.Errorf("expected Merged, got %s", updated.Name)
	}
	if updated.ID != "d1" {
		t.Error("ID should be unchanged")
	}
	if updated.Status != "active" {
		t.Error("Status should be unchanged")
	}
}

type TestDeviceMovable struct {
	ID   string  `json:"id"   parse:"filter" filter_ops:"eq"`
	Name *string `json:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace,remove,move,copy"`
	Alt  *string `json:"alt"  parse:"patch" patch_ops:"replace,remove,move,copy"`
}

func TestApplyPatch_SlowPathForMoveOp(t *testing.T) {
	tp := parse.MustResource[TestDeviceMovable]("test_device_movables")
	name := "Original"
	original := TestDeviceMovable{ID: "d1", Name: &name}

	// move op → triggers slow path (JSON round-trip).
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"move","from":"/name","path":"/alt"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Alt == nil || *updated.Alt != "Original" {
		t.Errorf("expected Original in alt, got %v", updated.Alt)
	}
	if updated.Name != nil {
		t.Errorf("expected no name after move, got %q", *updated.Name)
	}
}

// --- Immutable in SchemaResponse ---

func TestSchemaRegistry_ImmutableField(t *testing.T) {
	type Entity struct {
		ID   string `json:"id"   parse:"filter" filter_ops:"eq" immutable:"true"`
		Name string `json:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace"`
	}
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Entity](reg)
	resp := reg.SchemaFor("entitys")
	if resp == nil {
		// Try with proper plural.
		resp = reg.SchemaFor("entities")
	}
	if resp == nil {
		t.Fatal("expected schema response")
	}
	idField := resp.Filter.Fields["id"]
	if idField == nil {
		t.Fatal("expected id field in filter schema")
	}
	if !idField.Immutable {
		t.Error("expected id to be immutable in schema response")
	}
	nameField := resp.Filter.Fields["name"]
	if nameField == nil {
		t.Fatal("expected name field in filter schema")
	}
	if nameField.Immutable {
		t.Error("expected name to NOT be immutable")
	}
}

// --- Benchmarks ---

func BenchmarkApplyPatch_DirectPath(b *testing.B) {
	tp, err := parse.NewResource[TestDevice]("test_devices")
	if err != nil {
		b.Fatal(err)
	}
	parsed, err := tp.ParseMergePatch([]byte(`{"name":"Updated","status":"offline"}`))
	if err != nil {
		b.Fatal(err)
	}
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}
	if got, err := tp.ApplyValidatedPatch(parsed, original); err != nil || got.Name != "Updated" {
		b.Fatalf("apply: %+v, %v", got, err)
	}
	for b.Loop() {
		if _, err := tp.ApplyValidatedPatch(parsed, original); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyPatch_JSONPath(b *testing.B) {
	tp, err := parse.NewResource[TestDeviceMovable]("test_device_movables")
	if err != nil {
		b.Fatal(err)
	}
	parsed, err := tp.ParsePatch([]byte(`[{"op":"move","from":"/name","path":"/alt"}]`))
	if err != nil {
		b.Fatal(err)
	}
	name := "Original"
	original := TestDeviceMovable{ID: "d1", Name: &name}
	if got, err := tp.ApplyValidatedPatch(parsed, original); err != nil || got.Alt == nil || *got.Alt != "Original" {
		b.Fatalf("apply: %+v, %v", got, err)
	}
	for b.Loop() {
		if _, err := tp.ApplyValidatedPatch(parsed, original); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildPatchSQL_WithStartParam(b *testing.B) {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/name", Value: "new", Schema: &patch.PathSchema{SQLColumn: "name", DataType: core.FieldTypeString}},
			{Op: patch.PatchOpReplace, Path: "/status", Value: "active", Schema: &patch.PathSchema{SQLColumn: "status", DataType: core.FieldTypeString}},
		},
	}
	res, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{}, patch.WithStartParam(4))
	if err != nil || res.SetClause != "name = $5, status = $6" || res.ParamCount != 2 {
		b.Fatalf("build: %+v, %v", res, err)
	}
	for b.Loop() {
		if _, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{}, patch.WithStartParam(4)); err != nil {
			b.Fatal(err)
		}
	}
}

type TestDeviceTimestamped struct {
	ID        string     `json:"id"         parse:"filter"       filter_ops:"eq"`
	Name      string     `json:"name"       parse:"filter,patch" filter_ops:"eq" patch_ops:"replace"`
	RevokedAt *time.Time `json:"revoked_at" parse:"filter,patch" patch_ops:"replace"`
	SeenAt    time.Time  `json:"seen_at"    parse:"filter,patch" patch_ops:"replace"`
}

// The direct-apply fast path must coerce JSON RFC 3339 strings into
// time.Time / *time.Time targets — this is how every timestamp column
// arrives from a merge-patch body, and the memory repositories apply
// patches through this path (SQL backends coerce in the database).
func TestApplyPatch_DirectPathCoercesRFC3339Timestamps(t *testing.T) {
	tp, err := parse.NewResource[TestDeviceTimestamped]("test_device_timestampeds")
	if err != nil {
		t.Fatal(err)
	}
	original := TestDeviceTimestamped{ID: "d1", Name: "Old"}

	parsed, err := tp.ParseMergePatch([]byte(`{"revoked_at":"2026-07-09T12:34:56.789Z","seen_at":"2026-07-09T01:02:03Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tp.ApplyValidatedPatch(parsed, original)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RevokedAt == nil || !updated.RevokedAt.Equal(time.Date(2026, 7, 9, 12, 34, 56, 789_000_000, time.UTC)) {
		t.Errorf("RevokedAt not coerced: %v", updated.RevokedAt)
	}
	if !updated.SeenAt.Equal(time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC)) {
		t.Errorf("SeenAt not coerced: %v", updated.SeenAt)
	}

	// A non-timestamp string is rejected when the patch is parsed, before
	// any backend sees it.
	_, err = tp.ParseMergePatch([]byte(`{"revoked_at":"not-a-time"}`))
	var pe *core.ParseError
	if !errors.As(err, &pe) || pe.Code != patch.ECodePatchValueInvalid.String() {
		t.Errorf("non-RFC3339 string: err = %v, want %s", err, patch.ECodePatchValueInvalid.String())
	}
}
