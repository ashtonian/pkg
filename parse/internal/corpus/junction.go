//go:build integration

package corpus

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// End-to-end smoke on each engine. Exercises every surface the relation tag
// activates — filter / patch (replace, add, remove) / facet / include /
// sort — through a fixture entity with one tenant-scoped junction. The
// cases double as the cross-tenant proof: rows owned by tenant B are
// unreachable from tenant A's perspective regardless of whether the user
// knows their target IDs.
//
// Junction shape:
//
//	widgets    (id, tenant_id, name, color)
//	tags       (id, tenant_id, label)
//	widget_tags(widget_id, tag_id, tenant_id)
//
// Fixture entity opts into the full activation matrix on the Tags field.
type widget struct {
	ID       string   `db:"id" json:"id" parse:"sort" sort_default:"asc"`
	TenantID string   `db:"tenant_id" json:"tenant_id" parse:"filter"`
	Name     string   `db:"name" json:"name" parse:"filter,patch,sort"`
	Color    string   `db:"color" json:"color" parse:"filter,patch,facet" facet:"terms:10"`
	Tags     []string `db:"-" json:"-" parse:"filter,patch,facet,include" filter_ops:"eq,in,nin" facet:"terms:10" relation:"widget_tags,widget_id,tag_id,id,tenant_id"`
}

// widgetDB is a fresh database of b holding the widgets: t1 owns w1, w2 and
// w3 (untagged), t2 owns w9, tagged only with t2's tag.
func widgetDB(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.db(t)
	dbtest.Exec(t, db,
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
		`INSERT INTO widgets (id, tenant_id, name, color) VALUES
			('w1', 't1', 'alpha', 'red'), ('w2', 't1', 'bravo', 'blue'), ('w3', 't1', 'charlie', 'red'), ('w9', 't2', 'secret', 'red')`,
		`INSERT INTO tags (id, tenant_id, label) VALUES
			('tag-foo', 't1', 'foo'), ('tag-bar', 't1', 'bar'), ('tag-baz', 't1', 'baz'), ('tag-other', 't2', 'other')`,
		`INSERT INTO widget_tags (widget_id, tag_id, tenant_id) VALUES
			('w1', 'tag-foo', 't1'), ('w1', 'tag-bar', 't1'), ('w2', 'tag-bar', 't1'), ('w2', 'tag-baz', 't1'),
			('w9', 'tag-other', 't2')`)
	return db
}

func widgetSchema(t *testing.T) (*filter.ResourceSchema, *patch.PatchSchema) {
	t.Helper()
	g := core.NewSchemaGenerator()
	rs, err := filter.GenerateFilterSchema[widget](g)
	require.NoError(t, err)
	withOwnerKeys(rs)
	ps, err := patch.GeneratePatchSchema[widget](g)
	require.NoError(t, err)
	return rs, ps
}

// withOwnerKeys reads each relation's owner columns from schema's table, as
// a resource declares them: a schema built outside one names none.
func withOwnerKeys(schema *filter.ResourceSchema) {
	for _, f := range schema.Fields {
		if f.Relation != nil && f.RelatedBy == nil {
			for _, key := range f.Relation.OwnerColumns() {
				f.RelatedBy = append(f.RelatedBy, &filter.FieldSchema{ExternalName: key, Table: schema.Name, Column: key, DataType: core.FieldTypeString})
			}
		}
	}
}

// widgetTags is w1's tags in id order.
func widgetTags(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return dbtest.QueryRows[string](t, db, `SELECT tag_id FROM widget_tags WHERE widget_id = 'w1' ORDER BY tag_id`)
}

// Filter: widgets with tag_id IN ('tag-foo','tag-bar'), bound to tenant t1.
// The outer tenant predicate is redundant with the junction-side predicate
// the bound tenant emits — w9 (tenant t2) is unreachable because its sole
// junction row carries tenant_id=t2.
func relationE2EFilterTenantScopedJunction(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		rs, _ := widgetSchema(t)
		ast := &filter.FilterAST{Root: &filter.FilterNode{
			Field: rs.Fields["tags"], Op: filter.OpIn, Value: []any{"tag-foo", "tag-bar"},
		}}
		res, err := filter.NewSQLBuilder(rs, b.dialect, filter.WithRelationScope(filter.RelationScope{Tenant: "t1"})).Build(ast)
		require.NoError(t, err)
		assert.Equal(t, []string{"w1", "w2"}, dbtest.QueryRows[string](t, db, "SELECT id FROM widgets WHERE "+res.Where+" ORDER BY id", res.Args...))
	})
}

// Tenant t2 owns the only widget tagged 'tag-other'. Even when t1 asks for
// that tag explicitly, the junction subquery's tenant predicate excludes
// it; no outer tenant_id filter is needed for this proof.
func relationE2EFilterCrossTenantBlocked(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		rs, _ := widgetSchema(t)
		ast := &filter.FilterAST{Root: &filter.FilterNode{
			Field: rs.Fields["tags"], Op: filter.OpEq, Value: "tag-other",
		}}
		res, err := filter.NewSQLBuilder(rs, b.dialect, filter.WithRelationScope(filter.RelationScope{Tenant: "t1"})).Build(ast)
		require.NoError(t, err)
		assert.Empty(t, dbtest.QueryRows[string](t, db, "SELECT id FROM widgets WHERE "+res.Where, res.Args...),
			"junction tenant scope leaked across tenants")
	})
}

// A relation patch replaces w1's tags atomically in a transaction, then adds
// and removes one, each as the statements RenderRelationOp writes.
func relationE2EPatchReplaceAddRemoveAtomic(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		_, ps := widgetSchema(t)
		d, ok := b.dialect.(patch.RelationDialect)
		require.True(t, ok, "%T renders relation patches", b.dialect)
		parser := patch.NewParser(ps, nil)
		statements := func(t *testing.T, op patch.RawPatchOp) []patch.RenderedRelationStatement {
			t.Helper()
			parsed, err := parser.ParseOps([]patch.RawPatchOp{op})
			require.NoError(t, err)
			result, err := patch.BuildPatchSQL(parsed, d)
			require.NoError(t, err)
			require.Len(t, result.RelationOps, 1)
			stmts, err := patch.RenderRelationOp(result.RelationOps[0], d, []core.Key{{"w1"}}, patch.RenderRelationOpOptions{TenantValue: "t1"})
			require.NoError(t, err)
			return stmts
		}

		// Replace w1's tags from {foo,bar} to {bar,baz}, atomic via Tx.
		tx, err := db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		for _, s := range statements(t, patch.RawPatchOp{Op: "replace", Path: "/tags", Value: []any{"tag-bar", "tag-baz"}}) {
			_, err := tx.ExecContext(t.Context(), s.SQL, s.Args...)
			require.NoError(t, err, s.SQL)
		}
		require.NoError(t, tx.Commit())
		assert.Equal(t, []string{"tag-bar", "tag-baz"}, widgetTags(t, db), "after replace")

		for _, op := range []patch.RawPatchOp{
			{Op: "add", Path: "/tags", Value: []any{"tag-foo"}},
			{Op: "remove", Path: "/tags", Value: []any{"tag-bar"}},
		} {
			for _, s := range statements(t, op) {
				_, err := db.ExecContext(t.Context(), s.SQL, s.Args...)
				require.NoError(t, err, "%s: %s", op.Op, s.SQL)
			}
		}
		assert.Equal(t, []string{"tag-baz", "tag-foo"}, widgetTags(t, db), "after add and remove")
	})
}

// A tenant-scoped relation facet counts t1's junction rows alone: foo=1
// (w1), bar=2 (w1,w2), baz=1 (w2).
func relationE2EFacetTenantScoped(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		rs, _ := widgetSchema(t)
		q, err := filter.BuildFacetQuery(rs, b.dialect,
			"widgets.tenant_id = "+b.dialect.Placeholder(1), []any{"t1"},
			[]filter.FacetRequest{{Field: "tags"}},
			filter.WithFacetRelationScope(filter.RelationScope{Tenant: "t1"}))
		require.NoError(t, err)
		results := queryFacets(t, db, q.SQL, q.Args...)
		require.Len(t, results, 1)
		assert.Equal(t, map[string]map[string]int64{"tags": {"tag-foo": 1, "tag-bar": 2, "tag-baz": 1}}, bucketCounts(results))
	})
}

// A tenant-scoped include names each owner's t1 targets in id order; an
// owner without junction rows is absent.
func relationE2EIncludeTenantScoped(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		rs, _ := widgetSchema(t)
		q, err := filter.BuildIncludeQuery(rs.Fields["tags"], b.dialect, []core.Key{{"w1"}, {"w2"}, {"w3"}},
			filter.WithIncludeRelationScope(filter.RelationScope{Tenant: "t1"}))
		require.NoError(t, err)
		res := includeRows(t, db, q)
		assert.Equal(t, map[string][]string{"w1": {"tag-bar", "tag-foo"}, "w2": {"tag-bar", "tag-baz"}}, res.IDs)
	})
}

// The per-owner cap is applied by the database, lowest target IDs first.
func relationE2EIncludeCapPerOwner(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := widgetDB(t, b)
		rs, _ := widgetSchema(t)
		q, err := filter.BuildIncludeQuery(rs.Fields["tags"], b.dialect, []core.Key{{"w1"}, {"w2"}},
			filter.WithIncludeRelationScope(filter.RelationScope{Tenant: "t1"}), filter.WithMaxIncludePerRelation(1))
		require.NoError(t, err)
		res := includeRows(t, db, q)
		assert.Equal(t, map[string][]string{"w1": {"tag-bar"}, "w2": {"tag-bar"}}, res.IDs)
		assert.Equal(t, []string{"w1", "w2"}, res.Truncated, "both owners")
	})
}

// includeRows runs q on db and reads its rows.
func includeRows(t *testing.T, db *sql.DB, q filter.IncludeQuery) filter.IncludeRows {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), q.SQL, q.Args...)
	require.NoError(t, err, q.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	res, err := q.Scan(rows)
	require.NoError(t, err, q.SQL)
	require.NoError(t, rows.Err())
	return res
}

type widgetEdges struct {
	ID      string   `db:"id" json:"id" parse:"filter"`
	Targets []string `db:"-" json:"-" parse:"filter,include,facet" filter_ops:"eq,in" relation:"widget_edges,from_id,to_id"`
}

// Two typed edges between one owner and one target are one pair: the
// include names the target once and the facet counts the owner once.
func relationTypedEdgesCountOnce(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		dbtest.Exec(t, db,
			`CREATE TABLE widgets (id TEXT PRIMARY KEY)`,
			`CREATE TABLE widget_edges (from_id TEXT, to_id TEXT, kind TEXT)`,
			`INSERT INTO widgets VALUES ('w1')`,
			`INSERT INTO widget_edges VALUES ('w1','t1','feeds'), ('w1','t1','powers'), ('w1','t2','feeds')`)
		schema, err := filter.GenerateFilterSchema[widgetEdges](core.NewSchemaGenerator(core.WithSQLTableName("widgets")))
		require.NoError(t, err)
		withOwnerKeys(schema)

		q, err := filter.BuildIncludeQuery(schema.Fields["targets"], b.dialect, []core.Key{{"w1"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"t1", "t2"}, includeRows(t, db, q).IDs["w1"])

		facets := facetCounts(t, db, b.dialect, schema, "targets")
		assert.Equal(t, map[string]int64{"t1": 1, "t2": 1}, facets["targets"], "one owner per target")
	})
}

// caseInsensitiveText is a text column type comparing case-insensitively on
// b: NOCASE on SQLite, a nondeterministic ICU collation PostgreSQL creates
// in db's schema.
func caseInsensitiveText(t *testing.T, b sqlBackend, db *sql.DB) string {
	t.Helper()
	if b.engine == SQLite {
		return "TEXT COLLATE NOCASE"
	}
	dbtest.Exec(t, db, `CREATE COLLATION case_insensitive (provider = icu, locale = 'und-u-ks-level2', deterministic = false)`)
	return "TEXT COLLATE case_insensitive"
}

// An include names its text targets in code-point order, its cap keeping
// the lowest, whatever the junction column's collation: a case-insensitive
// column's "B" and "b" are two targets.
func buildIncludeQueryTextKeysUseCodePointOrder(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		dbtest.Exec(t, db,
			`CREATE TABLE pairs (owner TEXT, target `+caseInsensitiveText(t, b, db)+`)`,
			`INSERT INTO pairs VALUES ('owner', 'a'), ('owner', 'Z'), ('owner', 'B'), ('owner', 'b')`)
		field := &filter.FieldSchema{
			ExternalName: "tags", DataType: core.FieldTypeString, Includable: true,
			Relation:  &core.RelationSchema{JoinTable: "pairs", SourceColumns: []string{"owner"}, TargetColumns: []string{"target"}, SourceTypes: textKey(), TargetTypes: textKey()},
			RelatedBy: []*filter.FieldSchema{{ExternalName: "id", Table: "owners", Column: "id", DataType: core.FieldTypeString}},
		}
		q, err := filter.BuildIncludeQuery(field, b.dialect, []core.Key{{"owner"}}, filter.WithMaxIncludePerRelation(3))
		require.NoError(t, err)
		got := includeRows(t, db, q)
		require.Equal(t, map[string][]string{"owner": {"B", "Z", "a"}}, got.IDs)
		require.Equal(t, []string{"owner"}, got.Truncated)
	})
}
