//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

type int32TenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID int32  `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type uint64TenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID uint64 `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type uuidTenantDevice struct {
	ID       int       `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID uuid.UUID `json:"-"    db:"tenant_id"`
	Name     string    `json:"name" db:"name" parse:"filter,sort,patch"`
}

type stringTenantDevice struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID string `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

// tenantKeyCase builds a plan's statements for a resource with one tenant
// key type.
type tenantKeyCase struct {
	name  string
	build func(t *testing.T, tenant string) (list, count, update parse.Statement, err error)
}

func tenantKeyStatements[T any](t *testing.T, tenant string) (parse.Statement, parse.Statement, parse.Statement, error) {
	t.Helper()
	r, err := parse.NewResource[T](onPostgres, "devices", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	req, err := r.ParseListInput(parse.ListInput{IncludeTotal: true})
	require.NoError(t, err)
	plan, err := r.BuildList(req, parse.Scope{Tenant: tenant})
	if err != nil {
		return parse.Statement{}, parse.Statement{}, parse.Statement{}, err
	}
	p, err := r.ParseMergePatch([]byte(`{"name":"renamed"}`))
	require.NoError(t, err)
	update, err := r.BuildUpdate(p, parse.ByID(1), parse.Scope{Tenant: tenant})
	if err != nil {
		return parse.Statement{}, parse.Statement{}, parse.Statement{}, err
	}
	return plan.Page, *plan.Count, update.Owner, nil
}

var tenantKeyCases = []tenantKeyCase{
	{"int32", tenantKeyStatements[int32TenantDevice]},
	{"uint64", tenantKeyStatements[uint64TenantDevice]},
	{"uuid", tenantKeyStatements[uuidTenantDevice]},
	{"string", tenantKeyStatements[stringTenantDevice]},
}

func TestTenantColumn_BindsTheKeyType(t *testing.T) {
	want := map[string]struct {
		tenant string
		bound  any
	}{
		"int32":  {"44", int64(44)},
		"uint64": {"18446744073709551615", uint64(18446744073709551615)},
		"uuid":   {"3F2504E0-4F89-41D3-9A0C-0305E82C3301", "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		"string": {"t-44", "t-44"},
	}
	for _, tc := range tenantKeyCases {
		t.Run(tc.name, func(t *testing.T) {
			w := want[tc.name]
			list, count, update, err := tc.build(t, w.tenant)
			require.NoError(t, err)
			for _, st := range []parse.Statement{list, count, update} {
				assert.Contains(t, st.Args, w.bound, st.SQL)
				if w.bound != w.tenant {
					assert.NotContains(t, st.Args, w.tenant, "the text is normalized, never bound as given")
				}
			}
		})
	}
}

func TestTenantColumn_RefusesTextTheKeyCannotHold(t *testing.T) {
	invalid := map[string][]string{
		"int32":  {"abc", "4.4", "1e3", " 44", "44 ", "0x2c", "2147483648", "-2147483649"},
		"uint64": {"-1", "18446744073709551616", "1.0"},
		"uuid":   {"not-a-uuid", "3f2504e0"},
	}
	for _, tc := range tenantKeyCases {
		for _, tenant := range invalid[tc.name] {
			t.Run(tc.name+"/"+tenant, func(t *testing.T) {
				_, _, _, err := tc.build(t, tenant)
				assert.ErrorIs(t, err, parse.ErrTenantInvalid)
			})
		}
		t.Run(tc.name+"/empty", func(t *testing.T) {
			_, _, _, err := tc.build(t, "")
			assert.ErrorIs(t, err, parse.ErrTenantRequired)
		})
	}
}

func TestTenantColumn_RefusesTypesThatAreNoTenantKey(t *testing.T) {
	type floatTenant struct {
		ID       int     `json:"id" db:"id"`
		TenantID float64 `json:"-"  db:"tenant_id"`
	}
	type timeTenant struct {
		ID       int       `json:"id" db:"id"`
		TenantID time.Time `json:"-"  db:"tenant_id"`
	}
	_, err := parse.NewResource[floatTenant](onPostgres, "a", parse.TenantColumn("tenant_id"))
	assert.ErrorContains(t, err, "a tenant key is a string, a UUID or an integer")
	_, err = parse.NewResource[timeTenant](onPostgres, "b", parse.TenantColumn("tenant_id"))
	assert.ErrorContains(t, err, "a tenant key is a string, a UUID or an integer")
}

type tenantKeyLabel struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Label    string `json:"label"     db:"label"     parse:"filter"`
}

func TestRelate_TenantKeysAgree(t *testing.T) {
	commands := parse.MustResource[parsetest.TenantKeyCommand](onPostgres, "commands", parse.TenantColumn("tenant_id"))
	labels := parse.MustResource[tenantKeyLabel](onPostgres, "labels", parse.TenantColumn("tenant_id"))

	t.Run("a target holding the tenant as another type is refused", func(t *testing.T) {
		devices := parse.MustResource[int32TenantDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
		err := devices.Relate("labels", parse.HasMany(labels, "device_id"))
		assert.ErrorContains(t, err, "tenant columns tenant_id (int) and labels.tenant_id (string) hold different types")
	})

	t.Run("a policy-scoped target's tenancy is its policy's", func(t *testing.T) {
		devices := parse.MustResource[int32TenantDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
		require.NoError(t, devices.Relate("labels", parse.HasMany(labels, "device_id", parse.PolicyScoped())))
	})

	t.Run("an owner without a tenant takes its target's key type", func(t *testing.T) {
		devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		req, err := devices.ParseListInput(parse.ListInput{Filter: `{"commands":{"$some":{"status":"pending"}}}`})
		require.NoError(t, err)
		plan, err := devices.BuildList(req, parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.Args, int64(44))
		assert.NotContains(t, plan.Page.Args, "44")

		_, err = devices.BuildList(req, parse.Scope{Tenant: "t-44"})
		assert.ErrorIs(t, err, parse.ErrTenantInvalid)
	})

	t.Run("pair tables, includes and membership writes bind the typed key", func(t *testing.T) {
		junction := parse.Junction{Table: "device_commands", Source: []string{"device_id"}, Target: []string{"command_id"}, Tenant: "tenant_id"}
		devices := parse.MustResource[int32TenantDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
		devices.MustRelate("commands", parse.ManyToMany(commands, junction, parse.Includable(), parse.Writable()))

		req, err := devices.ParseListInput(parse.ListInput{Include: []string{"commands"}})
		require.NoError(t, err)
		plan, err := devices.BuildList(req, parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		includes, err := devices.IncludeQueries(plan, []parse.Key{{1}})
		require.NoError(t, err)
		require.Len(t, includes, 1)
		assert.Contains(t, includes[0].Args, int64(44))
		assert.NotContains(t, includes[0].Args, "44")

		// The pair table declares no column types, so each pair holds what
		// the membership statements bound: the tenant as text would be
		// stored as text, and would delete no pair it replaces.
		provider, db := providertest.SQLite(t)
		dbtest.Exec(t, db,
			`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL)`,
			`CREATE TABLE commands (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, device_id INTEGER NOT NULL, status TEXT NOT NULL)`,
			`CREATE TABLE device_commands (device_id, command_id, tenant_id)`,
			`INSERT INTO devices VALUES (1, 44, 'alpha')`,
			`INSERT INTO commands VALUES (7, 44, 1, 'sent'), (8, 44, 1, 'sent')`,
			`INSERT INTO device_commands VALUES (1, 8, 44)`,
		)
		stored := parse.MustResource[int32TenantDevice](provider, "devices", parse.TenantColumn("tenant_id"))
		stored.MustRelate("commands", parse.ManyToMany(parse.MustResource[parsetest.TenantKeyCommand](provider, "commands", parse.TenantColumn("tenant_id")),
			junction, parse.Includable(), parse.Writable()))
		p, err := stored.ParseMergePatch([]byte(`{"commands":[7]}`))
		require.NoError(t, err)
		res, err := stored.Update(t.Context(), p, parse.ByID(1), parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), res.RelationRows, "the pair to 8 removed, the pair to 7 added")
		assert.Equal(t, [][]any{{int64(1), int64(7), "integer", int64(44)}},
			dbtest.QueryRows[[]any](t, db, `SELECT device_id, command_id, typeof(tenant_id), tenant_id FROM device_commands`))
	})
}
