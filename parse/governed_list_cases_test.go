//go:build unit || integration

package parse_test

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A governed list: tenant A reads its own documents and tenant B's b1,
// granted. Each document's notes, labels and topics are rows of its own
// tenant; the fixture also holds rows of the other tenant hanging from
// them (A's note n3 and pairs on b1, B's pair on a2), which are no
// document's relations.

type govDocument struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Title    string `json:"title"     db:"title"     parse:"filter"`
	// Topics is a pair table compared by key (the relation tag).
	Topics []string `json:"-" db:"-" parse:"filter" relation_name:"topics" relation:"document_topics,document_id,topic_id,id,tenant_id" indexed:"true"`
}

type govNote struct {
	ID         string     `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   string     `json:"tenant_id"   db:"tenant_id"`
	DocumentID string     `json:"document_id" db:"document_id" parse:"filter"`
	Body       string     `json:"body"        db:"body"        parse:"filter"`
	DeletedAt  *time.Time `json:"deleted_at"  db:"deleted_at"`
}

type govReaction struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	NoteID   string `json:"note_id"   db:"note_id"   parse:"filter"`
	Kind     string `json:"kind"      db:"kind"      parse:"filter"`
}

type govLabel struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter"`
}

func governedDocuments(t *testing.T) *parse.Resource[govDocument] {
	t.Helper()
	documents := parse.MustResource[govDocument]("documents", parse.TenantColumn("tenant_id"), parse.ParserOptions(core.WithMaxCost(1<<10)))
	notes := parse.MustResource[govNote]("notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	reactions := parse.MustResource[govReaction]("reactions", parse.TenantColumn("tenant_id"))
	labels := parse.MustResource[govLabel]("labels", parse.TenantColumn("tenant_id"))
	notes.MustRelate("reactions", parse.HasMany(reactions, "note_id", parse.Indexed()))
	documents.MustRelate("notes", parse.HasMany(notes, "document_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	documents.MustRelate("labels", parse.ManyToMany(labels,
		parse.Junction{Table: "document_labels", Source: "document_id", Target: "label_id", Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable()))
	return documents
}

// govTable is one fixture table: its rows are the memory store's and,
// rendered, the databases'.
type govTable struct {
	name    string
	columns []string
	rows    [][]any
}

// govDeleted is a deleted_at value: a timestamp in each store.
var govDeleted = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func governedFixture() []govTable {
	return []govTable{
		{"documents", []string{"id", "tenant_id", "title"}, [][]any{
			{"a1", "A", "alpha"}, {"a2", "A", "beta"}, {"b1", "B", "gamma"}, {"b2", "B", "delta"},
		}},
		{"notes", []string{"id", "tenant_id", "document_id", "body", "deleted_at"}, [][]any{
			{"n1", "A", "a1", "todo", nil},
			{"n2", "B", "b1", "urgent", nil},
			{"n3", "A", "b1", "stray", nil}, // A's row on B's document
			{"n4", "B", "b2", "urgent", nil},
			{"n5", "B", "b1", "archived", govDeleted},
		}},
		{"reactions", []string{"id", "tenant_id", "note_id", "kind"}, [][]any{
			{"r1", "B", "n2", "up"},
			{"r2", "A", "n2", "down"}, // A's row on B's note
			{"r3", "A", "n1", "down"},
		}},
		{"labels", []string{"id", "tenant_id", "name"}, [][]any{
			{"la", "A", "red"}, {"lb", "B", "red"}, {"lc", "B", "blue"},
		}},
		{"document_labels", []string{"document_id", "label_id", "tenant_id"}, [][]any{
			{"a1", "la", "A"}, {"b1", "lb", "B"}, {"b1", "lc", "B"},
			{"b1", "la", "A"}, // A's pair on B's document
			{"a2", "lb", "B"}, // B's pair on A's document
		}},
		{"document_topics", []string{"document_id", "topic_id", "tenant_id"}, [][]any{
			{"a1", "x", "A"}, {"b1", "z", "B"},
			{"b1", "y", "A"}, // A's pair on B's document
		}},
	}
}

// governedFixtureSQL creates and loads the fixture; timestampType is the
// dialect's column type for deleted_at.
func governedFixtureSQL(timestampType string) []string {
	stmts := []string{
		`CREATE TABLE documents (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, title TEXT NOT NULL)`,
		`CREATE TABLE notes (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, document_id TEXT NOT NULL, body TEXT NOT NULL, deleted_at ` + timestampType + `)`,
		`CREATE TABLE reactions (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, note_id TEXT NOT NULL, kind TEXT NOT NULL)`,
		`CREATE TABLE labels (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE document_labels (document_id TEXT NOT NULL, label_id TEXT NOT NULL, tenant_id TEXT NOT NULL, PRIMARY KEY (document_id, label_id, tenant_id))`,
		`CREATE TABLE document_topics (document_id TEXT NOT NULL, topic_id TEXT NOT NULL, tenant_id TEXT NOT NULL)`,
	}
	for _, tb := range governedFixture() {
		rows := make([]string, len(tb.rows))
		for i, row := range tb.rows {
			vals := make([]string, len(row))
			for j, v := range row {
				switch v := v.(type) {
				case nil:
					vals[j] = "NULL"
				case time.Time:
					vals[j] = "'" + v.Format(time.RFC3339) + "'"
				default:
					vals[j] = fmt.Sprintf("'%v'", v)
				}
			}
			rows[i] = "(" + strings.Join(vals, ", ") + ")"
		}
		stmts = append(stmts, "INSERT INTO "+tb.name+" ("+strings.Join(tb.columns, ", ")+") VALUES "+strings.Join(rows, ", "))
	}
	return stmts
}

func govEq(field string, v any) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
}

// governanceA is tenant A's rows and B's b1, granted.
func governanceA() *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{govEq("tenant_id", "A"), govEq("id", "b1")}}
}

var governedCases = []struct {
	name   string
	filter string
	want   []string
}{
	{"every governed row", ``, []string{"a1", "a2", "b1"}},
	{"a granted row's own related row", `{"notes":{"$some":{"body":"urgent"}}}`, []string{"b1"}},
	{"a row of another tenant on a granted row is not related", `{"notes":{"$some":{"body":"stray"}}}`, nil},
	{"none: a granted row is judged by its own related rows", `{"notes":{"$none":{"body":"urgent"}}}`, []string{"a1", "a2"}},
	{"a deleted related row is not related", `{"notes":{"$some":{"body":"archived"}}}`, nil},
	{"null: no related row of the row's tenant", `{"notes":{"$null":true}}`, []string{"a2"}},
	{"own tenant's related row", `{"notes":{"$some":{"body":"todo"}}}`, []string{"a1"}},
	{"depth two follows the granted row's tenant", `{"notes":{"$some":{"reactions":{"$some":{"kind":"up"}}}}}`, []string{"b1"}},
	{"depth two skips another tenant's rows", `{"notes":{"$some":{"reactions":{"$some":{"kind":"down"}}}}}`, []string{"a1"}},
	{"many-to-many: pair and target in the row's tenant", `{"labels":{"$some":{"name":"blue"}}}`, []string{"b1"}},
	{"many-to-many: each row's own labels", `{"labels":{"$some":{"name":"red"}}}`, []string{"a1", "b1"}},
	{"many-to-many id: another tenant's pair is not related", `{"labels":"la"}`, []string{"a1"}},
	{"many-to-many id: a pair of another tenant on an own row", `{"labels":"lb"}`, []string{"b1"}},
	{"pair table by key", `{"topics":"z"}`, []string{"b1"}},
	{"pair table by key: another tenant's pair", `{"topics":"y"}`, nil},
	{"combined with a column filter", `{"$or":[{"title":"beta"},{"labels":{"$some":{"name":"blue"}}}]}`, []string{"a2", "b1"}},
}

// governedResult is a list's page with its count, includes and facets.
type governedResult struct {
	ids      []string
	count    int64
	includes map[string]map[string][]string
	facets   map[string]map[string]int64
}

var governedIncludes = []string{"notes", "labels"}

func listGoverned(t *testing.T, r *parse.Resource[govDocument], d filter.Dialect, db *sql.DB, filterJSON string, scope parse.Scope) governedResult {
	t.Helper()
	return listRelated(t, r, d, db, filterJSON, scope, governedIncludes)
}

// listRelated runs filterJSON under scope on db through the dialect's
// executor, including and faceting relations.
func listRelated[T any](t *testing.T, r *parse.Resource[T], d filter.Dialect, db *sql.DB, filterJSON string, scope parse.Scope, relations []string) governedResult {
	t.Helper()
	q := url.Values{
		filter.ParamPageSize: {"50"}, filter.ParamIncludeTotal: {"true"},
		filter.ParamInclude: {strings.Join(relations, ",")}, filter.ParamFacets: {strings.Join(relations, ",")},
	}
	if filterJSON != "" {
		q.Set(filter.ParamFilter, filterJSON)
	}
	req, err := r.ParseList(q)
	require.NoError(t, err)
	plan, err := r.BuildList(d, req, scope)
	require.NoError(t, err)
	require.NotNil(t, plan.Count)
	require.NotNil(t, plan.Facets)
	page := listPage(t, db, r, d, plan)

	res := governedResult{includes: map[string]map[string][]string{}, facets: map[string]map[string]int64{}}
	for _, item := range page.Items {
		key, err := r.Key(item)
		require.NoError(t, err)
		res.ids = append(res.ids, fmt.Sprint(key))
	}
	slices.Sort(res.ids)
	require.NotNil(t, page.PageInfo.TotalCount)
	res.count = *page.PageInfo.TotalCount
	for field, ids := range page.Relations {
		res.includes[field] = ids
	}
	for field, buckets := range page.Facets {
		res.facets[field] = map[string]int64{}
		for _, b := range buckets {
			res.facets[field][b.Key] = b.Count
		}
	}
	return res
}

// runGovernedLists holds a governed list to its rows and each row's
// relations to the row's tenant, on a database.
func runGovernedLists(t *testing.T, d filter.Dialect, db *sql.DB) {
	t.Helper()
	r := governedDocuments(t)
	for _, tc := range governedCases {
		t.Run(tc.name, func(t *testing.T) {
			got := listGoverned(t, r, d, db, tc.filter, parse.Scope{Governance: governanceA()})
			assert.Equal(t, tc.want, got.ids)
			assert.Equal(t, int64(len(got.ids)), got.count, "the count is the page's")
			assert.Equal(t, facetsOfIncludes(got.ids, got.includes, governedIncludes), got.facets, "the facets count the page's related rows")

			// A's rows are what the tenant floor lists: governance only
			// adds the grant.
			floor := listGoverned(t, r, d, db, tc.filter, parse.Scope{Tenant: "A"})
			var own []string
			for _, id := range got.ids {
				if id != "b1" {
					own = append(own, id)
				}
			}
			assert.Equal(t, own, floor.ids)

			// Governance that grants nothing beyond the tenant is the floor.
			ungranted := listGoverned(t, r, d, db, tc.filter, parse.Scope{Governance: govEq("tenant_id", "A")})
			assert.Equal(t, floor, ungranted)
		})
	}

	t.Run("includes and facets of a granted row are its own tenant's", func(t *testing.T) {
		got := listGoverned(t, r, d, db, ``, parse.Scope{Governance: governanceA()})
		assert.Equal(t, map[string]map[string][]string{
			"notes":  {"a1": {"n1"}, "b1": {"n2"}},       // not A's n3 or the deleted n5
			"labels": {"a1": {"la"}, "b1": {"lb", "lc"}}, // not A's pair to la, or B's on a2
		}, got.includes)
		assert.Equal(t, map[string]map[string]int64{
			"notes":  {"n1": 1, "n2": 1},
			"labels": {"la": 1, "lb": 1, "lc": 1},
		}, got.facets)
	})
}

// facetsOfIncludes counts the owners per related key among ids, as a
// relation facet counts them.
func facetsOfIncludes(ids []string, includes map[string]map[string][]string, relations []string) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, field := range relations {
		for _, owner := range ids {
			for _, key := range includes[field][owner] {
				if out[field] == nil {
					out[field] = map[string]int64{}
				}
				out[field][key]++
			}
		}
	}
	return out
}

// runGovernedMemory lists each case as a backend holding rows in memory
// does: the composed policy selects the rows, and each tenant's rows are
// filtered and included with the relations resolved for that tenant.
func runGovernedMemory(t *testing.T, sqlResult func(t *testing.T, filterJSON string) governedResult) {
	t.Helper()
	r := governedDocuments(t)
	store := governedMemory{}
	for _, tb := range governedFixture() {
		for _, row := range tb.rows {
			m := map[string]any{}
			for i, c := range tb.columns {
				m[c] = row[i]
			}
			store[tb.name] = append(store[tb.name], m)
		}
	}
	for _, tc := range governedCases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.list(t, r, tc.filter, governanceA())
			assert.Equal(t, tc.want, got.ids)
			want := sqlResult(t, tc.filter)
			assert.Equal(t, want.ids, got.ids)
			assert.Equal(t, want.includes, got.includes)
		})
	}
}

// governedMemory is a store holding each table's rows by bare column.
type governedMemory map[string][]map[string]any

func (m governedMemory) list(t *testing.T, r *parse.Resource[govDocument], filterJSON string, governance *filter.PolicyConstraints) governedResult {
	t.Helper()
	ctx := context.Background()
	req, err := r.ParseListInput(parse.ListInput{Filter: filterJSON})
	require.NoError(t, err)
	schema := r.FilterSchema()
	policy, err := filter.ApplyPolicyConstraints(schema, nil, filter.ComposeConstraints(
		filter.RepositoryScope{TenantColumn: r.TenantColumnName(), DeleteColumn: r.DeletedColumnName()}, governance, nil))
	require.NoError(t, err)

	byTenant := map[string][]map[string]any{}
	for _, row := range m[r.Table()] {
		if m.selects(t, policy, r.Table(), row) {
			tenant, ok := row[r.TenantColumnName()].(string)
			require.True(t, ok)
			byTenant[tenant] = append(byTenant[tenant], row)
		}
	}
	res := governedResult{includes: map[string]map[string][]string{}}
	for _, tenant := range slices.Sorted(maps.Keys(byTenant)) {
		f := req.Filter()
		if f != nil {
			f, err = filter.ResolveMemberships(ctx, f, m, filter.ResolveRelationScope(filter.RelationScope{Tenant: tenant}))
			require.NoError(t, err)
		}
		var keys []any
		for _, row := range byTenant[tenant] {
			if f == nil || m.selects(t, f.AST(), r.Table(), row) {
				id, ok := row["id"].(string)
				require.True(t, ok)
				res.ids = append(res.ids, id)
				keys = append(keys, id)
			}
		}
		for _, name := range governedIncludes {
			rows, err := filter.ResolveInclude(ctx, schema.Fields[name], keys, m, filter.WithIncludeRelationScope(filter.RelationScope{Tenant: tenant}))
			require.NoError(t, err)
			if len(rows.IDs) == 0 {
				continue
			}
			if res.includes[name] == nil {
				res.includes[name] = map[string][]string{}
			}
			maps.Copy(res.includes[name], rows.IDs)
		}
	}
	slices.Sort(res.ids)
	return res
}

func (m governedMemory) selects(t *testing.T, ast *filter.FilterAST, table string, row map[string]any) bool {
	t.Helper()
	if ast == nil || ast.Root == nil {
		return true
	}
	truth, err := filter.Evaluate(ast.Root, governedRow(table, row))
	require.NoError(t, err)
	return truth == filter.True
}

// governedRow reads a table's row by its qualified columns (table.column).
func governedRow(table string, row map[string]any) filter.ColumnRow {
	return func(column string) (any, bool) {
		name, ok := strings.CutPrefix(column, table+".")
		if !ok {
			return nil, false
		}
		v, ok := row[name]
		return v, ok
	}
}

func (m governedMemory) selected(l *filter.Lookup) ([]map[string]any, error) {
	rows, ok := m[l.Table]
	if !ok {
		return nil, fmt.Errorf("no table %q", l.Table)
	}
	var out []map[string]any
	for _, row := range rows {
		ok, err := filter.MatchLookupRow(l.Where, func(name string) (any, bool) {
			v, ok := row[name]
			return v, ok
		})
		if err != nil {
			return nil, err
		}
		if ok && l.Filter != nil && l.Filter.AST() != nil && l.Filter.AST().Root != nil {
			truth, err := filter.Evaluate(l.Filter.AST().Root, governedRow(l.Table, row))
			if err != nil {
				return nil, err
			}
			ok = truth == filter.True
		}
		if ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m governedMemory) LookupValues(_ context.Context, l *filter.Lookup) ([]any, error) {
	rows, err := m.selected(l)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		v, ok := row[l.Project]
		if !ok {
			return nil, fmt.Errorf("table %s has no column %q", l.Table, l.Project)
		}
		out[i] = v
	}
	return out, nil
}

func (m governedMemory) LookupFilteredValues(ctx context.Context, l *filter.Lookup) ([]any, error) {
	return m.LookupValues(ctx, l)
}

func (m governedMemory) LookupPairs(_ context.Context, l *filter.Lookup, ownerColumn string) (iter.Seq2[filter.IncludePair, error], error) {
	rows, err := m.selected(l)
	if err != nil {
		return nil, err
	}
	return func(yield func(filter.IncludePair, error) bool) {
		for _, row := range rows {
			if !yield(filter.IncludePair{Owner: row[ownerColumn], Target: row[l.Project]}, nil) {
				return
			}
		}
	}, nil
}

// Tenant columns of two types in one graph, both holding a UUID: a
// governed list compares each related row's tenant with the row's, by the
// UUID's text where a UUID column meets a string one. A pair table holds
// the owner's type.

type mixDevice[K any] struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID K      `json:"tenant_id" db:"tenant_id"`
}

type mixCommand[K any] struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID K      `json:"tenant_id" db:"tenant_id"`
	DeviceID string `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}

type mixTag[K any] struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID K      `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter"`
}

const (
	mixU1 = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	mixU2 = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

var mixRelations = []string{"commands", "tags"}

func mixedDevices[O, R any](prefix string) *parse.Resource[mixDevice[O]] {
	devices := parse.MustResource[mixDevice[O]](prefix+"devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[mixCommand[R]](prefix+"commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[mixTag[R]](prefix+"tags", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	devices.MustRelate("tags", parse.ManyToMany(tags,
		parse.Junction{Table: prefix + "device_tags", Source: "device_id", Target: "tag_id", Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable()))
	return devices
}

// mixedFixtureSQL is the graph under prefix: the devices' and pairs'
// tenant columns of ownerType, the related tables' of relatedType.
func mixedFixtureSQL(prefix, ownerType, relatedType string) []string {
	return []string{
		`CREATE TABLE ` + prefix + `devices (id TEXT PRIMARY KEY, tenant_id ` + ownerType + ` NOT NULL)`,
		`CREATE TABLE ` + prefix + `commands (id TEXT PRIMARY KEY, tenant_id ` + relatedType + ` NOT NULL, device_id TEXT NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE ` + prefix + `tags (id TEXT PRIMARY KEY, tenant_id ` + relatedType + ` NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE ` + prefix + `device_tags (device_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id ` + ownerType + ` NOT NULL)`,
		// d2 is U2's, granted; d3 U2's, not. c3 and the pair d2-g1 are
		// U1's rows on U2's device.
		`INSERT INTO ` + prefix + `devices VALUES ('d1','` + mixU1 + `'), ('d2','` + mixU2 + `'), ('d3','` + mixU2 + `')`,
		`INSERT INTO ` + prefix + `commands VALUES ('c1','` + mixU1 + `','d1','sent'), ('c2','` + mixU2 + `','d2','sent'),
			('c3','` + mixU1 + `','d2','sent'), ('c4','` + mixU2 + `','d3','sent')`,
		`INSERT INTO ` + prefix + `tags VALUES ('g1','` + mixU1 + `','hot'), ('g2','` + mixU2 + `','hot')`,
		`INSERT INTO ` + prefix + `device_tags VALUES ('d1','g1','` + mixU1 + `'), ('d2','g2','` + mixU2 + `'), ('d2','g1','` + mixU1 + `')`,
	}
}

// runGovernedMixedTypes lists a governed graph whose tenant columns mix a
// UUID and a string type, both ways round; uuidType is the dialect's UUID
// column type.
func runGovernedMixedTypes(t *testing.T, d filter.Dialect, uuidType string, db *sql.DB) {
	t.Helper()
	governance := &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{govEq("tenant_id", mixU1), govEq("id", "d2")}}
	check := func(t *testing.T, list func(filterJSON string) governedResult) {
		t.Helper()
		for _, tc := range []struct {
			filter string
			want   []string
		}{
			{``, []string{"d1", "d2"}},
			{`{"commands":{"$some":{"status":"sent"}}}`, []string{"d1", "d2"}},
			{`{"commands":"c3"}`, nil},
			{`{"tags":"g1"}`, []string{"d1"}},
			{`{"tags":{"$some":{"name":"hot"}}}`, []string{"d1", "d2"}},
		} {
			assert.Equal(t, tc.want, list(tc.filter).ids, tc.filter)
		}
		got := list(``)
		assert.Equal(t, map[string]map[string][]string{
			"commands": {"d1": {"c1"}, "d2": {"c2"}},
			"tags":     {"d1": {"g1"}, "d2": {"g2"}},
		}, got.includes)
		assert.Equal(t, facetsOfIncludes(got.ids, got.includes, mixRelations), got.facets)
	}

	t.Run("uuid owner, string related tables", func(t *testing.T) {
		for _, stmt := range mixedFixtureSQL("u_", uuidType, "TEXT") {
			_, err := db.Exec(stmt)
			require.NoError(t, err, stmt)
		}
		r := mixedDevices[uuid.UUID, string]("u_")
		check(t, func(f string) governedResult {
			return listRelated(t, r, d, db, f, parse.Scope{Governance: governance}, mixRelations)
		})
	})
	t.Run("string owner, uuid related tables", func(t *testing.T) {
		for _, stmt := range mixedFixtureSQL("s_", "TEXT", uuidType) {
			_, err := db.Exec(stmt)
			require.NoError(t, err, stmt)
		}
		r := mixedDevices[string, uuid.UUID]("s_")
		check(t, func(f string) governedResult {
			return listRelated(t, r, d, db, f, parse.Scope{Governance: governance}, mixRelations)
		})
	})
}
