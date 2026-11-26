//go:build unit

package parse_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// relateInOrder declares relations in order, or reversed, and returns the
// first refusal.
func relateInOrder(ownerFirst bool, relate ...func() error) error {
	if !ownerFirst {
		relate = slices.Clone(relate)
		slices.Reverse(relate)
	}
	for _, f := range relate {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func declarationOrders(t *testing.T, run func(t *testing.T, ownerFirst bool)) {
	t.Run("owner first", func(t *testing.T) { run(t, true) })
	t.Run("target first", func(t *testing.T) { run(t, false) })
}

// listArgs is the page statement's arguments for filterJSON under tenant.
func listArgs[T any](t *testing.T, r *parse.Resource[T], filterJSON, tenant string) ([]any, error) {
	t.Helper()
	req, err := r.ParseListInput(parse.ListInput{Filter: filterJSON})
	require.NoError(t, err)
	plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: tenant})
	if err != nil {
		return nil, err
	}
	return plan.Page.Args, nil
}

func TestTenantKey_EveryDepthBindsOneType(t *testing.T) {
	t.Run("a depth-two tenant column of another type is refused", func(t *testing.T) {
		tests := []struct {
			name    string
			declare func(ownerFirst bool) error
		}{
			{"integer owner, string at depth two", func(ownerFirst bool) error {
				devices, commands, logs := tenanted[graphDevice[int]]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[string]]("logs")
				return relateInOrder(ownerFirst,
					func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
					func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
			}},
			{"string owner, integer at depth two", func(ownerFirst bool) error {
				devices, commands, logs := tenanted[graphDevice[string]]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[int]]("logs")
				return relateInOrder(ownerFirst,
					func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
					func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
			}},
			{"through a policy-scoped target's relations", func(ownerFirst bool) error {
				devices, commands, logs := tenanted[graphDevice[int]]("devices"), tenanted[graphCommand[string]]("commands"), tenanted[graphLog[string]]("logs")
				return relateInOrder(ownerFirst,
					func() error {
						return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.PolicyScoped(), parse.Indexed()))
					},
					func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
			}},
			{"through a cycle", func(ownerFirst bool) error {
				devices, commands, logs := tenanted[graphDevice[int]]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[string]]("logs")
				return relateInOrder(ownerFirst,
					func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
					func() error { return commands.Relate("device", parse.BelongsTo(devices, "device_id")) },
					func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				declarationOrders(t, func(t *testing.T, ownerFirst bool) {
					assert.ErrorContains(t, tt.declare(ownerFirst), "hold different types")
				})
			})
		}
	})

	t.Run("a refused relation leaves the graph as it was", func(t *testing.T) {
		devices, commands := tenanted[graphDevice[int]]("devices"), parse.MustResource[untenantedCommand]("commands")
		devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
		require.Error(t, commands.Relate("logs", parse.HasMany(tenanted[graphLog[uuid.UUID]]("logs"), "command_id", parse.Indexed())))

		_, err := commands.ParseListInput(parse.ListInput{Filter: `{"logs":{"$some":{}}}`})
		assert.Error(t, err, "the refused relation is not declared")
		_, err = listArgs(t, commands, "", "t-44")
		assert.NoError(t, err, "commands reaches no UUID column")
		require.NoError(t, commands.Relate("logs", parse.HasMany(tenanted[graphLog[int32]]("logs"), "command_id", parse.Indexed())),
			"the refused target is not reached")
		args, err := listArgs(t, devices, depthTwoFilter, "44")
		require.NoError(t, err)
		assert.Contains(t, args, int64(44))
	})

	t.Run("an owner without a tenant binds a depth-two column's type", func(t *testing.T) {
		declarationOrders(t, func(t *testing.T, ownerFirst bool) {
			devices, commands, logs := parse.MustResource[untenantedDevice]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[int]]("logs")
			require.NoError(t, relateInOrder(ownerFirst,
				func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
				func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) }))

			args, err := listArgs(t, devices, depthTwoFilter, "44")
			require.NoError(t, err)
			assert.Contains(t, args, int64(44))
			assert.NotContains(t, args, "44")
			_, err = listArgs(t, devices, depthTwoFilter, "t-44")
			assert.ErrorIs(t, err, parse.ErrTenantInvalid)
		})
	})
}

func TestTenantKey_IntegersAreRangeCheckedAgainstEveryColumn(t *testing.T) {
	tests := []struct {
		name string
		// declare relates integer tenant columns of two kinds and returns
		// the owner's list through a relation.
		declare func(ownerFirst bool) (list func(t *testing.T, tenant string) ([]any, error), err error)
		refused []string
	}{
		{
			name: "int64 owner, int32 target",
			declare: func(ownerFirst bool) (func(*testing.T, string) ([]any, error), error) {
				devices, commands := tenanted[graphDevice[int64]]("devices"), tenanted[graphCommand[int32]]("commands")
				err := devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
				return func(t *testing.T, tenant string) ([]any, error) {
					return listArgs(t, devices, `{"commands":{"$some":{"status":"sent"}}}`, tenant)
				}, err
			},
			refused: []string{"3000000000", "-2147483649"},
		},
		{
			name: "an owner without a tenant, int32 and int64 targets",
			declare: func(ownerFirst bool) (func(*testing.T, string) ([]any, error), error) {
				devices := parse.MustResource[untenantedDevice]("devices")
				narrow, wide := tenanted[graphCommand[int32]]("commands"), tenanted[graphCommand[int64]]("jobs")
				err := relateInOrder(ownerFirst,
					func() error { return devices.Relate("commands", parse.HasMany(narrow, "device_id", parse.Indexed())) },
					func() error { return devices.Relate("jobs", parse.HasMany(wide, "device_id", parse.Indexed())) })
				return func(t *testing.T, tenant string) ([]any, error) {
					return listArgs(t, devices, `{"jobs":{"$some":{"status":"sent"}}}`, tenant)
				}, err
			},
			refused: []string{"3000000000"},
		},
		{
			name: "int64 owner, int32 at depth two",
			declare: func(ownerFirst bool) (func(*testing.T, string) ([]any, error), error) {
				devices, commands, logs := tenanted[graphDevice[int64]]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[int32]]("logs")
				err := relateInOrder(ownerFirst,
					func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
					func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
				return func(t *testing.T, tenant string) ([]any, error) {
					return listArgs(t, devices, depthTwoFilter, tenant)
				}, err
			},
			refused: []string{"3000000000"},
		},
		{
			name: "uint64 owner, int32 target",
			declare: func(ownerFirst bool) (func(*testing.T, string) ([]any, error), error) {
				devices, commands := tenanted[graphDevice[uint64]]("devices"), tenanted[graphCommand[int32]]("commands")
				err := devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
				return func(t *testing.T, tenant string) ([]any, error) {
					return listArgs(t, devices, `{"commands":{"$some":{"status":"sent"}}}`, tenant)
				}, err
			},
			refused: []string{"-1", "3000000000", "+44"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declarationOrders(t, func(t *testing.T, ownerFirst bool) {
				list, err := tt.declare(ownerFirst)
				require.NoError(t, err, "integer columns of different widths agree")
				for _, tenant := range tt.refused {
					_, err := list(t, tenant)
					assert.ErrorIs(t, err, parse.ErrTenantInvalid, tenant)
				}
				args, err := list(t, "44")
				require.NoError(t, err)
				assert.Contains(t, args, int64(44))
			})
		})
	}
}

func TestTenantKey_StringBesideUUIDBindsTheCanonicalUUID(t *testing.T) {
	const given, canonical = "3F2504E0-4F89-41D3-9A0C-0305E82C3301", "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	tests := []struct {
		name    string
		declare func(ownerFirst bool) (func(t *testing.T, tenant string) ([]any, error), error)
	}{
		{"uuid owner, string target", func(bool) (func(*testing.T, string) ([]any, error), error) {
			devices, commands := tenanted[graphDevice[uuid.UUID]]("devices"), tenanted[graphCommand[string]]("commands")
			err := devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
			return func(t *testing.T, tenant string) ([]any, error) {
				return listArgs(t, devices, `{"commands":{"$some":{"status":"sent"}}}`, tenant)
			}, err
		}},
		{"string owner, uuid target", func(bool) (func(*testing.T, string) ([]any, error), error) {
			devices, commands := tenanted[graphDevice[string]]("devices"), tenanted[graphCommand[uuid.UUID]]("commands")
			err := devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
			return func(t *testing.T, tenant string) ([]any, error) {
				return listArgs(t, devices, `{"commands":{"$some":{"status":"sent"}}}`, tenant)
			}, err
		}},
		{"string owner, uuid at depth two", func(ownerFirst bool) (func(*testing.T, string) ([]any, error), error) {
			devices, commands, logs := tenanted[graphDevice[string]]("devices"), parse.MustResource[untenantedCommand]("commands"), tenanted[graphLog[uuid.UUID]]("logs")
			err := relateInOrder(ownerFirst,
				func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) },
				func() error { return commands.Relate("logs", parse.HasMany(logs, "command_id", parse.Indexed())) })
			return func(t *testing.T, tenant string) ([]any, error) {
				return listArgs(t, devices, depthTwoFilter, tenant)
			}, err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declarationOrders(t, func(t *testing.T, ownerFirst bool) {
				list, err := tt.declare(ownerFirst)
				require.NoError(t, err, "a string column holds a UUID's text")
				args, err := list(t, given)
				require.NoError(t, err)
				assert.Contains(t, args, canonical)
				assert.NotContains(t, args, given, "every tenant column binds the canonical text")
				_, err = list(t, "t-44")
				assert.ErrorIs(t, err, parse.ErrTenantInvalid, "a UUID column is reached")
			})
		})
	}

	t.Run("an integer beside a UUID is refused", func(t *testing.T) {
		devices, commands := tenanted[graphDevice[uuid.UUID]]("devices"), tenanted[graphCommand[int]]("commands")
		assert.ErrorContains(t, devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())), "hold different types")
	})
}

func TestTenantKey_PairTablesBindTheResourceKey(t *testing.T) {
	pairs := parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}

	t.Run("an owner without a tenant binds its typed key into every pair table", func(t *testing.T) {
		tests := []struct {
			name  string
			typed func(devices *parse.Resource[untenantedDevice]) func() error
		}{
			{"a typed target", func(devices *parse.Resource[untenantedDevice]) func() error {
				return func() error {
					return devices.Relate("commands", parse.HasMany(tenanted[graphCommand[int]]("commands"), "device_id", parse.Indexed()))
				}
			}},
			{"a typed column at depth two", func(devices *parse.Resource[untenantedDevice]) func() error {
				commands := parse.MustResource[untenantedCommand]("commands")
				commands.MustRelate("logs", parse.HasMany(tenanted[graphLog[int]]("logs"), "command_id", parse.Indexed()))
				return func() error { return devices.Relate("commands", parse.HasMany(commands, "device_id", parse.Indexed())) }
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				declarationOrders(t, func(t *testing.T, ownerFirst bool) {
					devices := parse.MustResource[untenantedDevice]("devices")
					tags := parse.MustResource[untenantedTag]("tags")
					require.NoError(t, relateInOrder(ownerFirst,
						tt.typed(devices),
						func() error { return devices.Relate("tags", parse.ManyToMany(tags, pairs, parse.Indexed())) }))

					args, err := listArgs(t, devices, `{"tags":20}`, "44")
					require.NoError(t, err)
					assert.Contains(t, args, int64(44), "the pair table's tenant is the request's one tenant, in its type")
					assert.NotContains(t, args, "44")
				})
			})
		}
	})

	t.Run("a graph without a typed tenant column binds the text", func(t *testing.T) {
		devices := parse.MustResource[untenantedDevice]("devices")
		devices.MustRelate("tags", parse.ManyToMany(parse.MustResource[untenantedTag]("tags"), pairs, parse.Indexed()))
		args, err := listArgs(t, devices, `{"tags":20}`, "44")
		require.NoError(t, err)
		assert.Contains(t, args, "44")
	})
}

func TestBuildUpdateBatch_RefusesAnInvalidTenantBeforeItsRows(t *testing.T) {
	devices := tenanted[graphDevice[int32]]("devices")
	duplicate := func() []parse.RowUpdate {
		p, err := devices.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		rows := []parse.RowUpdate{{Key: parse.Key{1}, Patch: p}, {Key: parse.Key{1}, Patch: p}}
		return rows
	}
	tests := []struct {
		name string
		rows []parse.RowUpdate
	}{
		{"no rows", nil},
		{"rows the batch would refuse", duplicate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := devices.BuildUpdateBatch(filter.PostgresDialect{}, tt.rows, parse.Scope{Tenant: "t-44"}, parse.UpdateBatchLimits{})
			assert.ErrorIs(t, err, parse.ErrTenantInvalid)
		})
	}
}

func TestTenantKey_ErrorsNameEachPartOnce(t *testing.T) {
	type floatTenant struct {
		ID       int     `json:"id" db:"id"`
		TenantID float64 `json:"-"  db:"tenant_id"`
	}
	tests := []struct {
		name string
		err  func() error
		want string
	}{
		{
			name: "the resource's own column",
			err: func() error {
				_, err := listArgs(t, tenanted[graphDevice[int32]]("devices"), "", "abc")
				return err
			},
			want: `parse: devices: Scope.Tenant is not a value of the tenant column's type: tenant_id: "abc" is not a decimal integer`,
		},
		{
			name: "a related column",
			err: func() error {
				devices := tenanted[graphDevice[int64]]("devices")
				devices.MustRelate("commands", parse.HasMany(tenanted[graphCommand[int32]]("commands"), "device_id", parse.Indexed()))
				_, err := listArgs(t, devices, "", "3000000000")
				return err
			},
			want: `parse: devices: Scope.Tenant is not a value of the tenant column's type: commands.tenant_id: 3000000000 overflows int32`,
		},
		{
			name: "beyond 64 bits",
			err: func() error {
				_, err := listArgs(t, tenanted[graphDevice[uint64]]("devices"), "", "18446744073709551616")
				return err
			},
			want: `parse: devices: Scope.Tenant is not a value of the tenant column's type: tenant_id: 18446744073709551616 overflows uint64`,
		},
		{
			name: "a type that is no tenant key",
			err: func() error {
				_, err := parse.NewResource[floatTenant]("readings", parse.TenantColumn("tenant_id"))
				return err
			},
			want: `parse: readings: tenant column tenant_id is float; a tenant key is a string, a UUID or an integer`,
		},
		{
			name: "a target of another type",
			err: func() error {
				devices := tenanted[graphDevice[int]]("devices")
				return devices.Relate("commands", parse.HasMany(tenanted[graphCommand[string]]("commands"), "device_id", parse.Indexed()))
			},
			want: `parse: devices: relation "commands": tenant columns tenant_id (int) and commands.tenant_id (string) hold different types, but one request binds its tenant to both`,
		},
		{
			name: "a dependant reaching another type",
			err: func() error {
				devices, commands := tenanted[graphDevice[int]]("devices"), parse.MustResource[untenantedCommand]("commands")
				devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
				return commands.Relate("logs", parse.HasMany(tenanted[graphLog[string]]("logs"), "command_id", parse.Indexed()))
			},
			want: `parse: commands: relation "logs": devices binds one request's tenant to tenant columns devices.tenant_id (int) and logs.tenant_id (string), which hold different types`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.EqualError(t, tt.err(), tt.want)
		})
	}
}
