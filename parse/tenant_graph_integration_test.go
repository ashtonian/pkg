//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// Requests whose tenant columns are reached through relations at depth
// two, through pair tables and with mixed types, against rows of tenant 2
// that point at tenant 1's rows, on each backend.
func TestTenantKeyGraph(t *testing.T) {
	runSQLBackends(t, runTenantKeyGraph)
}

func runTenantKeyGraph(t *testing.T, b sqlBackend) {
	uuidType := b.columnType(core.TypeInfo{Type: core.FieldTypeUUID})
	// setup opens a fresh database running stmts, with its provider.
	setup := func(t *testing.T, stmts ...string) (*parse.Provider, *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		execStatements(t, db, stmts...)
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
		return itemIDs(queryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...), func(row []any) int64 { return asInt64(t, row[0]) })
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
		devices := parse.MustResource[untenantedDevice](p, "devices")
		commands := parse.MustResource[untenantedCommand](p, "commands")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		devices.MustRelate("tags", parse.ManyToMany(parse.MustResource[untenantedTag](p, "tags"),
			parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed()))
		// Declared last: devices learns its key from a relation of its target.
		commands.MustRelate("logs", parse.HasMany(tenanted[graphLog[int]](p, "logs"), "command_id", parse.Indexed()))

		assert.Equal(t, []int64{1}, ids(t, db, devices, depthTwoFilter, "1"))
		assert.Equal(t, []int64{1, 2}, ids(t, db, devices, depthTwoFilter, "2"), "tenant 2's error log on device 1's command is tenant 2's")
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
		devices := tenanted[graphDevice[int64]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(tenanted[graphCommand[int32]](p, "commands"), "device_id", parse.Indexed()))

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
		devices := tenanted[graphDevice[uuid.UUID]](p, "devices")
		devices.MustRelate("commands", parse.HasMany(tenanted[graphCommand[string]](p, "commands"), "device_id", parse.Indexed()))

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
