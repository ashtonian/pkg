//go:build unit

package parse_test

import (
	"strings"
	"testing"
	"uuid"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/require"
)

func TestUpdateBatchRefusals(t *testing.T) {
	r := updateBatchResource(t)
	patch := updateBatchPatch(t, r, `{"reference":"new"}`)
	for _, test := range []struct {
		name   string
		rows   []parse.RowUpdate
		scope  parse.Scope
		limits parse.UpdateBatchLimits
	}{
		{"duplicate-normalized", []parse.RowUpdate{{Key: int32(1), Patch: patch}, {Key: int64(1), Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}},
		{"missing-tenant", []parse.RowUpdate{{Key: int64(1), Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{}},
		{"widened-scope", []parse.RowUpdate{{Key: int64(1), Patch: patch}}, parse.Scope{Tenant: "t1", IncludeDeleted: true}, parse.UpdateBatchLimits{}},
		{"negative-limit", nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: -1}},
		{"row-limit", []parse.RowUpdate{{Key: int64(1), Patch: patch}, {Key: int64(2), Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: 1}},
		{"parameter-limit", []parse.RowUpdate{{Key: int64(1), Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 1}},
		{"invalid-key", []parse.RowUpdate{{Key: "not-an-integer", Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}},
		{"relation", []parse.RowUpdate{{Key: int64(1), Patch: updateBatchPatch(t, r, `{"tags":["x"]}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}},
		{"key-write", []parse.RowUpdate{{Key: int64(1), Patch: updateBatchPatch(t, r, `{"id":2}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}},
		{"tenant-write", []parse.RowUpdate{{Key: int64(1), Patch: updateBatchPatch(t, r, `{"tenant_id":"t2"}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(filter.SQLiteDialect{}, test.rows, test.scope, test.limits)
			require.Error(t, err)
			require.Nil(t, plan)
		})
	}
	empty, err := r.BuildUpdateBatch(filter.PostgresDialect{}, nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Empty(t, empty.Chunks)
}

func TestUpdateBatchCanonicalKeys(t *testing.T) {
	type row struct {
		ID   uuid.UUID `db:"id"`
		Name string    `json:"name" db:"name" parse:"patch"`
	}
	r, err := parse.NewResource[row]("uuid_rows")
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	id := uuid.MustParse("018c018c-0123-7123-8123-0123456789ab")
	plan, err := r.BuildUpdateBatch(filter.PostgresDialect{}, []parse.RowUpdate{{Key: id, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.NoError(t, plan.Chunks[0].CheckCurrent([]any{id}))
	require.ErrorIs(t, plan.Chunks[0].CheckCurrent(nil), parse.ErrUpdateBatchTargets)
	require.ErrorIs(t, plan.Chunks[0].CheckCurrent([]any{uuid.MustParse("018c018c-0123-7123-8123-0123456789ac")}), parse.ErrUpdateBatchTargets)
	_, err = r.BuildUpdateBatch(filter.PostgresDialect{}, []parse.RowUpdate{{Key: id, Patch: p}, {Key: strings.ToUpper(id.String()), Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.Error(t, err)
}
