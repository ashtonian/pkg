//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type intTenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID int    `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type intTenantCommand struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID int    `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
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
	commands := parse.MustResource[intTenantCommand]("commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[intTenantTag]("tags", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
	devices.MustRelate("tags", parse.ManyToMany(tags,
		parse.Junction{Table: "device_tags", Source: "device_id", Target: "tag_id", Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Writable()))
	return devices
}

func TestIntegerTenantKeys_SQLite(t *testing.T) {
	runIntegerTenantKeys(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}

func TestIntegerTenantKeys_Postgres(t *testing.T) {
	runIntegerTenantKeys(t, dbtest.Postgres(t), filter.PostgresDialect{})
}

func runIntegerTenantKeys(t *testing.T, db *sql.DB, d parse.Dialect) {
	for _, stmt := range intTenantFixture {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	devices := intTenantResources(t)
	ids := func(st parse.Statement) []int64 {
		t.Helper()
		var out []int64
		for _, row := range queryRows(t, db, st.SQL, st.Args) {
			out = append(out, asInt64(t, row[0]))
		}
		return out
	}
	list := func(tenant string, q url.Values) *parse.ListPlan {
		t.Helper()
		req, err := devices.ParseList(q)
		require.NoError(t, err)
		plan, err := devices.BuildList(d, req, parse.Scope{Tenant: tenant})
		require.NoError(t, err)
		return plan
	}

	t.Run("lists and counts the tenant's rows", func(t *testing.T) {
		plan := list("1", url.Values{filter.ParamIncludeTotal: {"true"}})
		assert.Equal(t, []int64{1, 2}, ids(plan.Page))
		assert.Equal(t, [][]any{{int64(2)}}, normalizeInts(t, queryRows(t, db, plan.Count.SQL, plan.Count.Args)))
		assert.Equal(t, []int64{3}, ids(list("2", nil).Page))
	})

	t.Run("relation predicates reach only the tenant's related rows", func(t *testing.T) {
		plan := list("1", url.Values{filter.ParamFilter: {`{"commands":{"$some":{"status":"pending"}}}`}})
		assert.Equal(t, []int64{1}, ids(plan.Page), "tenant 2's pending command on device 2 is not tenant 1's")
		plan = list("1", url.Values{filter.ParamFilter: {`{"tags":21}`}})
		assert.Empty(t, ids(plan.Page), "device 1's pair with tenant 2's tag is not tenant 1's")
	})

	t.Run("includes return only the tenant's pairs", func(t *testing.T) {
		plan := list("1", url.Values{filter.ParamInclude: {"tags"}})
		queries, err := devices.IncludeQueries(plan, []any{int64(1), int64(2)})
		require.NoError(t, err)
		require.Len(t, queries, 1)
		var pairs [][2]string
		for _, row := range queryRows(t, db, queries[0].SQL, queries[0].Args) {
			pairs = append(pairs, [2]string{fmt.Sprint(row[0]), fmt.Sprint(row[1])})
		}
		assert.Equal(t, [][2]string{{"1", "20"}}, pairs)
	})

	t.Run("updates and membership writes stay in the tenant", func(t *testing.T) {
		p, err := devices.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		plan, err := devices.BuildUpdate(d, p, parse.ByID(3), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		assert.Empty(t, queryRows(t, db, plan.Owner.SQL, plan.Owner.Args), "device 3 is tenant 2's")

		p, err = devices.ParseMergePatch([]byte(`{"tags":[20, 21]}`))
		require.NoError(t, err)
		plan, err = devices.BuildUpdate(d, p, parse.ByID(2), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		owners := queryRows(t, db, plan.Owner.SQL, plan.Owner.Args)
		require.Len(t, owners, 1)
		stmts, err := devices.RelationStatements(plan, []any{owners[0][0]})
		require.NoError(t, err)
		for _, st := range stmts {
			_, err := db.Exec(st.SQL, st.Args...)
			require.NoError(t, err, st.SQL)
		}
		pairs := normalizeInts(t, queryRows(t, db, "SELECT device_id, tag_id, tenant_id FROM device_tags WHERE device_id = 2 ORDER BY tag_id", nil))
		assert.Equal(t, [][]any{{int64(2), int64(20), int64(1)}}, pairs, "tenant 2's tag is not paired")
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

func normalizeInts(t *testing.T, rows [][]any) [][]any {
	t.Helper()
	for _, row := range rows {
		for i, v := range row {
			row[i] = asInt64(t, v)
		}
	}
	return rows
}
