//go:build integration

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

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
var intTenantFixture = []string{
	`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL)`,
	`CREATE TABLE commands (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
	`CREATE TABLE tags (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, label TEXT NOT NULL)`,
	`CREATE TABLE device_tags (device_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, tenant_id INTEGER NOT NULL, PRIMARY KEY (device_id, tag_id))`,
	`INSERT INTO devices VALUES (1, 1, 'alpha'), (2, 1, 'beta'), (3, 2, 'gamma')`,
	`INSERT INTO commands VALUES (10, 1, 1, 'pending'), (11, 1, 2, 'done'), (12, 2, 2, 'pending')`,
	`INSERT INTO tags VALUES (20, 1, 'red'), (21, 2, 'blue')`,
	`INSERT INTO device_tags VALUES (1, 20, 1), (1, 21, 2), (3, 21, 2)`,
}

func intTenantResources(t *testing.T) *parse.Resource[intTenantDevice] {
	t.Helper()
	devices := parse.MustResource[intTenantDevice]("devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[tenantKeyCommand]("commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[intTenantTag]("tags", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
	devices.MustRelate("tags", parse.ManyToMany(tags,
		parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Writable()))
	return devices
}

func TestIntegerTenantKeys(t *testing.T) {
	runSQLBackends(t, runIntegerTenantKeys)
}

func runIntegerTenantKeys(t *testing.T, b sqlBackend) {
	db := b.open(t)
	d := b.dialect
	execStatements(t, db, intTenantFixture...)
	devices := intTenantResources(t)
	list := func(tenant string, q url.Values) *filter.Page[intTenantDevice] {
		t.Helper()
		req, err := devices.ParseList(q)
		require.NoError(t, err)
		plan, err := devices.BuildList(d, req, parse.Scope{Tenant: tenant})
		require.NoError(t, err)
		return listPage(t, db, devices, d, plan)
	}
	ids := func(page *filter.Page[intTenantDevice]) []int {
		return itemIDs(page.Items, func(dev intTenantDevice) int { return dev.ID })
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
		plan, err := devices.BuildUpdate(d, p, parse.ByID(3), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		res, err := applyUpdate(t, db, devices, d, plan, false)
		require.NoError(t, err)
		assert.Zero(t, res, "device 3 is tenant 2's")

		p, err = devices.ParseMergePatch([]byte(`{"tags":[20, 21]}`))
		require.NoError(t, err)
		plan, err = devices.BuildUpdate(d, p, parse.ByID(2), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		res, err = applyUpdate(t, db, devices, d, plan, false)
		require.NoError(t, err)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, int64(1), res.RelationRows, "only tenant 1's tag 20 is paired")
		pairs := normalizeInts(t, queryRows[[]any](t, db, "SELECT device_id, tag_id, tenant_id FROM device_tags WHERE device_id = 2 ORDER BY tag_id"))
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
