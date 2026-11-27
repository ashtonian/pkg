//go:build unit

package parse_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type isEntity struct {
	ID   string `json:"id"   parse:"filter"`
	Name string `json:"name" parse:"filter,patch"`
}

// Code identity survives wrapping alongside cause, path and operation index.
func TestParseError_IsByCode(t *testing.T) {
	p, err := parse.NewResource[isEntity]("is_entities")
	require.NoError(t, err)

	_, err = p.ParseFilter(`{"absent":1}`)
	wrapped := fmt.Errorf("list devices: %w", err)
	assert.ErrorIs(t, wrapped, filter.ECodeUnknownField)
	assert.NotErrorIs(t, wrapped, filter.ECodeInvalidOperator)

	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/nope","value":1}]`))
	var pe *core.ParseError
	require.ErrorAs(t, fmt.Errorf("update: %w", err), &pe)
	assert.ErrorIs(t, err, patch.ECodePatchFieldNotFound)
	assert.Equal(t, "/nope", pe.Path)
	require.NotNil(t, pe.Index)
	assert.Equal(t, 0, *pe.Index)

	cause := errors.New("decoder failed")
	coded := core.NewError(filter.ECodeInvalidJSON, "", core.WithCause(cause))
	assert.ErrorIs(t, coded, cause, "the cause stays reachable")
	assert.ErrorIs(t, coded, filter.ECodeInvalidJSON)
	uncoded, alsoUncoded := core.NewError("", ""), core.NewError("", "")
	assert.NotErrorIs(t, uncoded, alsoUncoded, "an empty code matches nothing")
}

// Each root sentinel is reached with errors.Is from the call that returns
// it, under one prefix naming the package and the table once; the sentinel
// itself carries none.
func TestRootSentinels_OnePrefixShape(t *testing.T) {
	type device struct {
		ID       string `json:"id"        db:"id"        parse:"filter"`
		TenantID int64  `json:"tenant_id" db:"tenant_id"`
		Name     string `json:"name"      db:"name"      parse:"filter,patch"`
	}
	devices := parse.MustResource[device]("devices", parse.TenantColumn("tenant_id"))
	other := parse.MustResource[device]("devices", parse.TenantColumn("tenant_id"))
	d := filter.SQLiteDialect{}
	rename, err := devices.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	foreign, err := other.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	tenant := parse.Scope{Tenant: "1"}
	reg := parse.NewSchemaRegistry()
	parse.MustRegisterResource(reg, devices)
	run := func(build func() error) error { return build() }

	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"a list without a tenant", run(func() error { _, err := devices.BuildList(d, nil, parse.Scope{}); return err }), parse.ErrTenantRequired},
		{"an update without a tenant", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.ByID("a"), parse.Scope{}); return err }), parse.ErrTenantRequired},
		{"a tenant the column cannot hold", run(func() error { _, err := devices.BuildList(d, nil, parse.Scope{Tenant: "abc"}); return err }), parse.ErrTenantInvalid},
		{"no target", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.Target{}, tenant); return err }), parse.ErrNoTarget},
		{"a key of another type", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.ByID(7), tenant); return err }), parse.ErrKeyInvalid},
		{"one of several keys of another type", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.ByIDs("a", 7), tenant); return err }), parse.ErrKeyInvalid},
		{"a key holding U+0000", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.ByID("a\x00b"), tenant); return err }), parse.ErrKeyInvalid},
		{"a key of invalid UTF-8", run(func() error { _, err := devices.BuildUpdate(d, rename, parse.ByIDs("a", "\xff"), tenant); return err }), parse.ErrKeyInvalid},
		{"a batch key holding U+0000", run(func() error {
			_, err := devices.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{"a\x00"}, Patch: rename}}, tenant, parse.UpdateBatchLimits{})
			return err
		}), parse.ErrKeyInvalid},
		{"a batch key of another type", run(func() error {
			_, err := devices.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{7}, Patch: rename}}, tenant, parse.UpdateBatchLimits{})
			return err
		}), parse.ErrKeyInvalid},
		{"a widening scope", run(func() error {
			_, err := devices.BuildUpdate(d, rename, parse.ByID("a"), parse.Scope{Tenant: "1", IncludeDeleted: true})
			return err
		}), parse.ErrUpdateScope},
		{"another resource's patch", run(func() error { _, err := devices.BuildUpdate(d, foreign, parse.ByID("a"), tenant); return err }), parse.ErrForeign},
		{"a taken name", parse.RegisterResource(reg, other), parse.ErrRegistered},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.err, tt.want)
			msg := tt.err.Error()
			assert.True(t, strings.HasPrefix(msg, "parse: devices: "), msg)
			assert.Equal(t, 1, strings.Count(msg, "parse:"), msg)
			assert.Equal(t, 1, strings.Count(msg, "devices"), msg)
			assert.NotContains(t, tt.want.Error(), "parse:")
			_, isParseError := errors.AsType[*core.ParseError](tt.err)
			assert.False(t, isParseError, "the server's mistake, never the request's")
		})
	}

	assert.ErrorIs(t, parse.UpdateBatchChunk{}.CheckCurrent(nil), parse.ErrUpdateBatchTargets)
	assert.NotContains(t, parse.ErrUpdateBatchTargets.Error(), "parse:")
}
