//go:build integration

package corpus

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/patch"
)

func mergePatchMapKeys(t *testing.T, engine Engine) {
	runOn(t, engine, runMapKeyMerges)
}

// runMapKeyMerges updates a stored row with each merge patch and reads back
// what encoding/json makes of the same members over the row, the in-memory
// application's result too.
func runMapKeyMerges(t *testing.T, b sqlBackend) {
	document := column[map[string]any]
	table := sqlTable{name: "keyed_maps", key: []string{"id"}, columns: []sqlColumn{
		column[int64]("id").required(), document("by_int").required(), document("by_uint").required(), document("by_addr").required(),
	}}
	for _, tc := range parsetest.KeyedMapMerges() {
		t.Run(tc.Name, func(t *testing.T) {
			provider, db := b.open(t)
			r := parse.MustResource[parsetest.KeyedMaps](provider, "keyed_maps")
			row := parsetest.KeyedMapsRow()
			stored := []any{int64(1)}
			for _, m := range []any{row.ByInt, row.ByUint, row.ByAddr} {
				raw, err := json.Marshal(m)
				require.NoError(t, err)
				stored = append(stored, string(raw))
			}
			b.load(t, db, table, [][]any{stored})

			parsed, err := r.ParseMergePatch([]byte(tc.Body))
			require.NoError(t, err)
			res, err := applyUpdate(t, r, false, parsed, parse.ByID(int64(1)), parse.Scope{})
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)

			want := parsetest.MergedByEncodingJSON(t, parsetest.KeyedMapsRow(), tc.Body)
			assert.Equal(t, want, res.Changed[0])
			inMemory, err := r.ApplyValidatedPatch(parsed, parsetest.KeyedMapsRow())
			require.NoError(t, err)
			assert.Equal(t, inMemory, res.Changed[0])
		})
	}
}

func updateMembers(t *testing.T, engine Engine) {
	runOn(t, engine, runMemberUpdates)
}

// runMemberUpdates updates gadgets' documents member by member through
// BuildUpdate and the dialect's executor: the rows equal what
// ApplyValidatedPatch makes of them, and a patch a target row's document
// does not take fails as ApplyValidatedPatch fails it, writing no row.
func runMemberUpdates(t *testing.T, b sqlBackend) {
	rows := []parsetest.GadgetAttrs{
		{ID: 1, Attrs: map[string]any{"color": "blue", "old": true, "n": map[string]any{"k": "v"}}},
		{ID: 2, Attrs: map[string]any{"color": "green"}},
	}
	// setup seeds a fresh database of b with rows and returns the resource
	// on it.
	setup := func(t *testing.T) *parse.Resource[parsetest.GadgetAttrs] {
		t.Helper()
		provider, db := b.open(t)
		r := parsetest.GadgetAttrsResource(t, provider)
		stored := make([][]any, len(rows))
		for i, row := range rows {
			raw, err := json.Marshal(row.Attrs)
			require.NoError(t, err)
			stored[i] = []any{row.ID, string(raw)}
		}
		b.load(t, db, sqlTable{name: "gadget_attrs", key: []string{"id"}, columns: []sqlColumn{
			column[int64]("id").required(), column[map[string]any]("attrs").required(),
		}}, stored)
		return r
	}
	stored := func(t *testing.T, r *parse.Resource[parsetest.GadgetAttrs]) []parsetest.GadgetAttrs {
		t.Helper()
		req, err := r.ParseList(nil)
		require.NoError(t, err)
		return listPage(t, r, req, parse.Scope{}).Items
	}
	jsonOf := func(t *testing.T, v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		return string(raw)
	}

	t.Run("members written in one statement", func(t *testing.T) {
		r := setup(t)
		parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"},{"op":"add","path":"/attrs/n/size","value":{"w":2}},{"op":"remove","path":"/attrs/old"}]`))
		require.NoError(t, err)
		res, err := applyUpdate(t, r, false, parsed, parse.ByID(int64(1)), parse.Scope{})
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		want, err := r.ApplyValidatedPatch(parsed, rows[0])
		require.NoError(t, err)
		assert.JSONEq(t, jsonOf(t, want), jsonOf(t, res.Changed[0]))
		assert.JSONEq(t, jsonOf(t, []parsetest.GadgetAttrs{want, rows[1]}), jsonOf(t, stored(t, r)))
	})

	for _, tc := range []struct {
		name    string
		ops     string
		target  parse.Target
		failing int // the index in rows of the target row the patch fails on
		unmet   int
		path    string
	}{
		{"the operation a row fails is the update's error", `[{"op":"replace","path":"/attrs/color","value":"x"},{"op":"remove","path":"/attrs/zz"}]`, parse.ByID(int64(1)), 0, 1, "/attrs/zz"},
		{"a replace of a member the row lacks", `[{"op":"replace","path":"/attrs/size","value":"red"}]`, parse.ByID(int64(2)), 1, 0, "/attrs/size"},
		{"one target row of two fails, and neither is written", `[{"op":"remove","path":"/attrs/old"}]`, parse.ByIDs(int64(1), int64(2)), 1, 0, "/attrs/old"},
		{"an add below a member that is not an object", `[{"op":"add","path":"/attrs/color/x","value":1}]`, parse.ByID(int64(2)), 1, 0, "/attrs/color/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t)
			parsed, err := r.ParsePatch([]byte(tc.ops))
			require.NoError(t, err)
			_, err = applyUpdate(t, r, false, parsed, tc.target, parse.Scope{})
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
			require.NotNil(t, pe.Index)
			assert.Equal(t, tc.unmet, *pe.Index)
			assert.Equal(t, tc.path, pe.Path)
			assert.JSONEq(t, jsonOf(t, rows), jsonOf(t, stored(t, r)), "no row is written")

			_, err = r.ApplyValidatedPatch(parsed, rows[tc.failing])
			memory, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "ApplyValidatedPatch: %v is no ParseError", err)
			assert.Equal(t, pe.Code, memory.Code)
			assert.Equal(t, *pe.Index, *memory.Index)
		})
	}

	t.Run("a member set to the value it holds is unchanged", func(t *testing.T) {
		r := setup(t)
		parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"blue"}]`))
		require.NoError(t, err)
		res, err := applyUpdate(t, r, false, parsed, parse.ByID(int64(1)), parse.Scope{}, parse.SkipUnchanged())
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		require.Len(t, res.Unchanged, 1)
		assert.JSONEq(t, jsonOf(t, rows[0]), jsonOf(t, res.Unchanged[0]))
	})
}

type tuning struct {
	Mode  string `json:"mode"  parse:"filter"`
	Level int64  `json:"level" parse:"filter,sort"`
}

type tunedItem struct {
	ID       string `json:"id"       db:"id"       parse:"filter"`
	Settings tuning `json:"settings" db:"settings" parse:"filter,patch"`
}

// A merged typed-document member reads back through its path on each
// backend: the canonical typed document (J02) and the path rule agree.
func pathPatches(t *testing.T, engine Engine) {
	runOn(t, engine, runPathPatches)
}

func runPathPatches(t *testing.T, b sqlBackend) {
	t.Helper()
	provider, db := b.open(t)
	b.load(t, db, sqlTable{name: "tuned_items", columns: []sqlColumn{column[string]("id"), column[tuning]("settings")}, key: []string{"id"}},
		[][]any{{"a", `{"mode":"auto","level":2}`}, {"b", `{"mode":"manual","level":5}`}})
	r, err := parse.NewResource[tunedItem](provider, "tuned_items")
	require.NoError(t, err)

	ids := func(f string) []string {
		req, err := r.ParseList(url.Values{"filter": {f}})
		require.NoError(t, err)
		return parsetest.ItemIDs(listPage(t, r, req, parse.Scope{}).Items, func(it tunedItem) string { return it.ID })
	}

	p, err := r.ParseMergePatch([]byte(`{"settings":{"level":9}}`))
	require.NoError(t, err)
	_, err = applyUpdate(t, r, false, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)

	assert.Equal(t, []string{"a"}, ids(`{"settings.level":9}`), "the patched member reads back through its path")
	assert.Equal(t, []string{"a"}, ids(`{"settings.mode":"auto"}`), "its sibling survives the merge")
	assert.Equal(t, []string{"b"}, ids(`{"settings.level":5}`), "another row is untouched")

	_, err = r.ParseMergePatch([]byte(`{"settings":{"bogus":1}}`))
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "a member the type does not declare is refused, not stored")
}

// mergePrefs is a typed document as the TypeScript SDK's types.test.ts
// declares it: City, Units and Note always written (Note as null when
// nil), Label left out when empty.
type mergePrefs struct {
	City  string  `json:"city"`
	Units string  `json:"units"`
	Label string  `json:"label,omitempty"`
	Note  *string `json:"note"`
}

type mergePrefsRow struct {
	ID    string     `json:"id"    db:"id"`
	Prefs mergePrefs `json:"prefs" db:"prefs" parse:"patch"`
}

func typedMergePatchPreservesSiblings(t *testing.T, engine Engine) {
	runOn(t, engine, runTypedMergePatch)
}

// runTypedMergePatch serves the merge patches the TypeScript SDK's typed
// calls send (sdk/ts/src/patch/types.test.ts) as a parse server does:
// parsehttp.ParsePatch reads the body, BuildUpdate and the dialect's
// executor write it. A partial document changes the members it names and
// keeps its siblings, in the row the update returns, in the stored row and
// in ApplyValidatedPatch alike. null removes a member the document may
// leave out and is refused on one its type always writes, which is the
// rule the SDK's MergeDocument type states.
func runTypedMergePatch(t *testing.T, b sqlBackend) {
	resource := func(t *testing.T, p *parse.Provider) *parse.Resource[mergePrefsRow] {
		t.Helper()
		r, err := parse.NewResource[mergePrefsRow](p, "merge_prefs")
		require.NoError(t, err)
		return r
	}
	initial := mergePrefsRow{ID: "one", Prefs: mergePrefs{City: "York", Units: "metric", Label: "home"}}

	for _, tc := range []struct {
		name, body string
		want       mergePrefs
	}{
		{"one member, its siblings kept", `{"prefs":{"city":"Bath"}}`, mergePrefs{City: "Bath", Units: "metric", Label: "home"}},
		{"a complete document", `{"prefs":{"city":"Bath","units":"imperial","label":"work"}}`, mergePrefs{City: "Bath", Units: "imperial", Label: "work"}},
		{"a member the document may leave out, removed", `{"prefs":{"label":null}}`, mergePrefs{City: "York", Units: "metric"}},
		{"a member written as null, given a value", `{"prefs":{"note":"call"}}`, mergePrefs{City: "York", Units: "metric", Label: "home", Note: new("call")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, db := b.open(t)
			r := resource(t, provider)
			stored, err := json.Marshal(initial.Prefs)
			require.NoError(t, err)
			b.load(t, db, sqlTable{name: "merge_prefs", key: []string{"id"}, columns: []sqlColumn{
				column[string]("id").required(), column[mergePrefs]("prefs").required(),
			}}, [][]any{{initial.ID, string(stored)}})

			parsed, err := parsehttp.ParsePatch(sdkPatchRequest(t, tc.body), r)
			require.NoError(t, err)
			res, err := applyUpdate(t, r, false, parsed, parse.ByID(initial.ID), parse.Scope{})
			require.NoError(t, err)
			want := mergePrefsRow{ID: initial.ID, Prefs: tc.want}
			assert.Equal(t, []mergePrefsRow{want}, res.Changed)

			req, err := r.ParseList(nil)
			require.NoError(t, err)
			assert.Equal(t, []mergePrefsRow{want}, listPage(t, r, req, parse.Scope{}).Items)

			applied, err := r.ApplyValidatedPatch(parsed, initial)
			require.NoError(t, err)
			assert.Equal(t, want, applied)
		})
	}

	for _, tc := range []struct{ name, body string }{
		{"a member the document always writes takes no null", `{"prefs":{"units":null}}`},
		{"nor does one it writes as null", `{"prefs":{"note":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, _ := b.open(t)
			_, err := parsehttp.ParsePatch(sdkPatchRequest(t, tc.body), resource(t, provider))
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
		})
	}
}

// sdkPatchRequest is the request the SDK's patch sends: body as its JSON
// Merge Patch, Content-Type application/json.
func sdkPatchRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/merge_prefs/one", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func updateBatchDatabase(t *testing.T, engine Engine) {
	runOn(t, engine, testUpdateBatchDatabase)
}

func testUpdateBatchDatabase(t *testing.T, b sqlBackend) {
	t.Helper()
	provider, db := b.open(t)
	r := parsetest.UpdateBatchResource(t, provider)
	if b.engine != Memory {
		jsonType := b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})
		dbtest.Exec(t, db, `CREATE TABLE batch_rows (id BIGINT PRIMARY KEY,tenant_id TEXT NOT NULL,reference TEXT CHECK(reference <> 'refused'),version BIGINT NOT NULL,meta `+jsonType+` NOT NULL,updated_at TEXT NOT NULL,deleted_at TEXT)`)
	}
	table := sqlTable{name: "batch_rows", key: []string{"id"}, columns: []sqlColumn{
		column[int64]("id").required(), column[string]("tenant_id").required(), column[*string]("reference"), column[int64]("version").required(),
		column[map[string]any]("meta").required(), column[string]("updated_at").required(), column[*string]("deleted_at"),
	}}
	var seeds [][]any
	for id := int64(1); id <= 42; id++ {
		tenant := "t1"
		var deleted *string
		if id == 41 {
			tenant = "t2"
		}
		if id == 42 {
			deleted = new("deleted")
		}
		seeds = append(seeds, []any{id, tenant, fmt.Sprintf("old-%d", id), int64(3), `{"value":"old","count":9}`, "old", deleted})
	}
	b.insert(t, db, table, seeds...)
	stamp := time.Date(2026, time.November, 8, 17, 0, 0, 0, time.UTC)
	var requests []parse.RowUpdate
	for id := int64(40); id >= 1; id-- {
		raw := fmt.Sprintf(`{"reference":"new-%d","meta":{"value":"new-%d"}}`, id, id)
		if id == 3 {
			raw = `{"reference":"old-3","meta":{"value":"old"}}`
		}
		if id == 4 {
			raw = `{}`
		}
		if id == 5 {
			raw = `{"reference":null}`
		}
		requests = append(requests, parse.RowUpdate{Key: parse.Key{id}, Patch: parsetest.UpdateBatchPatch(t, r, raw), Policy: updateBatchVersion(3)})
	}
	opts := []parse.UpdateOption{parse.SkipUnchanged(), parse.Stamp("updated_at", stamp), parse.Increment("version")}
	limits := parse.UpdateBatchLimits{MaxParameters: 40}
	if b.engine != Memory {
		plan, err := r.BuildUpdateBatch(requests, parse.Scope{Tenant: "t1"}, limits, opts...)
		require.NoError(t, err)
		require.Greater(t, len(plan.Chunks), 1)
		require.Less(t, len(plan.Chunks), len(requests))
	}
	res, err := r.UpdateBatch(t.Context(), requests, parse.Scope{Tenant: "t1"}, limits, opts...)
	require.NoError(t, err)
	require.Len(t, res.Changed, 38)
	for _, row := range res.Changed {
		assert.Equal(t, "t1", row.TenantID)
		assert.Equal(t, int64(4), row.Version)
		assert.Equal(t, core.FormatTimestamp(stamp), row.UpdatedAt)
		assert.Equal(t, 9, row.Meta.Count)
		if row.ID == 5 {
			assert.Nil(t, row.Reference)
		} else {
			require.NotNil(t, row.Reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.ID), *row.Reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.ID), row.Meta.Value)
		}
	}
	unchanged := make([]int64, len(res.Unchanged))
	for i, row := range res.Unchanged {
		unchanged[i] = row.ID
		assert.Equal(t, int64(3), row.Version, "row %d keeps its version", row.ID)
	}
	assert.ElementsMatch(t, []int64{3, 4}, unchanged, "3 already holds its patch; 4's patch is empty")
	assert.Zero(t, res.RelationRows)
	for _, row := range b.tableRows(t, db, "batch_rows", "id", "version", "updated_at") {
		id := asInt64(t, row[0])
		if slices.Contains([]int64{3, 4, 41, 42}, id) {
			assert.Equal(t, int64(3), asInt64(t, row[1]), "row %d's version", id)
			assert.Equal(t, "old", columnText(t, row[2]), "row %d's stamp", id)
		}
	}
	for _, test := range []struct {
		name, raw, reference string
		key, version         int64
		opt                  parse.UpdateOption
	}{
		{"server-counter-owns-value", `{"reference":"counter-control","version":777}`, "counter-control", 3, 4, parse.Increment("version")},
		{"server-stamp-owns-value", `{"reference":"stamp-conflict","meta":{"value":"stamp-control"}}`, "server-stamp", 4, 3, parse.StampSQL("reference", "'server-stamp'")},
	} {
		t.Run(test.name, func(t *testing.T) {
			res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{test.key}, Patch: parsetest.UpdateBatchPatch(t, r, test.raw)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, test.opt)
			if b.engine == Memory && test.name == "server-stamp-owns-value" {
				require.ErrorIs(t, err, parse.ErrUnsupported, "the memory provider evaluates no StampSQL")
				return
			}
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)
			assert.Empty(t, res.Unchanged)
			assert.Equal(t, test.version, res.Changed[0].Version)
			require.NotNil(t, res.Changed[0].Reference)
			assert.Equal(t, test.reference, *res.Changed[0].Reference)
		})
	}
	for _, test := range []struct {
		name string
		key  int64
		rev  int64
	}{
		{"stale", 2, 3}, {"missing", 99, 4}, {"foreign", 41, 3}, {"deleted", 42, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{
				{Key: parse.Key{int64(1)}, Patch: parsetest.UpdateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
				{Key: parse.Key{test.key}, Patch: parsetest.UpdateBatchPatch(t, r, `{"reference":"also-refused"}`), Policy: updateBatchVersion(test.rev)},
			}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
			require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
			assert.Zero(t, res)
			assert.Equal(t, "new-1", storedReference(t, b, db, 1), "no chunk wrote")
		})
	}
	t.Run("late-constraint-rolls-back-earlier-chunk", func(t *testing.T) {
		b.sqlOnly(t, "refuses the second owner's write by a CHECK constraint, which a memory table does not hold")
		res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{
			{Key: parse.Key{int64(1)}, Patch: parsetest.UpdateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
			{Key: parse.Key{int64(2)}, Patch: parsetest.UpdateBatchPatch(t, r, `{"reference":"refused"}`), Policy: updateBatchVersion(4)},
		}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
		require.Error(t, err)
		require.NotErrorIs(t, err, parse.ErrUpdateBatchTargets, "every target was found; the second owner failed")
		assert.Zero(t, res)
		assert.Equal(t, "new-1", storedReference(t, b, db, 1), "the first chunk's write is undone")
	})
}

func storedReference(t *testing.T, b sqlBackend, db *sql.DB, id int64) string {
	t.Helper()
	references := columnOf(t, b, db, "batch_rows", "reference", "id", strconv.FormatInt(id, 10))
	require.Len(t, references, 1)
	return references[0]
}

func updateBatchVersion(version int64) *filter.PolicyConstraints {
	policy := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "version", Operator: filter.OpEq, Value: version}}}
	return policy
}

func gadgetTable() sqlTable {
	table := sqlTable{name: "gadgets", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[string]("tenant_id").required(), column[string]("name"), column[int64]("rank"),
		column[int64]("version").required(), column[time.Time]("updated_at"), column[time.Time]("deleted_at"),
	}}
	return table
}

func gadgetTagTable() sqlTable {
	table := sqlTable{name: "gadget_tags", key: []string{"gadget_id", "tag_id", "tenant_id"}, columns: []sqlColumn{
		column[string]("gadget_id").required(), column[string]("tag_id").required(), column[string]("tenant_id").required(),
	}}
	return table
}

// loadGadgets seeds a fresh database of b, returned with its provider: t1
// holds g1, g2 and a deleted g3, t2 holds h1. Tags pair in each row's
// tenant.
func loadGadgets(t *testing.T, b sqlBackend) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	b.load(t, db, gadgetTable(), [][]any{
		{"g1", "t1", "one", 1, 1, nil, nil},
		{"g2", "t1", "two", 1, 1, nil, nil},
		{"g3", "t1", "three", 1, 1, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"h1", "t2", "other", 1, 1, nil, nil},
	})
	b.load(t, db, gadgetTagTable(), [][]any{{"g1", "red", "t1"}, {"h1", "red", "t2"}})
	return p, db
}

func gadgetID(g parsetest.Gadget) string { return g.ID }

func gadgetNames(t *testing.T, b sqlBackend, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range b.tableRows(t, db, "gadgets", "id", "name") {
		out[columnText(t, row[0])] = columnText(t, row[1])
	}
	return out
}

func tagPairs(t *testing.T, b sqlBackend, db *sql.DB) []string {
	t.Helper()
	var pairs []string
	for _, row := range b.tableRows(t, db, "gadget_tags", "gadget_id", "tag_id", "tenant_id") {
		pairs = append(pairs, columnText(t, row[0])+":"+columnText(t, row[1])+"@"+columnText(t, row[2]))
	}
	slices.Sort(pairs)
	return pairs
}

// ownerRows are the rows an update of target with p reports: on a SQL
// engine, those its plan's owner statement returns, run alone
// (BuildUpdate); on the Memory engine, which compiles no statement, those
// the update itself reports, changed and unchanged.
func ownerRows(t *testing.T, b sqlBackend, r *parse.Resource[parsetest.Gadget], p *patch.ParsedPatch, target parse.Target, scope parse.Scope, opts ...parse.UpdateOption) []parsetest.Gadget {
	t.Helper()
	if b.engine == Memory {
		res, err := r.Update(t.Context(), p, target, scope, opts...)
		require.NoError(t, err)
		return append(res.Changed, res.Unchanged...)
	}
	plan, err := r.BuildUpdate(p, target, scope, opts...)
	require.NoError(t, err)
	require.True(t, plan.Writes)
	return parsetest.ResourceRows(t, r, plan.Owner)
}

func byFilter(t *testing.T, r *parse.Resource[parsetest.Gadget], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

func nameIs(name string) *filter.PolicyConstraints {
	policy := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "name", Operator: filter.OpEq, Value: name}}}
	return policy
}

func buildUpdateChangesOnlyTheScopedTargetRows(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		for name, tc := range map[string]struct {
			target func(r *parse.Resource[parsetest.Gadget]) parse.Target
			scope  parse.Scope
			want   []string
		}{
			"by key":                    {func(*parse.Resource[parsetest.Gadget]) parse.Target { return parse.ByID("g1") }, parse.Scope{Tenant: "t1"}, []string{"g1"}},
			"by keys":                   {func(*parse.Resource[parsetest.Gadget]) parse.Target { return parse.ByIDs("g1", "g2") }, parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
			"another tenant's key":      {func(*parse.Resource[parsetest.Gadget]) parse.Target { return parse.ByID("h1") }, parse.Scope{Tenant: "t1"}, nil},
			"a deleted row":             {func(*parse.Resource[parsetest.Gadget]) parse.Target { return parse.ByID("g3") }, parse.Scope{Tenant: "t1"}, nil},
			"by filter, in its tenant":  {func(r *parse.Resource[parsetest.Gadget]) parse.Target { return byFilter(t, r, `{"rank":1}`) }, parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
			"policy narrows the target": {func(*parse.Resource[parsetest.Gadget]) parse.Target { return parse.ByIDs("g1", "g2") }, parse.Scope{Tenant: "t1", Policy: nameIs("two")}, []string{"g2"}},
		} {
			t.Run(name, func(t *testing.T) {
				provider, db := loadGadgets(t, b)
				r := parsetest.Gadgets(t, provider)
				p, err := r.ParseMergePatch([]byte(`{"name":"renamed","rank":7}`))
				require.NoError(t, err)
				got := ownerRows(t, b, r, p, tc.target(r), tc.scope)
				assert.Equal(t, tc.want, sortedIDs(got, gadgetID), "the rows the update reports")
				for _, g := range got {
					assert.Equal(t, "renamed", g.Name)
					assert.Equal(t, int64(7), g.Rank)
				}
				for id, name := range gadgetNames(t, b, db) {
					if slices.Contains(tc.want, id) {
						assert.Equal(t, "renamed", name, id)
					} else {
						assert.NotEqual(t, "renamed", name, "%s is outside the target or scope", id)
					}
				}
			})
		}
	})
}

// A stamp is the time given, or an expression the database evaluates: its
// clock here. The memory provider evaluates no expression.
func buildUpdateStamps(t *testing.T, engine Engine) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, _ := loadGadgets(t, b)
		r := parsetest.Gadgets(t, provider)
		p, err := r.ParseMergePatch([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		got := ownerRows(t, b, r, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at))
		require.Len(t, got, 1)
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "stamped %v", *got[0].UpdatedAt)

		if b.engine == Memory {
			_, err := r.Update(t.Context(), p, parse.ByIDs("g2", "h1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "CURRENT_TIMESTAMP"))
			require.ErrorIs(t, err, parse.ErrUnsupported)
		} else {
			got = ownerRows(t, b, r, p, parse.ByIDs("g2", "h1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", b.clock()))
			require.Len(t, got, 1, "h1 is t2's")
			require.NotNil(t, got[0].UpdatedAt)
			assert.WithinDuration(t, time.Now(), *got[0].UpdatedAt, time.Minute, "the database clock")
		}

		rel, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
		require.NoError(t, err)
		got = ownerRows(t, b, r, rel, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at.Add(time.Hour)))
		require.Len(t, got, 1)
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "a relation change alone writes no column, so it stamps none")
	})
}

type revision struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"    parse:"filter,server"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
}

// An incremented version moves one past each row's own and is returned
// moved; no patch sets it, the server's own included.
func buildUpdateIncrements(t *testing.T, engine Engine) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := b.open(t)
		r, err := parse.NewResource[revision](provider, "revisions", parse.TenantColumn("tenant_id"))
		require.NoError(t, err)
		b.load(t, db, sqlTable{name: "revisions", key: []string{"id"}, columns: []sqlColumn{
			column[string]("id"), column[string]("tenant_id").required(), column[string]("name"), column[int64]("version").required(), column[time.Time]("updated_at"),
		}}, [][]any{{"r1", "t1", "one", 1, nil}, {"r2", "t1", "two", 1, nil}})
		update := func(t *testing.T, p *patch.ParsedPatch, target parse.Target) []revision {
			t.Helper()
			res, err := applyUpdate(t, r, false, p, target, parse.Scope{Tenant: "t1"},
				parse.Increment("version"), parse.Stamp("updated_at", at))
			require.NoError(t, err)
			assert.Empty(t, res.Unchanged, "a plain update writes every target")
			return res.Changed
		}

		rename, err := r.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		got := update(t, rename, parse.ByID("r1"))
		require.Len(t, got, 1)
		assert.Equal(t, int64(2), got[0].Version, "RETURNING reports the moved version")
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "the stamp still applies")

		got = update(t, rename, parse.ByIDs("r1", "r2"))
		require.Len(t, got, 2)
		versions := map[string]int64{got[0].ID: got[0].Version, got[1].ID: got[1].Version}
		assert.Equal(t, map[string]int64{"r1": 3, "r2": 2}, versions, "each row moves one past its own version")

		server, err := r.ServerMergePatchMap(map[string]any{"name": "server", "version": 40})
		require.NoError(t, err)
		got = update(t, server, parse.ByID("r2"))
		require.Len(t, got, 1)
		assert.Equal(t, int64(3), got[0].Version, "the server's own patch does not set the version either")
		assert.Equal(t, "server", got[0].Name)
	})
}

func buildUpdateRelationsChangeOnlyTheRowsTheOwnerMatched(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := loadGadgets(t, b)
		r := parsetest.Gadgets(t, provider)
		p, err := r.ParseMergePatch([]byte(`{"tags":["blue","green"]}`))
		require.NoError(t, err)
		if b.engine != Memory {
			plan, err := r.BuildUpdate(p, parse.ByIDs("g1", "h1", "g3"), parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			assert.True(t, plan.Writes)
			assert.True(t, plan.HasRelations())
			assert.True(t, strings.HasPrefix(plan.Owner.SQL, "SELECT "), "a relation-only patch selects its owners")
		}

		res, err := applyUpdate(t, r, false, p, parse.ByIDs("g1", "h1", "g3"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		assert.Equal(t, []string{"g1"}, sortedIDs(res.Unchanged, gadgetID), "h1 is t2's, g3 is deleted")
		assert.Equal(t, int64(3), res.RelationRows, "red out of g1, blue and green in")
		assert.Equal(t, []string{"g1:blue@t1", "g1:green@t1", "h1:red@t2"}, tagPairs(t, b, db), "the requested h1 is not the matched one")

		none, err := applyUpdate(t, r, false, p, parse.ByIDs("h1", "g3"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.Empty(t, none.Changed)
		assert.Empty(t, none.Unchanged)
		assert.Zero(t, none.RelationRows, "no matched owners, no junction writes")
		assert.Equal(t, []string{"g1:blue@t1", "g1:green@t1", "h1:red@t2"}, tagPairs(t, b, db))
	})
}

func buildUpdateColumnsAndRelationsTogether(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := loadGadgets(t, b)
		r := parsetest.Gadgets(t, provider)
		p, err := r.ParseMergePatch([]byte(`{"name":"both","tags":["blue"]}`))
		require.NoError(t, err)
		if b.engine != Memory {
			plan, err := r.BuildUpdate(p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(plan.Owner.SQL, `UPDATE "gadgets" SET `))
			assert.True(t, plan.HasRelations())
		}
		res, err := applyUpdate(t, r, false, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		assert.Equal(t, "both", res.Changed[0].Name)
		assert.Equal(t, "both", gadgetNames(t, b, db)["g1"])
		assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, tagPairs(t, b, db))
	})
}

func buildUpdateAnEmptyPatchIsAScopedRead(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, _ := loadGadgets(t, b)
		r := parsetest.Gadgets(t, provider)
		p, err := r.ParseMergePatch([]byte(`{}`))
		require.NoError(t, err)
		if b.engine != Memory {
			plan, err := r.BuildUpdate(p, parse.ByIDs("g1", "h1"), parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			assert.False(t, plan.Writes)
			assert.False(t, plan.HasRelations())
		}
		res, err := applyUpdate(t, r, false, p, parse.ByIDs("g1", "h1"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		assert.Equal(t, []string{"g1"}, sortedIDs(res.Unchanged, gadgetID))
	})
}

// A relation change's owner statement locks its owner rows until the
// update's transaction ends, so a second writer waits for them. PostgreSQL
// alone locks rows: SQLite's write transactions serialize.
func buildUpdateRelationChangeLocksItsOwners(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	provider, db := loadGadgets(t, b)
	r := parsetest.Gadgets(t, provider)
	ctx := t.Context()
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)

	// The owner statement runs in a transaction held open, so a second
	// writer can run while it holds the owner rows.
	tx, err := provider.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(ctx)) }()
	scoped, err := r.In(tx)
	require.NoError(t, err)
	owners, err := scoped.Query(ctx, plan.Owner)
	require.NoError(t, err)
	require.Len(t, owners, 1)

	other, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = other.ExecContext(ctx, `SET LOCAL lock_timeout = '200ms'`)
	require.NoError(t, err)
	_, err = other.ExecContext(ctx, plan.Owner.SQL, plan.Owner.Args...)
	var pgErr sqlStateError
	require.ErrorAs(t, err, &pgErr, "a second writer waits for the owner rows")
	assert.Equal(t, "55P03", pgErr.SQLState(), "lock_not_available")
	require.NoError(t, other.Rollback())

	res, err := scoped.Update(ctx, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"g1"}, sortedIDs(res.Unchanged, gadgetID))
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, tagPairs(t, b, db))
}

func noteTable() sqlTable {
	table := sqlTable{name: "notes", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[string]("tenant_id").required(), column[string]("title"), column[int64]("rank").required(),
		column[[]string]("labels").required(), column[map[string]any]("attrs").required(), column[int64]("version").required(),
		column[time.Time]("updated_at"), column[time.Time]("deleted_at"),
	}}
	return table
}

func noteTagTable() sqlTable {
	table := sqlTable{name: "note_tags", key: []string{"note_id", "tag_id"}, columns: []sqlColumn{
		column[string]("note_id"), column[string]("tag_id"), column[string]("tenant_id").required(),
	}}
	return table
}

// loadNotes seeds a fresh database of b and returns the notes resource on
// it with the database: t1 holds n1 and n2 (title "one" and "two", rank 1,
// labels a and b) and a deleted n3, t2 holds m1, and n1 is tagged red. n1's
// attrs hold their keys out of order.
func loadNotes(t *testing.T, b sqlBackend) (*parse.Resource[parsetest.Note], *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	ab := []string{"a", "b"}
	b.load(t, db, noteTable(), [][]any{
		{"n1", "t1", "one", 1, ab, `{"b":[2],"a":1}`, 1, nil, nil},
		{"n2", "t1", "two", 1, ab, `{"a":1}`, 1, nil, nil},
		{"n3", "t1", "gone", 1, []string{}, `{}`, 1, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"m1", "t2", "other", 1, []string{}, `{}`, 1, nil, nil},
	})
	b.load(t, db, noteTagTable(), [][]any{{"n1", "red", "t1"}})
	return parsetest.Notes(t, p), db
}

func noteID(n parsetest.Note) string { return n.ID }

// skipApply applies body, a merge patch, to target in tenant t1 with opts,
// parsetest.SkipOptions when there are none.
func skipApply(t *testing.T, r *parse.Resource[parsetest.Note], body string, target parse.Target, opts ...parse.UpdateOption) parse.UpdateResult[parsetest.Note] {
	t.Helper()
	if len(opts) == 0 {
		opts = parsetest.SkipOptions()
	}
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	res, err := applyUpdate(t, r, false, p, target, parse.Scope{Tenant: "t1"}, opts...)
	require.NoError(t, err)
	return res
}

// applied applies a guarded update (skipApply), whose plan reads its
// targets first on a SQL engine, and splits the target rows.
func applied(t *testing.T, b sqlBackend, r *parse.Resource[parsetest.Note], body string, target parse.Target, opts ...parse.UpdateOption) (changed, unchanged []parsetest.Note) {
	t.Helper()
	if b.engine != Memory {
		require.NotNil(t, parsetest.SkipUpdate(t, r, body, target, opts...).Current)
	}
	res := skipApply(t, r, body, target, opts...)
	return res.Changed, res.Unchanged
}

// storedNote is the note of id as the table holds it, whatever its tenant
// or deletion: on the Memory engine, read from the store, a value per
// column as the resource scans them.
func storedNote(t *testing.T, b sqlBackend, db *sql.DB, r *parse.Resource[parsetest.Note], id string) parsetest.Note {
	t.Helper()
	if b.engine == Memory {
		rows, err := storeOf(t, db).Rows(t.Context(), "notes")
		require.NoError(t, err)
		for _, row := range rows {
			if row["id"] == id {
				return scannedNote(t, row, r.Columns())
			}
		}
		require.Fail(t, "no note "+id)
	}
	got := parsetest.ResourceRows(t, r, parse.Statement{SQL: `SELECT ` + parsetest.JoinColumns(r.Columns()) + ` FROM notes WHERE id = ` + b.dialect.Placeholder(1), Args: []any{id}})
	require.Len(t, got, 1)
	return got[0]
}

// scannedNote is a note's stored row as its resource scans it: of the
// columns parsetest.Note holds, those the resource reads (columns), each
// through database/sql's conversion, a document or an array from its JSON.
func scannedNote(t *testing.T, row map[string]any, columns []string) parsetest.Note {
	t.Helper()
	var n parsetest.Note
	n.ID, n.TenantID = columnText(t, row["id"]), columnText(t, row["tenant_id"])
	if row["title"] != nil {
		n.Title = new(columnText(t, row["title"]))
	}
	n.Rank, n.Version = asInt64(t, row["rank"]), asInt64(t, row["version"])
	for column, into := range map[string]any{"labels": &n.Labels, "attrs": &n.Attrs} {
		if slices.Contains(columns, column) {
			require.NoError(t, json.Unmarshal([]byte(columnText(t, row[column])), into), column)
		}
	}
	for column, into := range map[string]**time.Time{"updated_at": &n.UpdatedAt, "deleted_at": &n.DeletedAt} {
		if row[column] == nil {
			continue
		}
		at, ok := row[column].(time.Time)
		require.True(t, ok, "%s reads as a time.Time: %T", column, row[column])
		*into = &at
	}
	return n
}

func byNoteFilter(t *testing.T, r *parse.Resource[parsetest.Note], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

func noteTags(t *testing.T, b sqlBackend, db *sql.DB) []string {
	t.Helper()
	var pairs []string
	for _, row := range b.tableRows(t, db, "note_tags", "note_id", "tag_id") {
		pairs = append(pairs, columnText(t, row[0])+":"+columnText(t, row[1]))
	}
	slices.Sort(pairs)
	return pairs
}

// SkipUnchanged writes the rows a patch changes, on each backend: columns,
// arrays and documents compared as the backend holds them.
func skipUnchangedWritesOnlyRowsThePatchChanges(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		t.Run("a patch of what the row holds writes nothing", func(t *testing.T) {
			r, db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, `{"title":"one","rank":1,"labels":["a","b"],"attrs":{"a":1,"b":[2]}}`, parse.ByID("n1"))
			assert.Empty(t, changed, "no row is updated")
			assert.Equal(t, []string{"n1"}, sortedIDs(unchanged, noteID), "the read-back finds the row")
			n1 := storedNote(t, b, db, r, "n1")
			assert.Equal(t, int64(1), n1.Version, "the version stays")
			assert.Nil(t, n1.UpdatedAt, "the stamp stays")
			assert.Equal(t, unchanged[0], n1)
		})

		// A merge keeps the stored document's key order, so a patch in
		// another order reads as unchanged; a number's form counts, and an
		// array's order.
		t.Run("documents and arrays: key order does not count, a number's form does; arrays in order", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			for _, tc := range []struct {
				body    string
				changed bool
			}{
				{`{"attrs":{"b":[2],"a":1}}`, false},
				{`{"attrs":{"a":1.0}}`, true},
				{`{"attrs":{"a":2}}`, true},
				{`{"labels":["a","b"]}`, false},
				{`{"labels":["b","a"]}`, true},
			} {
				changed, _ := applied(t, b, r, tc.body, parse.ByID("n1"))
				assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
			}
		})

		t.Run("a patch changing one column of two writes, stamps and counts", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, `{"title":"one","rank":5}`, parse.ByID("n1"))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Empty(t, unchanged)
			assert.Equal(t, int64(5), changed[0].Rank)
			assert.Equal(t, int64(2), changed[0].Version)
			require.NotNil(t, changed[0].UpdatedAt)
			assert.True(t, parsetest.SkipAt().Equal(*changed[0].UpdatedAt), "stamped %v", *changed[0].UpdatedAt)
		})

		t.Run("a stamp the database clock sets moves with the rows that change", func(t *testing.T) {
			b.sqlOnly(t, "stamps the rows by the database's clock (StampSQL), an expression the memory provider does not evaluate")
			r, db := loadNotes(t, b)
			for _, tc := range []struct {
				body    string
				changed bool
			}{
				{`{"rank":1}`, false},
				{`{"rank":2}`, true},
			} {
				changed, unchanged := applied(t, b, r, tc.body, parse.ByID("n1"),
					parse.StampSQL("updated_at", b.clock()), parse.Increment("version"), parse.SkipUnchanged())
				assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
				assert.Equal(t, !tc.changed, len(unchanged) == 1, tc.body)
			}
			n1 := storedNote(t, b, db, r, "n1")
			assert.Equal(t, int64(2), n1.Version)
			require.NotNil(t, n1.UpdatedAt)
			assert.WithinDuration(t, time.Now(), *n1.UpdatedAt, time.Minute)
		})

		t.Run("a value to NULL, then NULL to NULL", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			changed, _ := applied(t, b, r, `{"title":null}`, parse.ByID("n1"))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Nil(t, changed[0].Title)
			changed, unchanged := applied(t, b, r, `{"title":null}`, parse.ByID("n1"))
			assert.Empty(t, changed)
			assert.Equal(t, []string{"n1"}, sortedIDs(unchanged, noteID))
			changed, _ = applied(t, b, r, `{"title":"back"}`, parse.ByID("n1"))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID), "NULL to a value")
			assert.Equal(t, int64(3), changed[0].Version)
		})

		t.Run("a batch writes the rows that change, within the scope", func(t *testing.T) {
			r, db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, `{"title":"two","attrs":{"a":1}}`, parse.ByIDs("n1", "n2", "n3", "m1"))
			assert.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Equal(t, []string{"n2"}, sortedIDs(unchanged, noteID), "n3 is deleted, m1 is t2's")
			assert.Equal(t, int64(1), storedNote(t, b, db, r, "n2").Version)
			assert.Equal(t, int64(1), storedNote(t, b, db, r, "m1").Version)
		})

		t.Run("a filter the patch moves rows out of", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, `{"rank":7}`, byNoteFilter(t, r, `{"rank":1}`))
			assert.Equal(t, []string{"n1", "n2"}, sortedIDs(changed, noteID))
			assert.Empty(t, unchanged, "the rows the patch moved out of the filter are changed ones")
			changed, unchanged = applied(t, b, r, `{"rank":7}`, byNoteFilter(t, r, `{"rank":7}`))
			assert.Empty(t, changed)
			assert.Equal(t, []string{"n1", "n2"}, sortedIDs(unchanged, noteID))
		})

		t.Run("a target outside the scope is not found", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			for _, id := range []string{"m1", "n3", "zz"} {
				changed, unchanged := applied(t, b, r, `{"title":"x"}`, parse.ByID(id))
				assert.Empty(t, changed, id)
				assert.Empty(t, unchanged, id)
			}
		})
	})
}

// SkipUnchanged with relations, on each backend: the junction says what a
// relation patch changed.
func skipUnchangedRelations(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		t.Run("a relation-only patch selects every owner, and the junction says what changed", func(t *testing.T) {
			r, db := loadNotes(t, b)
			for _, tc := range []struct {
				body    string
				changes int64
				tags    []string
			}{
				{`{"tags":["red"]}`, 0, []string{"n1:red"}},
				{`{"tags":["blue"]}`, 2, []string{"n1:blue"}},
			} {
				if b.engine != Memory {
					plan := parsetest.SkipUpdate(t, r, tc.body, parse.ByID("n1"))
					assert.Nil(t, plan.Current, "Owner returns every target row")
					assert.True(t, plan.Writes)
				}
				res := skipApply(t, r, tc.body, parse.ByID("n1"))
				assert.Empty(t, res.Changed, "a relation change moves no column")
				require.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
				assert.Equal(t, tc.changes, res.RelationRows, tc.body)
				assert.Equal(t, tc.tags, noteTags(t, b, db))
				assert.Equal(t, int64(1), storedNote(t, b, db, r, "n1").Version, "a relation change moves no column")
			}
		})

		t.Run("unchanged columns still change relations, for the rows Current reads", func(t *testing.T) {
			r, db := loadNotes(t, b)
			if b.engine != Memory {
				plan := parsetest.SkipUpdate(t, r, `{"title":"one","tags":["blue"]}`, parse.ByIDs("n1", "n2"))
				require.NotNil(t, plan.Current)
			}
			res := skipApply(t, r, `{"title":"one","tags":["blue"]}`, parse.ByIDs("n1", "n2"))
			assert.Equal(t, []string{"n2"}, sortedIDs(res.Changed, noteID), "n1 already reads one")
			assert.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
			assert.Equal(t, int64(3), res.RelationRows, "red out of n1, blue into both")
			assert.Equal(t, []string{"n1:blue", "n2:blue"}, noteTags(t, b, db))
			assert.Equal(t, int64(1), storedNote(t, b, db, r, "n1").Version)
			assert.Equal(t, int64(2), storedNote(t, b, db, r, "n2").Version)
		})

		t.Run("an empty patch is a scoped read", func(t *testing.T) {
			r, _ := loadNotes(t, b)
			if b.engine != Memory {
				plan := parsetest.SkipUpdate(t, r, `{}`, parse.ByID("n1"))
				assert.False(t, plan.Writes)
				assert.Nil(t, plan.Current)
			}
			res := skipApply(t, r, `{}`, parse.ByID("n1"))
			assert.Empty(t, res.Changed)
			assert.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
		})
	})
}

type uuidRow struct {
	ID   uuid.UUID `db:"id"`
	Name string    `json:"name" db:"name" parse:"patch"`
}

// A batch's keys are canonical: a UUID key names its row as the column
// holds it, one that names no row is a missing target, and two spellings of
// one key are one key given twice.
func updateBatchCanonicalKeys(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		id := uuid.MustParse("018c018c-0123-7123-8123-0123456789ab")
		provider, db := b.open(t)
		b.load(t, db, sqlTable{name: "uuid_rows", key: []string{"id"}, columns: []sqlColumn{column[uuid.UUID]("id"), column[string]("name").required()}},
			[][]any{{id, "old"}})
		r, err := parse.NewResource[uuidRow](provider, "uuid_rows")
		require.NoError(t, err)
		p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
		require.NoError(t, err)
		res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		assert.Equal(t, []uuidRow{{ID: id, Name: "new"}}, res.Changed)
		_, err = r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{uuid.MustParse("018c018c-0123-7123-8123-0123456789ac")}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
		twice := []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}, {Key: parse.Key{strings.ToUpper(id.String())}, Patch: p}}
		if b.engine == Memory {
			_, err = r.UpdateBatch(t.Context(), twice, parse.Scope{}, parse.UpdateBatchLimits{})
		} else {
			_, err = r.BuildUpdateBatch(twice, parse.Scope{}, parse.UpdateBatchLimits{})
		}
		require.Error(t, err)
	})
}

type batchDevice struct {
	ID       string `json:"id"        db:"id"        parse:"filter"`
	TenantID int64  `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,patch"`
}

// A batch target that is not there is the server's error: it matches
// ErrUpdateBatchTargets, names the resource once after parse's prefix, and
// is no request error.
func updateBatchMissingTargetNamesItsResourceOnce(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		devices := parse.MustResource[batchDevice](p, "devices", parse.TenantColumn("tenant_id"))
		b.create(t, db, sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{
			column[string]("id").required(), column[int64]("tenant_id").required(), column[string]("name").required(),
		}})
		rename, err := devices.ParseMergePatch([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		_, err = devices.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{"a"}, Patch: rename}}, parse.Scope{Tenant: "1"}, parse.UpdateBatchLimits{})
		require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
		msg := err.Error()
		assert.True(t, strings.HasPrefix(msg, "parse: devices: "), msg)
		assert.Equal(t, 1, strings.Count(msg, "parse:"), msg)
		assert.Equal(t, 1, strings.Count(msg, "devices"), msg)
		assert.NotContains(t, parse.ErrUpdateBatchTargets.Error(), "parse:")
		_, isParseError := errors.AsType[*core.ParseError](err)
		assert.False(t, isParseError, "the server's mistake, never the request's")
	})
}

type pointerKeyed struct {
	ID   *string `json:"id"   db:"id"   parse:"filter"`
	Name string  `json:"name" db:"name" parse:"filter,patch"`
}

// optionalCode is a key codec that writes NULL for its zero value.
type optionalCode string

func (c optionalCode) Value() (driver.Value, error) {
	if c == "" {
		return nil, nil
	}
	return string(c), nil
}

func (c *optionalCode) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*c = ""
	case string:
		*c = optionalCode(v)
	default:
		return fmt.Errorf("optionalCode: %T", src)
	}
	return nil
}

type valuerKeyed struct {
	ID   optionalCode `json:"id"   db:"id"   parse:"filter"`
	Name string       `json:"name" db:"name" parse:"filter,patch"`
}

// keyedTable holds rows a, named old, and b, named new, on a fresh
// database of b, and the resource of T on it. On a SQL engine its id
// column is no primary key, so it can hold NULL.
func keyedTable[T any](t *testing.T, b sqlBackend) (*parse.Resource[T], *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	r, err := parse.NewResource[T](p, "keyed")
	require.NoError(t, err)
	if b.engine == Memory {
		require.NoError(t, storeOf(t, db).Put(t.Context(), "keyed", map[string]any{"id": "a", "name": "old"}, map[string]any{"id": "b", "name": "new"}))
		return r, db
	}
	dbtest.Exec(t, db, `CREATE TABLE keyed (id TEXT UNIQUE, name TEXT NOT NULL)`,
		`INSERT INTO keyed VALUES ('a', 'old'), ('b', 'new')`)
	return r, db
}

// keylessRow writes a row whose key is NULL to the keyed table: on the
// Memory engine, whose table refuses a row without its key as it is
// written, it requires the refusal and reports false.
func keylessRow(t *testing.T, b sqlBackend, db *sql.DB) bool {
	t.Helper()
	if b.engine == Memory {
		err := storeOf(t, db).Put(t.Context(), "keyed", map[string]any{"id": nil, "name": "new"})
		require.ErrorContains(t, err, "key column id: NULL")
		return false
	}
	dbtest.Exec(t, db, `INSERT INTO keyed VALUES (NULL, 'new')`)
	return true
}

// renameSkipping applies {"name":"new"} to target under SkipUnchanged.
func renameSkipping[T any](t *testing.T, r *parse.Resource[T], target func(*parse.Resource[T]) parse.Target) (parse.UpdateResult[T], error) {
	t.Helper()
	rename, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	return r.Update(t.Context(), rename, target(r), parse.Scope{}, parse.SkipUnchanged())
}

func byName[T any](t *testing.T) func(*parse.Resource[T]) parse.Target {
	return func(r *parse.Resource[T]) parse.Target {
		f, err := r.ParseFilterMap(map[string]any{"name": "new"})
		require.NoError(t, err)
		return parse.ByFilter(f)
	}
}

// An update under SkipUnchanged splits the rows it read by their keys'
// values: rows scanned apart hold distinct pointers to one key, and a key
// column the database holds as NULL, as a nil pointer or a Valuer that
// writes nil, is a row without a key.
func skipUnchangedRowKeys(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		t.Run("pointer keys compare by value", func(t *testing.T) {
			r, db := keyedTable[pointerKeyed](t, b)
			res, err := renameSkipping(t, r, func(*parse.Resource[pointerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)
			assert.Equal(t, "a", *res.Changed[0].ID)
			require.Len(t, res.Unchanged, 1)
			assert.Equal(t, "b", *res.Unchanged[0].ID)

			if keylessRow(t, b, db) {
				_, err = renameSkipping(t, r, byName[pointerKeyed](t))
				assert.ErrorContains(t, err, "has no key")
			}
		})
		t.Run("a row whose key is NULL is refused", func(t *testing.T) {
			r, db := keyedTable[valuerKeyed](t, b)
			res, err := renameSkipping(t, r, func(*parse.Resource[valuerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
			require.NoError(t, err)
			assert.Equal(t, []valuerKeyed{{ID: "a", Name: "new"}}, res.Changed)
			assert.Equal(t, []valuerKeyed{{ID: "b", Name: "new"}}, res.Unchanged)

			if keylessRow(t, b, db) {
				_, err = renameSkipping(t, r, byName[valuerKeyed](t))
				require.ErrorContains(t, err, "keyed: a row has no key: key column id holds no value")
			}
		})
	})
}

type clockType struct {
	ID        string    `json:"id"         db:"id"         parse:"filter,sort"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

type clockItem struct {
	ID        string    `json:"id"         db:"id"         parse:"filter,sort"`
	Name      string    `json:"name"       db:"name"       parse:"filter,patch"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

// An update reads its resource's clock once: its membership change admits
// the related rows the target's policy admits at the plan's instant, not at
// a later reading.
func updateClockMembershipAtThePlansInstant(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		stepping := func() time.Time {
			at = at.Add(time.Hour)
			return at
		}
		p, db := b.open(t)
		types := parse.MustResource[clockType](p, "clock_types")
		items, err := parse.NewResource[clockItem](p, "clock_items", parse.ParserOptions(core.WithClock(stepping)))
		require.NoError(t, err)
		items.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("clock_item_types", "item_id", "type_id"), parse.PolicyScoped(), parse.Writable()))
		scope := parse.Scope{
			Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+1h"}}},
			Related: map[string]*filter.PolicyConstraints{
				"clock_types": {Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+2h"}}},
			},
		}
		parsed, err := items.ParseMergePatch([]byte(`{"name":"x","shared_types":["k0","k1"]}`))
		require.NoError(t, err)
		// The update's plan takes the clock's next reading. k0 is visible at
		// that instant (created before now+2h); k1 only at a later reading.
		instant := at.Add(time.Hour)
		b.load(t, db, sqlTable{name: "clock_items", key: []string{"id"}, columns: []sqlColumn{
			column[string]("id"), column[string]("name").required(), column[time.Time]("created_at").required(),
		}}, [][]any{{"i1", "old", instant.Add(-time.Hour)}})
		b.load(t, db, sqlTable{name: "clock_types", key: []string{"id"}, columns: []sqlColumn{column[string]("id"), column[time.Time]("created_at").required()}},
			[][]any{{"k0", instant.Add(time.Hour)}, {"k1", instant.Add(150 * time.Minute)}})
		b.create(t, db, sqlTable{name: "clock_item_types", key: []string{"item_id", "type_id"}, columns: []sqlColumn{
			column[string]("item_id").required(), column[string]("type_id").required(),
		}})

		res, err := items.Update(t.Context(), parsed, parse.ByID("i1"), scope)
		require.NoError(t, err)
		assert.Equal(t, instant, at, "the update read the clock once")
		assert.Equal(t, int64(1), res.RelationRows)
		var pairs []string
		for _, row := range b.tableRows(t, db, "clock_item_types", "item_id", "type_id") {
			pairs = append(pairs, columnText(t, row[0])+":"+columnText(t, row[1]))
		}
		assert.Equal(t, []string{"i1:k0"}, pairs, "a membership change admits the related rows at the plan's instant")
	})
}
