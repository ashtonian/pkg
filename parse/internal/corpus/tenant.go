//go:build integration

package corpus

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// Requests whose tenant columns are reached through relations at depth
// two, through pair tables and with mixed types, against rows of tenant 2
// that point at tenant 1's rows, on each backend.
func tenantKeyGraph(t *testing.T, engine Engine) {
	runOn(t, engine, runTenantKeyGraph)
}

func runTenantKeyGraph(t *testing.T, b sqlBackend) {
	// setup opens a fresh database holding tables, with its provider.
	setup := func(t *testing.T, tables ...fixtureTable) (*parse.Provider, *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		for _, f := range tables {
			b.load(t, db, f.table, f.rows)
		}
		return p, db
	}
	id := func(name string) sqlColumn { return column[int64](name).required() }
	text := func(name string) sqlColumn { return column[string](name).required() }
	untenanted := func(d parsetest.UntenantedDevice) int { return d.ID }

	t.Run("an owner without a tenant binds a depth-two integer column and its pair tables", func(t *testing.T) {
		p, db := setup(t,
			fixtureTable{sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{id("id"), text("name")}},
				[][]any{{1, "alpha"}, {2, "beta"}}},
			fixtureTable{sqlTable{name: "commands", key: []string{"id"}, columns: []sqlColumn{id("id"), id("device_id"), text("status")}},
				[][]any{{10, 1, "sent"}, {11, 2, "sent"}}},
			fixtureTable{sqlTable{name: "logs", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), id("command_id"), text("level")}},
				[][]any{{100, 1, 10, "error"}, {101, 2, 11, "error"}, {102, 2, 10, "error"}}},
			fixtureTable{sqlTable{name: "tags", key: []string{"id"}, columns: []sqlColumn{id("id"), text("label")}},
				[][]any{{20, "red"}}},
			fixtureTable{sqlTable{name: "device_tags", key: []string{"device_id", "tag_id"}, columns: []sqlColumn{id("device_id"), id("tag_id"), id("tenant_id")}},
				[][]any{{1, 20, 1}, {2, 20, 2}}},
		)
		devices := parse.MustResource[parsetest.UntenantedDevice](p, "devices")
		commands := parse.MustResource[parsetest.UntenantedCommand](p, "commands")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		devices.MustRelate("tags", parse.ManyToMany(parse.MustResource[parsetest.UntenantedTag](p, "tags"),
			parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed()))
		// Declared last: devices learns its key from a relation of its target.
		commands.MustRelate("logs", parse.HasMany(parsetest.Tenanted[parsetest.GraphLog[int]](p, "logs"), "command_id", parse.Indexed()))

		assert.Equal(t, []int{1}, tenantIDs(t, b, db, devices, parsetest.DepthTwoFilter, "1", untenanted))
		assert.Equal(t, []int{1, 2}, tenantIDs(t, b, db, devices, parsetest.DepthTwoFilter, "2", untenanted), "tenant 2's error log on device 1's command is tenant 2's")
		assert.Equal(t, []int{1}, tenantIDs(t, b, db, devices, `{"tags":20}`, "1", untenanted))
		assert.Equal(t, []int{2}, tenantIDs(t, b, db, devices, `{"tags":20}`, "2", untenanted))
	})

	t.Run("integer columns of different widths bind the tenant both hold", func(t *testing.T) {
		p, db := setup(t,
			fixtureTable{sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), text("name")}},
				[][]any{{1, 1, "alpha"}, {2, 1, "beta"}, {3, 2, "gamma"}}},
			fixtureTable{sqlTable{name: "commands", key: []string{"id"}, columns: []sqlColumn{id("id"), column[int32]("tenant_id").required().holding(Postgres, "INTEGER"), id("device_id"), text("status")}},
				[][]any{{10, 1, 1, "sent"}, {11, 2, 2, "sent"}}},
		)
		devices := parsetest.Tenanted[parsetest.GraphDevice[int64]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(parsetest.Tenanted[parsetest.GraphCommand[int32]](p, "commands"), "device_id", parse.Indexed()))

		graphID := func(d parsetest.GraphDevice[int64]) int { return d.ID }
		assert.Equal(t, []int{1}, tenantIDs(t, b, db, devices, `{"commands":{"$some":{"status":"sent"}}}`, "1", graphID))
		if b.engine == Memory {
			req, err := devices.ParseListInput(parse.ListInput{})
			require.NoError(t, err)
			_, err = devices.List(t.Context(), req, parse.Scope{Tenant: "3000000000"})
			assert.ErrorIs(t, err, parse.ErrTenantInvalid, "commands.tenant_id cannot hold it")
			return
		}
		_, err := devices.BuildList(nil, parse.Scope{Tenant: "3000000000"})
		assert.ErrorIs(t, err, parse.ErrTenantInvalid, "commands.tenant_id cannot hold it")
	})

	t.Run("a string column beside a UUID one holds its canonical text", func(t *testing.T) {
		const tenant1, tenant2 = "3f2504e0-4f89-41d3-9a0c-0305e82c3301", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
		p, db := setup(t,
			fixtureTable{sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{id("id"), column[uuid.UUID]("tenant_id").required(), text("name")}},
				[][]any{{1, tenant1, "alpha"}, {2, tenant1, "beta"}, {3, tenant2, "gamma"}}},
			fixtureTable{sqlTable{name: "commands", key: []string{"id"}, columns: []sqlColumn{id("id"), text("tenant_id"), id("device_id"), text("status")}},
				[][]any{{10, tenant1, 1, "sent"}, {11, tenant2, 2, "sent"}}},
		)
		devices := parsetest.Tenanted[parsetest.GraphDevice[uuid.UUID]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(parsetest.Tenanted[parsetest.GraphCommand[string]](p, "commands"), "device_id", parse.Indexed()))

		upper := strings.ToUpper(tenant1)
		graphID := func(d parsetest.GraphDevice[uuid.UUID]) int { return d.ID }
		assert.Equal(t, []int{1}, tenantIDs(t, b, db, devices, `{"commands":{"$some":{"status":"sent"}}}`, upper, graphID))
		assert.Equal(t, []int{2}, tenantIDs(t, b, db, devices, `{"commands":{"$none":{"status":"sent"}}}`, upper, graphID),
			"tenant 2's command on device 2 is not tenant 1's")
	})
}

// fixtureTable is a fixture table and the rows it holds.
type fixtureTable struct {
	table sqlTable
	rows  [][]any
}

// tenantIDs is the ids of the rows r lists for filterJSON in tenant, each
// read by id: on the Memory engine, its page's items; on a SQL engine, the
// rows of BuildList's page statement, whose first column is the id.
func tenantIDs[T any](t *testing.T, b sqlBackend, db *sql.DB, r *parse.Resource[T], filterJSON, tenant string, id func(T) int) []int {
	t.Helper()
	req, err := r.ParseListInput(parse.ListInput{Filter: filterJSON})
	require.NoError(t, err)
	if b.engine == Memory {
		return parsetest.ItemIDs(listPage(t, r, req, parse.Scope{Tenant: tenant}).Items, id)
	}
	plan, err := r.BuildList(req, parse.Scope{Tenant: tenant})
	require.NoError(t, err)
	return parsetest.ItemIDs(dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...), func(row []any) int { return int(asInt64(t, row[0])) })
}

func asInt64(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	}
	require.Fail(t, fmt.Sprintf("not an integer: %T %v", v, v))
	return 0
}

type intTenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID int    `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type intTenantTag struct {
	ID       int    `json:"id"    db:"id"    parse:"filter,sort"`
	TenantID int    `json:"-"     db:"tenant_id"`
	Label    string `json:"label" db:"label" parse:"filter"`
}

// loadIntTenants loads tenants 1 and 2 keyed by integer columns into db.
// Tenant 2's rows point at tenant 1's devices (a pending command on device
// 2, a pair of device 1 with tenant 2's tag) so a relation that lost its
// tenant floor would match them.
func loadIntTenants(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	id := func(name string) sqlColumn { return column[int32](name).required().holding(Postgres, "INTEGER") }
	text := func(name string) sqlColumn { return column[string](name).required() }
	b.load(t, db, sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), text("name")}},
		[][]any{{1, 1, "alpha"}, {2, 1, "beta"}, {3, 2, "gamma"}})
	b.load(t, db, sqlTable{name: "commands", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), id("device_id"), text("status")}},
		[][]any{{10, 1, 1, "pending"}, {11, 1, 2, "done"}, {12, 2, 2, "pending"}})
	b.load(t, db, sqlTable{name: "tags", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), text("label")}},
		[][]any{{20, 1, "red"}, {21, 2, "blue"}})
	b.load(t, db, sqlTable{name: "device_tags", key: []string{"device_id", "tag_id"}, columns: []sqlColumn{id("device_id"), id("tag_id"), id("tenant_id")}},
		[][]any{{1, 20, 1}, {1, 21, 2}, {3, 21, 2}})
}

// intTenantResources builds the devices, with their relations, on p.
func intTenantResources(t *testing.T, p *parse.Provider) *parse.Resource[intTenantDevice] {
	t.Helper()
	devices := parse.MustResource[intTenantDevice](p, "devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[parsetest.TenantKeyCommand](p, "commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[intTenantTag](p, "tags", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
	devices.MustRelate("tags", parse.ManyToMany(tags,
		parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Writable()))
	return devices
}

func integerTenantKeys(t *testing.T, engine Engine) {
	runOn(t, engine, runIntegerTenantKeys)
}

func runIntegerTenantKeys(t *testing.T, b sqlBackend) {
	provider, db := b.open(t)
	loadIntTenants(t, b, db)
	devices := intTenantResources(t, provider)
	list := func(tenant string, q url.Values) *filter.Page[intTenantDevice] {
		t.Helper()
		req, err := devices.ParseList(q)
		require.NoError(t, err)
		return listPage(t, devices, req, parse.Scope{Tenant: tenant})
	}
	ids := func(page *filter.Page[intTenantDevice]) []int {
		return parsetest.ItemIDs(page.Items, func(dev intTenantDevice) int { return dev.ID })
	}

	t.Run("lists and counts the tenant's rows", func(t *testing.T) {
		page := list("1", url.Values{filter.ParamIncludeTotal: {"true"}})
		assert.Equal(t, []int{1, 2}, ids(page))
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
		assert.Equal(t, []int{3}, ids(list("2", nil)))
	})

	t.Run("relation predicates reach only the tenant's related rows", func(t *testing.T) {
		page := list("1", url.Values{filter.ParamFilter: {`{"commands":{"$some":{"status":"pending"}}}`}})
		assert.Equal(t, []int{1}, ids(page), "tenant 2's pending command on device 2 is not tenant 1's")
		page = list("1", url.Values{filter.ParamFilter: {`{"tags":21}`}})
		assert.Empty(t, ids(page), "device 1's pair with tenant 2's tag is not tenant 1's")
	})

	t.Run("includes return only the tenant's pairs", func(t *testing.T) {
		page := list("1", url.Values{filter.ParamInclude: {"tags"}})
		assert.Equal(t, []int{1, 2}, ids(page))
		assert.Equal(t, map[string][]string{"1": {"20"}}, page.Relations["tags"], "not device 1's pair with tenant 2's tag")
	})

	t.Run("updates and membership writes stay in the tenant", func(t *testing.T) {
		p, err := devices.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		res, err := devices.Update(t.Context(), p, parse.ByID(3), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		assert.Zero(t, res, "device 3 is tenant 2's")

		p, err = devices.ParseMergePatch([]byte(`{"tags":[20, 21]}`))
		require.NoError(t, err)
		res, err = devices.Update(t.Context(), p, parse.ByID(2), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, int64(1), res.RelationRows, "only tenant 1's tag 20 is paired")
		var pairs [][]any
		for _, row := range normalizeInts(t, b.tableRows(t, db, "device_tags", "device_id", "tag_id", "tenant_id")) {
			if row[0] == int64(2) {
				pairs = append(pairs, row)
			}
		}
		assert.Equal(t, [][]any{{int64(2), int64(20), int64(1)}}, pairs, "tenant 2's tag is not paired")
	})
}

func normalizeInts(t *testing.T, rows [][]any) [][]any {
	t.Helper()
	for _, row := range rows {
		for i, v := range row {
			row[i] = asInt64(t, v)
		}
	}
	return rows
}

type int32TenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID int32  `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

// A pair table's membership statements bind the tenant as the owner's
// tenant column holds it, an integer: on SQLite a pair table that declares
// no column types holds what was bound, and a tenant bound as text would
// delete no pair it replaces.
func integerTenantKeysPairsHoldTheBoundKey(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := b.open(t)
		id := func(name string) sqlColumn { return column[int64](name).required() }
		text := func(name string) sqlColumn { return column[string](name).required() }
		// untyped is a pair column SQLite declares no type for.
		untyped := func(name string) sqlColumn { return column[int64](name).holding(SQLite, "") }
		b.load(t, db, sqlTable{name: "devices", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), text("name")}},
			[][]any{{1, 44, "alpha"}})
		b.load(t, db, sqlTable{name: "commands", key: []string{"id"}, columns: []sqlColumn{id("id"), id("tenant_id"), id("device_id"), text("status")}},
			[][]any{{7, 44, 1, "sent"}, {8, 44, 1, "sent"}})
		b.load(t, db, sqlTable{name: "device_commands", columns: []sqlColumn{untyped("device_id"), untyped("command_id"), untyped("tenant_id")}},
			[][]any{{1, 8, 44}})
		junction := parse.Junction{Table: "device_commands", Source: []string{"device_id"}, Target: []string{"command_id"}, Tenant: "tenant_id"}
		devices := parse.MustResource[int32TenantDevice](provider, "devices", parse.TenantColumn("tenant_id"))
		devices.MustRelate("commands", parse.ManyToMany(parse.MustResource[parsetest.TenantKeyCommand](provider, "commands", parse.TenantColumn("tenant_id")),
			junction, parse.Includable(), parse.Writable()))
		p, err := devices.ParseMergePatch([]byte(`{"commands":[7]}`))
		require.NoError(t, err)
		res, err := devices.Update(t.Context(), p, parse.ByID(1), parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), res.RelationRows, "the pair to 8 removed, the pair to 7 added")
		assert.Equal(t, [][]any{{int64(1), int64(7), int64(44)}},
			normalizeInts(t, b.tableRows(t, db, "device_commands", "device_id", "command_id", "tenant_id")))
		if b.engine == SQLite {
			assert.Equal(t, []string{"integer"}, dbtest.QueryRows[string](t, db, `SELECT typeof(tenant_id) FROM device_commands`))
		}
	})
}
