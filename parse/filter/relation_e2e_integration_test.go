//go:build integration

package filter_test

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// end-to-end smoke against SQLite. Exercises every
// surface activated by the new tag — filter / patch (replace, add,
// remove) / facet / include / sort — through a fixture entity with one
// tenant-scoped junction. The test doubles as the cross-tenant proof:
// rows owned by tenant B are unreachable from tenant A's perspective
// regardless of whether the user knows their target IDs.
//
// Junction shape:
//
//	widgets    (id, tenant_id, name, color)
//	tags       (id, tenant_id, label)
//	widget_tags(widget_id, tag_id, tenant_id)
//
// Fixture entity opts into the full activation matrix on the Tags field.
type widget struct {
	ID       string   `db:"id" json:"id" sort_field:"true" sort_default:"asc"`
	TenantID string   `db:"tenant_id" json:"tenant_id" parse:"filter"`
	Name     string   `db:"name" json:"name" parse:"filter,patch" sort_field:"true"`
	Color    string   `db:"color" json:"color" parse:"filter,patch,facet" facet:"terms:10"`
	Tags     []string `db:"-" json:"-" parse:"filter,patch,facet,include" filter_ops:"eq,in,nin" facet:"terms:10" relation:"widget_tags,widget_id,tag_id,id,tenant_id"`
}

func setupRelationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE widgets (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			name TEXT NOT NULL,
			color TEXT NOT NULL
		)`,
		`CREATE TABLE tags (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			label TEXT NOT NULL
		)`,
		`CREATE TABLE widget_tags (
			widget_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			PRIMARY KEY (widget_id, tag_id)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	// Seed widgets: t1 owns w1, w2, w3; t2 owns w9 (cross-tenant).
	for _, w := range []struct {
		id, tid, name, color string
	}{
		{"w1", "t1", "alpha", "red"},
		{"w2", "t1", "bravo", "blue"},
		{"w3", "t1", "charlie", "red"},
		{"w9", "t2", "secret", "red"},
	} {
		if _, err := db.Exec(
			`INSERT INTO widgets (id, tenant_id, name, color) VALUES (?, ?, ?, ?)`,
			w.id, w.tid, w.name, w.color); err != nil {
			t.Fatalf("seed widget: %v", err)
		}
	}
	for _, tg := range []struct{ id, tid, label string }{
		{"tag-foo", "t1", "foo"},
		{"tag-bar", "t1", "bar"},
		{"tag-baz", "t1", "baz"},
		{"tag-other", "t2", "other"},
	} {
		if _, err := db.Exec(
			`INSERT INTO tags (id, tenant_id, label) VALUES (?, ?, ?)`,
			tg.id, tg.tid, tg.label); err != nil {
			t.Fatalf("seed tag: %v", err)
		}
	}
	for _, jt := range []struct{ wid, tid, tenant string }{
		{"w1", "tag-foo", "t1"},
		{"w1", "tag-bar", "t1"},
		{"w2", "tag-bar", "t1"},
		{"w2", "tag-baz", "t1"},
		// w3 has no tags initially.
		{"w9", "tag-other", "t2"}, // cross-tenant
	} {
		if _, err := db.Exec(
			`INSERT INTO widget_tags (widget_id, tag_id, tenant_id) VALUES (?, ?, ?)`,
			jt.wid, jt.tid, jt.tenant); err != nil {
			t.Fatalf("seed junction: %v", err)
		}
	}
	return db
}

func widgetSchema(t *testing.T) (*filter.ResourceSchema, *filter.SortSchema, *patch.PatchSchema) {
	t.Helper()
	g := core.NewSchemaGenerator()
	rs, err := filter.GenerateFilterSchema[widget](g)
	if err != nil {
		t.Fatalf("filter schema: %v", err)
	}
	ss, err := filter.GenerateSortSchema[widget](g)
	if err != nil {
		t.Fatalf("sort schema: %v", err)
	}
	ps, err := patch.GeneratePatchSchema[widget](g)
	if err != nil {
		t.Fatalf("patch schema: %v", err)
	}
	return rs, ss, ps
}

func TestRelationE2E_Filter_TenantScopedJunction(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, _ := widgetSchema(t)

	dialect := filter.SQLiteDialect{}
	tagsField := rs.Fields["tags"]

	// Filter: widgets with tag_id IN ('tag-foo','tag-bar'), bound to tenant t1.
	// The outer tenant predicate is redundant with the junction-side
	// predicate emitted by WithTenant — w9 (tenant t2) is unreachable
	// because its sole junction row carries tenant_id=t2.
	ast := &filter.FilterAST{Root: &filter.FilterNode{
		Field: tagsField, Op: filter.OpIn, Value: []any{"tag-foo", "tag-bar"},
	}}
	b := filter.NewSQLBuilder(rs, dialect, filter.WithTenant("t1"))
	res, err := b.Build(ast)
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	q := "SELECT id FROM widgets WHERE " + res.Where + " ORDER BY id"
	rows, err := db.QueryContext(context.Background(), q, res.Args...)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"w1", "w2"}) {
		t.Errorf("got %v, want [w1 w2]", ids)
	}
}

func TestRelationE2E_Filter_CrossTenantBlocked(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, _ := widgetSchema(t)

	// Tenant t2 owns the only widget tagged 'tag-other'. Even when t1
	// asks for that tag explicitly, the junction subquery's tenant
	// predicate excludes it; no outer tenant_id filter is needed for
	// this proof.
	tagsField := rs.Fields["tags"]
	ast := &filter.FilterAST{Root: &filter.FilterNode{
		Field: tagsField, Op: filter.OpEq, Value: "tag-other",
	}}
	b := filter.NewSQLBuilder(rs, filter.SQLiteDialect{}, filter.WithTenant("t1"))
	res, err := b.Build(ast)
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	q := "SELECT id FROM widgets WHERE " + res.Where
	rows, err := db.QueryContext(context.Background(), q, res.Args...)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var id string
		_ = rows.Scan(&id)
		t.Errorf("expected no rows, got %q — junction tenant scope leaked across tenants", id)
	}
}

func TestRelationE2E_Patch_Replace_Add_Remove_Atomic(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, ps := widgetSchema(t)
	_ = rs

	dialect := filter.SQLiteDialect{}
	parser := patch.NewParser(ps, nil)
	parsed, err := parser.ParseOps([]patch.RawPatchOp{
		// Replace w1's tags from {foo,bar} to {bar,baz}
		{Op: "replace", Path: "/tags", Value: []any{"tag-bar", "tag-baz"}},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	result, err := patch.BuildPatchSQL(parsed, dialect)
	if err != nil {
		t.Fatalf("build patch: %v", err)
	}
	if len(result.RelationOps) != 1 {
		t.Fatalf("want 1 relation op, got %d", len(result.RelationOps))
	}

	// Apply with owner pk = "w1", tenant = "t1" — atomic via Tx.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	for _, rop := range result.RelationOps {
		stmts, err := patch.RenderRelationOp(rop, dialect, []any{"w1"}, patch.RenderRelationOpOptions{TenantValue: "t1"})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(context.Background(), s.SQL, s.Args...); err != nil {
				t.Fatalf("exec %q: %v", s.SQL, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify w1's junction state.
	rows, err := db.QueryContext(context.Background(),
		`SELECT tag_id FROM widget_tags WHERE widget_id = 'w1' ORDER BY tag_id`)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	if !reflect.DeepEqual(got, []string{"tag-bar", "tag-baz"}) {
		t.Errorf("after replace got %v, want [tag-bar tag-baz]", got)
	}

	// Now add tag-foo back via add verb.
	parsed, _ = parser.ParseOps([]patch.RawPatchOp{
		{Op: "add", Path: "/tags", Value: []any{"tag-foo"}},
	})
	result, _ = patch.BuildPatchSQL(parsed, dialect)
	stmts, _ := patch.RenderRelationOp(result.RelationOps[0], dialect, []any{"w1"}, patch.RenderRelationOpOptions{TenantValue: "t1"})
	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s.SQL, s.Args...); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	// And remove tag-bar.
	parsed, _ = parser.ParseOps([]patch.RawPatchOp{
		{Op: "remove", Path: "/tags", Value: []any{"tag-bar"}},
	})
	result, _ = patch.BuildPatchSQL(parsed, dialect)
	stmts, _ = patch.RenderRelationOp(result.RelationOps[0], dialect, []any{"w1"}, patch.RenderRelationOpOptions{TenantValue: "t1"})
	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s.SQL, s.Args...); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}

	// Final state should be {tag-baz, tag-foo}.
	rows2, _ := db.QueryContext(context.Background(),
		`SELECT tag_id FROM widget_tags WHERE widget_id = 'w1' ORDER BY tag_id`)
	defer rows2.Close()
	var final []string
	for rows2.Next() {
		var s string
		_ = rows2.Scan(&s)
		final = append(final, s)
	}
	if !reflect.DeepEqual(final, []string{"tag-baz", "tag-foo"}) {
		t.Errorf("final got %v, want [tag-baz tag-foo]", final)
	}
}

func TestRelationE2E_Facet_TenantScoped(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, _ := widgetSchema(t)

	q, err := filter.BuildFacetQuery(
		rs, filter.SQLiteDialect{},
		"widgets.tenant_id = ?1", []any{"t1"},
		[]filter.FacetRequest{{Field: "tags"}},
		filter.WithFacetTenant("t1"),
	)
	if err != nil {
		t.Fatalf("build facet: %v", err)
	}
	rows, err := db.QueryContext(context.Background(), q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("exec facet: %v", err)
	}
	defer rows.Close()
	results, err := filter.ScanFacetUnion(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 || results[0].Field != "tags" {
		t.Fatalf("want one tags facet, got %+v", results)
	}
	// Expected counts within t1: foo=1 (w1), bar=2 (w1,w2), baz=1 (w2).
	want := map[string]int64{"tag-foo": 1, "tag-bar": 2, "tag-baz": 1}
	got := map[string]int64{}
	for _, b := range results[0].Buckets {
		got[b.Key] = b.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facet counts got %v, want %v", got, want)
	}
}

func TestRelationE2E_Include_TenantScoped(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, _ := widgetSchema(t)

	q, err := filter.BuildIncludeQuery(
		rs.Fields["tags"], filter.SQLiteDialect{},
		[]any{"w1", "w2", "w3"},
		filter.WithIncludeTenant("t1"),
	)
	if err != nil {
		t.Fatalf("build include: %v", err)
	}
	rows, err := db.QueryContext(context.Background(), q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("exec include: %v", err)
	}
	defer rows.Close()
	res, err := q.Scan(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := res.IDs
	want := map[string][]string{
		"w1": {"tag-bar", "tag-foo"},
		"w2": {"tag-bar", "tag-baz"},
		// w3 has no junction rows → absent from the map.
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("include got %v, want %v", out, want)
	}
}

// The per-owner cap is applied by the database, lowest target IDs first.
func TestRelationE2E_Include_CapPerOwner(t *testing.T) {
	db := setupRelationDB(t)
	rs, _, _ := widgetSchema(t)

	q, err := filter.BuildIncludeQuery(rs.Fields["tags"], filter.SQLiteDialect{}, []any{"w1", "w2"},
		filter.WithIncludeTenant("t1"), filter.WithMaxIncludePerRelation(1))
	if err != nil {
		t.Fatalf("build include: %v", err)
	}
	rows, err := db.QueryContext(context.Background(), q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("exec include: %v", err)
	}
	defer rows.Close()
	res, err := q.Scan(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := map[string][]string{"w1": {"tag-bar"}, "w2": {"tag-bar"}}
	if !reflect.DeepEqual(res.IDs, want) {
		t.Errorf("include got %v, want %v", res.IDs, want)
	}
	if !reflect.DeepEqual(res.Truncated, []string{"w1", "w2"}) {
		t.Errorf("truncated = %v, want both owners", res.Truncated)
	}
}
