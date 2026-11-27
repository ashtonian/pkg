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
	"github.com/ashtonian/pkg/parse/core"
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
	uuidType := b.columnType(core.TypeInfo{Type: core.FieldTypeUUID})
	// setup opens a fresh database running stmts, with its provider.
	setup := func(t *testing.T, stmts ...string) (*parse.Provider, *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		dbtest.Exec(t, db, stmts...)
		return p, db
	}
	ids := func(t *testing.T, db *sql.DB, r interface {
		ParseListInput(parse.ListInput) (*parse.ListRequest, error)
		BuildList(*parse.ListRequest, parse.Scope) (*parse.ListPlan, error)
	}, filterJSON, tenant string) []int64 {
		t.Helper()
		req, err := r.ParseListInput(parse.ListInput{Filter: filterJSON})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{Tenant: tenant})
		require.NoError(t, err)
		return parsetest.ItemIDs(dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...), func(row []any) int64 { return asInt64(t, row[0]) })
	}

	t.Run("an owner without a tenant binds a depth-two integer column and its pair tables", func(t *testing.T) {
		p, db := setup(t,
			`CREATE TABLE devices (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
			`CREATE TABLE commands (id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
			`CREATE TABLE logs (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, command_id INTEGER NOT NULL, level TEXT NOT NULL)`,
			`CREATE TABLE tags (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`,
			`CREATE TABLE device_tags (device_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, tenant_id INTEGER NOT NULL, PRIMARY KEY (device_id, tag_id))`,
			`INSERT INTO devices VALUES (1, 'alpha'), (2, 'beta')`,
			`INSERT INTO commands VALUES (10, 1, 'sent'), (11, 2, 'sent')`,
			`INSERT INTO logs VALUES (100, 1, 10, 'error'), (101, 2, 11, 'error'), (102, 2, 10, 'error')`,
			`INSERT INTO tags VALUES (20, 'red')`,
			`INSERT INTO device_tags VALUES (1, 20, 1), (2, 20, 2)`,
		)
		devices := parse.MustResource[parsetest.UntenantedDevice](p, "devices")
		commands := parse.MustResource[parsetest.UntenantedCommand](p, "commands")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		devices.MustRelate("tags", parse.ManyToMany(parse.MustResource[parsetest.UntenantedTag](p, "tags"),
			parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed()))
		// Declared last: devices learns its key from a relation of its target.
		commands.MustRelate("logs", parse.HasMany(parsetest.Tenanted[parsetest.GraphLog[int]](p, "logs"), "command_id", parse.Indexed()))

		assert.Equal(t, []int64{1}, ids(t, db, devices, parsetest.DepthTwoFilter, "1"))
		assert.Equal(t, []int64{1, 2}, ids(t, db, devices, parsetest.DepthTwoFilter, "2"), "tenant 2's error log on device 1's command is tenant 2's")
		assert.Equal(t, []int64{1}, ids(t, db, devices, `{"tags":20}`, "1"))
		assert.Equal(t, []int64{2}, ids(t, db, devices, `{"tags":20}`, "2"))
	})

	t.Run("integer columns of different widths bind the tenant both hold", func(t *testing.T) {
		p, db := setup(t,
			`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id BIGINT NOT NULL, name TEXT NOT NULL)`,
			`CREATE TABLE commands (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
			`INSERT INTO devices VALUES (1, 1, 'alpha'), (2, 1, 'beta'), (3, 2, 'gamma')`,
			`INSERT INTO commands VALUES (10, 1, 1, 'sent'), (11, 2, 2, 'sent')`,
		)
		devices := parsetest.Tenanted[parsetest.GraphDevice[int64]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(parsetest.Tenanted[parsetest.GraphCommand[int32]](p, "commands"), "device_id", parse.Indexed()))

		assert.Equal(t, []int64{1}, ids(t, db, devices, `{"commands":{"$some":{"status":"sent"}}}`, "1"))
		_, err := devices.BuildList(nil, parse.Scope{Tenant: "3000000000"})
		assert.ErrorIs(t, err, parse.ErrTenantInvalid, "commands.tenant_id cannot hold it")
	})

	t.Run("a string column beside a UUID one holds its canonical text", func(t *testing.T) {
		const tenant1, tenant2 = "3f2504e0-4f89-41d3-9a0c-0305e82c3301", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
		p, db := setup(t,
			`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id `+uuidType+` NOT NULL, name TEXT NOT NULL)`,
			`CREATE TABLE commands (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
			`INSERT INTO devices VALUES (1, '`+tenant1+`', 'alpha'), (2, '`+tenant1+`', 'beta'), (3, '`+tenant2+`', 'gamma')`,
			`INSERT INTO commands VALUES (10, '`+tenant1+`', 1, 'sent'), (11, '`+tenant2+`', 2, 'sent')`,
		)
		devices := parsetest.Tenanted[parsetest.GraphDevice[uuid.UUID]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(parsetest.Tenanted[parsetest.GraphCommand[string]](p, "commands"), "device_id", parse.Indexed()))

		upper := strings.ToUpper(tenant1)
		assert.Equal(t, []int64{1}, ids(t, db, devices, `{"commands":{"$some":{"status":"sent"}}}`, upper))
		assert.Equal(t, []int64{2}, ids(t, db, devices, `{"commands":{"$none":{"status":"sent"}}}`, upper),
			"tenant 2's command on device 2 is not tenant 1's")
	})
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

// intTenantFixture is tenants 1 and 2 keyed by INTEGER columns. Tenant 2's
// rows point at tenant 1's devices (a pending command on device 2, a pair of
// device 1 with tenant 2's tag) so a relation that lost its tenant floor
// would match them.
func intTenantFixture() []string {
	stmts := []string{
		`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE commands (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, label TEXT NOT NULL)`,
		`CREATE TABLE device_tags (device_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, tenant_id INTEGER NOT NULL, PRIMARY KEY (device_id, tag_id))`,
		`INSERT INTO devices VALUES (1, 1, 'alpha'), (2, 1, 'beta'), (3, 2, 'gamma')`,
		`INSERT INTO commands VALUES (10, 1, 1, 'pending'), (11, 1, 2, 'done'), (12, 2, 2, 'pending')`,
		`INSERT INTO tags VALUES (20, 1, 'red'), (21, 2, 'blue')`,
		`INSERT INTO device_tags VALUES (1, 20, 1), (1, 21, 2), (3, 21, 2)`,
	}
	return stmts
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
	dbtest.Exec(t, db, intTenantFixture()...)
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
		pairs := normalizeInts(t, dbtest.QueryRows[[]any](t, db, "SELECT device_id, tag_id, tenant_id FROM device_tags WHERE device_id = 2 ORDER BY tag_id"))
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
