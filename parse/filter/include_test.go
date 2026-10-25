//go:build unit

package filter_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestPageSetInclude_ReplacesAndClearsOneField(t *testing.T) {
	page := filter.Page[string]{}
	page.SetInclude("tags", filter.IncludeRows{IDs: map[string][]string{"a": {"old"}}, Truncated: []string{"a"}})
	page.SetInclude("children", filter.IncludeRows{IDs: map[string][]string{"a": {"child"}}, Truncated: []string{"a"}})
	page.SetInclude("tags", filter.IncludeRows{IDs: map[string][]string{"b": {"new"}}})
	require.Equal(t, map[string]map[string][]string{"tags": {"b": {"new"}}, "children": {"a": {"child"}}}, page.Relations)
	require.Equal(t, map[string][]string{"children": {"a"}}, page.Truncated)
	page.SetInclude("tags", filter.IncludeRows{})
	require.Equal(t, map[string]map[string][]string{"children": {"a": {"child"}}}, page.Relations)
	require.Equal(t, map[string][]string{"children": {"a"}}, page.Truncated)
	page.SetInclude("children", filter.IncludeRows{})
	body, err := json.Marshal(page)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	require.NotContains(t, wire, "_relations")
	require.NotContains(t, wire, "_truncated")
}

type widgetIncludable struct {
	ID   string   `db:"id" json:"id" parse:"filter"`
	Tags []string `db:"-" json:"-" parse:"filter,include" filter_ops:"eq,in" relation:"widget_tags,widget_id,tag_id,id,tenant_id"`
}

type widgetNoInclude struct {
	ID   string   `db:"id" json:"id" parse:"filter"`
	Tags []string `db:"-" json:"-" parse:"filter" filter_ops:"eq,in" relation:"widget_tags,widget_id,tag_id"`
}

func TestGenerateFilterSchema_IncludableTagSets_Includable(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetIncludable](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tags := schema.Fields["tags"]
	if !tags.Includable {
		t.Errorf("expected Includable=true on parse:\"filter,include\" field")
	}
}

func TestBuildIncludeQuery_Postgres_TenantScoped(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetIncludable](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tags := schema.Fields["tags"]

	q, err := filter.BuildIncludeQuery(tags, filter.PostgresDialect{}, []any{"w1", "w2"}, filter.WithIncludeTenant("tenant-A"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(q.SQL, "FROM widget_tags j") {
		t.Errorf("missing junction FROM clause: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "j.widget_id IN (") {
		t.Errorf("missing source IN clause: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "j.tenant_id = $3") {
		t.Errorf("expected tenant predicate after PK params, got: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "WHERE include_rank <= 101 ORDER BY include__.owner_pk, include__.target_id") {
		t.Errorf("expected the default per-owner cap and a stable ORDER BY: %s", q.SQL)
	}
	if len(q.Args) != 3 {
		t.Errorf("want 3 args (2 PKs + tenant), got %d: %v", len(q.Args), q.Args)
	}
}

// A tenant-scoped include with no tenant bound fails closed; the predicate
// is omitted only on explicit opt-out.
func TestBuildIncludeQuery_NoTenantBound_FailsClosed(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetIncludable](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, buildErr := filter.BuildIncludeQuery(schema.Fields["tags"], filter.PostgresDialect{}, []any{"w1"}); buildErr == nil {
		t.Fatal("expected a tenant-scoped error")
	}
	q, err := filter.BuildIncludeQuery(schema.Fields["tags"], filter.PostgresDialect{}, []any{"w1"}, filter.WithIncludeUnscoped())
	if err != nil {
		t.Fatalf("unscoped: %v", err)
	}
	if strings.Contains(q.SQL, "tenant_id") {
		t.Errorf("unscoped include carried a tenant predicate: %s", q.SQL)
	}
}

func TestBuildIncludeQuery_RejectsNonIncludable(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetNoInclude](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, err = filter.BuildIncludeQuery(schema.Fields["tags"], filter.PostgresDialect{}, []any{"w1"})
	if err == nil {
		t.Error("expected error on field without parse:\"include\"")
	}
}

func TestBuildIncludeQuery_RejectsNonRelation(t *testing.T) {
	scalarField := &filter.FieldSchema{
		ExternalName: "name", SQLExpr: "items.name",
		DataType: core.FieldTypeString, Includable: true,
		// No Relation.
	}
	_, err := filter.BuildIncludeQuery(scalarField, filter.PostgresDialect{}, []any{"x"})
	if err == nil {
		t.Error("expected error on non-relation field")
	}
}

func TestBuildIncludeQuery_EmptyOwners_EmptySQL(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetIncludable](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	q, err := filter.BuildIncludeQuery(schema.Fields["tags"], filter.PostgresDialect{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if q.SQL != "" {
		t.Errorf("empty owner set should produce empty SQL, got: %q", q.SQL)
	}
}

// fakeRows is a deterministic Rows for testing
// IncludeQuery.Scan without a database.
type fakeRows struct {
	rows [][2]string
	i    int
	err  error
}

func (f *fakeRows) Next() bool { return f.i < len(f.rows) }
func (f *fakeRows) Scan(dest ...any) error {
	if len(dest) != 2 {
		return errors.New("expected 2 dest columns")
	}
	for i := range dest {
		value, ok := dest[i].(*any)
		if !ok {
			return errors.New("expected *any destination")
		}
		*value = f.rows[f.i][i]
	}
	f.i++
	return nil
}
func (f *fakeRows) Err() error { return f.err }

// Scan groups by owner and reports an owner whose IDs exceed the limit —
// the query reads one more than the limit — keeping the first Limit.
func TestIncludeQueryScan_GroupsByOwnerAndReportsTruncation(t *testing.T) {
	rows := &fakeRows{rows: [][2]string{
		{"w1", "t1"}, {"w1", "t2"},
		{"w2", "t1"}, {"w2", "t3"}, {"w2", "t9"},
	}}
	out, err := filter.IncludeQuery{Limit: 2}.Scan(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if want := map[string][]string{"w1": {"t1", "t2"}, "w2": {"t1", "t3"}}; !reflect.DeepEqual(out.IDs, want) {
		t.Errorf("IDs = %v, want %v", out.IDs, want)
	}
	if !slices.Equal(out.Truncated, []string{"w2"}) {
		t.Errorf("Truncated = %v, want [w2]", out.Truncated)
	}
}

func TestBuildIncludeQuery_CapsInSQL(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[widgetIncludable](core.NewSchemaGenerator())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	q, err := filter.BuildIncludeQuery(schema.Fields["tags"], filter.SQLiteDialect{}, []any{"w1"},
		filter.WithIncludeUnscoped(), filter.WithMaxIncludePerRelation(2))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(q.SQL, "ROW_NUMBER() OVER (PARTITION BY owner_pk ORDER BY target_id)") || !strings.Contains(q.SQL, "include_rank <= 3") ||
		!strings.Contains(q.SQL, "SELECT DISTINCT j.widget_id AS owner_pk, j.tag_id COLLATE BINARY AS target_id") {
		t.Errorf("expected a per-owner window cap of 2 over distinct pairs: %s", q.SQL)
	}
}

func TestParseIncludes(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[widgetIncludable](core.NewSchemaGenerator())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got, err := filter.ParseIncludes(schema, " tags, ,tags")
	if err != nil || len(got) != 1 || got[0] != "tags" {
		t.Errorf("ParseIncludes = %v, %v; want [tags]", got, err)
	}
	for param, code := range map[string]string{"nope": filter.ECodeUnknownField.String(), "id": filter.ECodeInvalidRelation.String()} {
		_, err := filter.ParseIncludes(schema, param)
		var pe *core.ParseError
		if !errors.As(err, &pe) || pe.Code != code {
			t.Errorf("ParseIncludes(%q) = %v; want %s", param, err, code)
		}
	}
}

type widgetEdges struct {
	ID      string   `db:"id" json:"id" parse:"filter"`
	Targets []string `db:"-" json:"-" parse:"filter,include,facet" filter_ops:"eq,in" relation:"widget_edges,from_id,to_id"`
}

// Two typed edges between one owner and one target are one pair: the
// include names the target once and the facet counts the owner once
// .
func TestRelation_TypedEdgesCountOnce(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	if _, buildErr := db.Exec(`CREATE TABLE widgets (id TEXT PRIMARY KEY);
		CREATE TABLE widget_edges (from_id TEXT, to_id TEXT, kind TEXT);
		INSERT INTO widgets VALUES ('w1');
		INSERT INTO widget_edges VALUES ('w1','t1','feeds'), ('w1','t1','powers'), ('w1','t2','feeds');`); buildErr != nil {
		t.Fatal(buildErr)
	}
	schema, err := filter.GenerateFilterSchema[widgetEdges](core.NewSchemaGenerator(core.WithSQLTableName("widgets")))
	if err != nil {
		t.Fatal(err)
	}

	q, err := filter.BuildIncludeQuery(schema.Fields["targets"], filter.SQLiteDialect{}, []any{"w1"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(q.SQL, q.Args...)
	if err != nil {
		t.Fatal(err)
	}
	res, err := q.Scan(rows)
	require.NoError(t, rows.Close())
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IDs["w1"]; !slices.Equal(got, []string{"t1", "t2"}) {
		t.Errorf("include = %v, want [t1 t2]", got)
	}

	fq, err := filter.BuildFacetQuery(schema, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "targets"}})
	if err != nil {
		t.Fatal(err)
	}
	frows, err := db.Query(fq.SQL, fq.Args...)
	if err != nil {
		t.Fatal(err)
	}
	facets, err := filter.ScanFacetUnion(frows)
	require.NoError(t, frows.Close())
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, b := range facets[0].Buckets {
		counts[b.Key] = b.Count
	}
	if counts["t1"] != 1 || counts["t2"] != 1 {
		t.Errorf("facet counts = %v, want one owner per target", counts)
	}
}
