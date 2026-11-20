//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
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
	r, err := parse.NewResource[T]("devices", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	req, err := r.ParseListInput(parse.ListInput{IncludeTotal: true})
	require.NoError(t, err)
	plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: tenant})
	if err != nil {
		return parse.Statement{}, parse.Statement{}, parse.Statement{}, err
	}
	p, err := r.ParseMergePatch([]byte(`{"name":"renamed"}`))
	require.NoError(t, err)
	update, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(1), parse.Scope{Tenant: tenant})
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
	_, err := parse.NewResource[floatTenant]("a", parse.TenantColumn("tenant_id"))
	assert.ErrorContains(t, err, "a tenant key is a string, a UUID or an integer")
	_, err = parse.NewResource[timeTenant]("b", parse.TenantColumn("tenant_id"))
	assert.ErrorContains(t, err, "a tenant key is a string, a UUID or an integer")
}

type tenantKeyCommand struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID int    `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}

type tenantKeyLabel struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Label    string `json:"label"     db:"label"     parse:"filter"`
}

type untenantedDevice struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort,patch"`
}

func TestRelate_TenantKeysAgree(t *testing.T) {
	commands := parse.MustResource[tenantKeyCommand]("commands", parse.TenantColumn("tenant_id"))
	labels := parse.MustResource[tenantKeyLabel]("labels", parse.TenantColumn("tenant_id"))

	t.Run("a target holding the tenant as another type is refused", func(t *testing.T) {
		devices := parse.MustResource[int32TenantDevice]("devices", parse.TenantColumn("tenant_id"))
		err := devices.Relate("labels", parse.HasMany(labels, "device_id"))
		assert.ErrorContains(t, err, "tenant column labels.tenant_id is string, but devices.tenant_id is int")
	})

	t.Run("a policy-scoped target's tenancy is its policy's", func(t *testing.T) {
		devices := parse.MustResource[int32TenantDevice]("devices", parse.TenantColumn("tenant_id"))
		require.NoError(t, devices.Relate("labels", parse.HasMany(labels, "device_id", parse.PolicyScoped())))
	})

	t.Run("an owner without a tenant takes its target's key type", func(t *testing.T) {
		devices := parse.MustResource[untenantedDevice]("devices")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		req, err := devices.ParseListInput(parse.ListInput{Filter: `{"commands":{"$some":{"status":"pending"}}}`})
		require.NoError(t, err)
		plan, err := devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.Args, int64(44))
		assert.NotContains(t, plan.Page.Args, "44")

		_, err = devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t-44"})
		assert.ErrorIs(t, err, parse.ErrTenantInvalid)
	})

	t.Run("pair tables, includes and membership writes bind the typed key", func(t *testing.T) {
		devices := parse.MustResource[int32TenantDevice]("devices", parse.TenantColumn("tenant_id"))
		devices.MustRelate("commands", parse.ManyToMany(commands,
			parse.Junction{Table: "device_commands", Source: "device_id", Target: "command_id", Tenant: "tenant_id"},
			parse.Includable(), parse.Writable()))

		req, err := devices.ParseListInput(parse.ListInput{Include: []string{"commands"}})
		require.NoError(t, err)
		plan, err := devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		includes, err := devices.IncludeQueries(plan, []any{1})
		require.NoError(t, err)
		require.Len(t, includes, 1)
		assert.Contains(t, includes[0].Args, int64(44))
		assert.NotContains(t, includes[0].Args, "44")

		p, err := devices.ParseMergePatch([]byte(`{"commands":[7]}`))
		require.NoError(t, err)
		update, err := devices.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(1), parse.Scope{Tenant: "44"})
		require.NoError(t, err)
		stmts, err := devices.RelationStatements(update, []any{int64(1)})
		require.NoError(t, err)
		require.NotEmpty(t, stmts)
		for _, st := range stmts {
			assert.Contains(t, st.Args, int64(44), st.SQL)
			assert.NotContains(t, st.Args, "44", st.SQL)
		}
	})
}
