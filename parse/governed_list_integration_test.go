//go:build integration

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

// governedTables are the fixture's tables, each with its rows: the memory
// store's and the databases'.
func governedTables() []govTable {
	text := func(name string) sqlColumn { return column[string](name).required() }
	tables := []govTable{
		{sqlTable{name: "documents", key: []string{"id"}, columns: []sqlColumn{text("id"), text("tenant_id"), text("title")}}, [][]any{
			{"a1", "A", "alpha"}, {"a2", "A", "beta"}, {"b1", "B", "gamma"}, {"b2", "B", "delta"},
		}},
		{sqlTable{name: "notes", key: []string{"id"}, columns: []sqlColumn{text("id"), text("tenant_id"), text("document_id"), text("body"), column[time.Time]("deleted_at")}}, [][]any{
			{"n1", "A", "a1", "todo", nil},
			{"n2", "B", "b1", "urgent", nil},
			{"n3", "A", "b1", "stray", nil}, // A's row on B's document
			{"n4", "B", "b2", "urgent", nil},
			{"n5", "B", "b1", "archived", govDeleted()},
		}},
		{sqlTable{name: "reactions", key: []string{"id"}, columns: []sqlColumn{text("id"), text("tenant_id"), text("note_id"), text("kind")}}, [][]any{
			{"r1", "B", "n2", "up"},
			{"r2", "A", "n2", "down"}, // A's row on B's note
			{"r3", "A", "n1", "down"},
		}},
		{sqlTable{name: "labels", key: []string{"id"}, columns: []sqlColumn{text("id"), text("tenant_id"), text("name")}}, [][]any{
			{"la", "A", "red"}, {"lb", "B", "red"}, {"lc", "B", "blue"},
		}},
		{sqlTable{name: "document_labels", key: []string{"document_id", "label_id", "tenant_id"}, columns: []sqlColumn{text("document_id"), text("label_id"), text("tenant_id")}}, [][]any{
			{"a1", "la", "A"}, {"b1", "lb", "B"}, {"b1", "lc", "B"},
			{"b1", "la", "A"}, // A's pair on B's document
			{"a2", "lb", "B"}, // B's pair on A's document
		}},
		{sqlTable{name: "document_topics", columns: []sqlColumn{text("document_id"), text("topic_id"), text("tenant_id")}}, [][]any{
			{"a1", "x", "A"}, {"b1", "z", "B"},
			{"b1", "y", "A"}, // A's pair on B's document
		}},
	}
	return tables
}

// govTable is one fixture table and its rows.
type govTable struct {
	sqlTable
	rows [][]any
}

// govDeleted is a deleted_at value: a timestamp in each store.
func govDeleted() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// loadGoverned creates and fills the fixture's tables on a fresh database
// of b.
func loadGoverned(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.open(t)
	for _, tb := range governedTables() {
		b.load(t, db, tb.sqlTable, tb.rows)
	}
	return db
}

// A governed list holds its rows and each row's relations to the row's
// tenant, on each backend.
func TestGovernedLists(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		runGovernedLists(t, b.dialect, loadGoverned(t, b))
	})
}

// A backend holding rows in memory lists each case as each backend does.
func TestGovernedLists_Memory(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := loadGoverned(t, b)
		r := governedDocuments(t)
		runGovernedMemory(t, func(t *testing.T, filterJSON string) governedResult {
			return listGoverned(t, r, b.dialect, db, filterJSON, parse.Scope{Governance: governanceA()})
		})
	})
}

// A governed graph whose tenant columns mix a UUID and a string type. On
// PostgreSQL no operator compares uuid and text: the comparison is by the
// UUID's text.
func TestGovernedMixedTypes(t *testing.T) {
	runSQLBackends(t, runGovernedMixedTypes)
}

type governedCase struct {
	name   string
	filter string
	want   []string
}

func governedCases() []governedCase {
	cases := []governedCase{
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
	return cases
}

// governedResult is a list's page with its count, includes and facets.
type governedResult struct {
	ids      []string
	count    int64
	includes map[string]map[string][]string
	facets   map[string]map[string]int64
}

func governedIncludes() []string {
	relations := []string{"notes", "labels"}
	return relations
}

func listGoverned(t *testing.T, r *parse.Resource[govDocument], d filter.Dialect, db *sql.DB, filterJSON string, scope parse.Scope) governedResult {
	t.Helper()
	return listRelated(t, r, d, db, filterJSON, scope, governedIncludes())
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
		res.ids = append(res.ids, fmt.Sprint(key[0]))
	}
	slices.Sort(res.ids)
	require.NotNil(t, page.PageInfo.TotalCount)
	res.count = *page.PageInfo.TotalCount
	maps.Copy(res.includes, page.Relations)
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
	for _, tc := range governedCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := listGoverned(t, r, d, db, tc.filter, parse.Scope{Governance: governanceA()})
			assert.Equal(t, tc.want, got.ids)
			assert.Equal(t, int64(len(got.ids)), got.count, "the count is the page's")
			assert.Equal(t, facetsOfIncludes(got.ids, got.includes, governedIncludes()), got.facets, "the facets count the page's related rows")

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
	for _, tb := range governedTables() {
		for _, row := range tb.rows {
			m := map[string]any{}
			for i, c := range tb.columns {
				m[c.name] = row[i]
			}
			store[tb.name] = append(store[tb.name], m)
		}
	}
	for _, tc := range governedCases() {
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
		var keys []core.Key
		for _, row := range byTenant[tenant] {
			if f == nil || m.selects(t, f.AST(), r.Table(), row) {
				id, ok := row["id"].(string)
				require.True(t, ok)
				res.ids = append(res.ids, id)
				keys = append(keys, core.Key{id})
			}
		}
		for _, name := range governedIncludes() {
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
		out[i], err = governedProject(l.Table, row, lookupColumns(l))
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m governedMemory) LookupFilteredValues(ctx context.Context, l *filter.Lookup) ([]any, error) {
	return m.LookupValues(ctx, l)
}

func (m governedMemory) LookupPairs(_ context.Context, l *filter.Lookup, owner []string) (iter.Seq2[filter.IncludePair, error], error) {
	rows, err := m.selected(l)
	if err != nil {
		return nil, err
	}
	return func(yield func(filter.IncludePair, error) bool) {
		for _, row := range rows {
			o, err := governedProject(l.Table, row, owner)
			if err != nil {
				yield(filter.IncludePair{}, err)
				return
			}
			target, err := governedProject(l.Table, row, lookupColumns(l))
			if err != nil {
				yield(filter.IncludePair{}, err)
				return
			}
			if !yield(filter.IncludePair{Owner: o, Target: target}, nil) {
				return
			}
		}
	}, nil
}

// lookupColumns are the columns l projects.
func lookupColumns(l *filter.Lookup) []string {
	if l.ProjectKey != nil {
		return l.ProjectKey
	}
	columns := []string{l.Project}
	return columns
}

// governedProject is a row's value of its one column, or a core.Key of a
// value per column for several, as a lookup answers a projection.
func governedProject(table string, row map[string]any, columns []string) (any, error) {
	key := make(core.Key, len(columns))
	for i, c := range columns {
		v, ok := row[c]
		if !ok {
			return nil, fmt.Errorf("table %s has no column %q", table, c)
		}
		key[i] = v
	}
	if len(key) == 1 {
		return key[0], nil
	}
	return key, nil
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

func mixRelations() []string {
	relations := []string{"commands", "tags"}
	return relations
}

func mixedDevices[O, R any](prefix string) *parse.Resource[mixDevice[O]] {
	devices := parse.MustResource[mixDevice[O]](prefix+"devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[mixCommand[R]](prefix+"commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[mixTag[R]](prefix+"tags", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	devices.MustRelate("tags", parse.ManyToMany(tags,
		parse.Junction{Table: prefix + "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable()))
	return devices
}

// loadMixed creates and fills the graph under prefix on db: the devices'
// and pairs' tenant columns hold an O, the related tables' an R. d2 is U2's,
// granted; d3 U2's, not. c3 and the pair d2-g1 are U1's rows on U2's
// device.
func loadMixed[O, R any](t *testing.T, b sqlBackend, db *sql.DB, prefix string) {
	t.Helper()
	text := func(name string) sqlColumn { return column[string](name).required() }
	owner, related := column[O]("tenant_id").required(), column[R]("tenant_id").required()
	b.load(t, db, sqlTable{name: prefix + "devices", key: []string{"id"}, columns: []sqlColumn{text("id"), owner}},
		[][]any{{"d1", mixU1}, {"d2", mixU2}, {"d3", mixU2}})
	b.load(t, db, sqlTable{name: prefix + "commands", key: []string{"id"}, columns: []sqlColumn{text("id"), related, text("device_id"), text("status")}},
		[][]any{{"c1", mixU1, "d1", "sent"}, {"c2", mixU2, "d2", "sent"}, {"c3", mixU1, "d2", "sent"}, {"c4", mixU2, "d3", "sent"}})
	b.load(t, db, sqlTable{name: prefix + "tags", key: []string{"id"}, columns: []sqlColumn{text("id"), related, text("name")}},
		[][]any{{"g1", mixU1, "hot"}, {"g2", mixU2, "hot"}})
	b.load(t, db, sqlTable{name: prefix + "device_tags", columns: []sqlColumn{text("device_id"), text("tag_id"), owner}},
		[][]any{{"d1", "g1", mixU1}, {"d2", "g2", mixU2}, {"d2", "g1", mixU1}})
}

// runGovernedMixedTypes lists a governed graph whose tenant columns mix a
// UUID and a string type, both ways round.
func runGovernedMixedTypes(t *testing.T, b sqlBackend) {
	t.Helper()
	db := b.open(t)
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
		assert.Equal(t, facetsOfIncludes(got.ids, got.includes, mixRelations()), got.facets)
	}

	t.Run("uuid owner, string related tables", func(t *testing.T) {
		loadMixed[uuid.UUID, string](t, b, db, "u_")
		r := mixedDevices[uuid.UUID, string]("u_")
		check(t, func(f string) governedResult {
			return listRelated(t, r, b.dialect, db, f, parse.Scope{Governance: governance}, mixRelations())
		})
	})
	t.Run("string owner, uuid related tables", func(t *testing.T) {
		loadMixed[string, uuid.UUID](t, b, db, "s_")
		r := mixedDevices[string, uuid.UUID]("s_")
		check(t, func(f string) governedResult {
			return listRelated(t, r, b.dialect, db, f, parse.Scope{Governance: governance}, mixRelations())
		})
	})
}
