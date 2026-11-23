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
		opts   []parse.UpdateOption
	}{
		{"duplicate-normalized", []parse.RowUpdate{{Key: parse.Key{int32(1)}, Patch: patch}, {Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"missing-tenant", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{}, nil},
		{"widened-scope", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1", IncludeDeleted: true}, parse.UpdateBatchLimits{}, nil},
		{"negative-limit", nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: -1}, nil},
		{"row-limit", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}, {Key: parse.Key{int64(2)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: 1}, nil},
		{"parameter-limit", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 1}, nil},
		{"invalid-key", []parse.RowUpdate{{Key: parse.Key{"not-an-integer"}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"relation", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: updateBatchPatch(t, r, `{"tags":["x"]}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"key-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.Increment("id")}},
		{"tenant-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.StampSQL("tenant_id", "'t2'")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(filter.SQLiteDialect{}, test.rows, test.scope, test.limits, test.opts...)
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
	plan, err := r.BuildUpdateBatch(filter.PostgresDialect{}, []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.NoError(t, plan.Chunks[0].CheckCurrent([]parse.Key{{id}}))
	require.ErrorIs(t, plan.Chunks[0].CheckCurrent(nil), parse.ErrUpdateBatchTargets)
	require.ErrorIs(t, plan.Chunks[0].CheckCurrent([]parse.Key{{uuid.MustParse("018c018c-0123-7123-8123-0123456789ac")}}), parse.ErrUpdateBatchTargets)
	_, err = r.BuildUpdateBatch(filter.PostgresDialect{}, []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}, {Key: parse.Key{strings.ToUpper(id.String())}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.Error(t, err)
}

// A batch locks its rows in the key's code-point order, under the collation
// every list orders the key by.
func TestUpdateBatchLocksInTheDialectsCodePointOrder(t *testing.T) {
	type row struct {
		Code string `db:"code"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	r, err := parse.NewResource[row]("coded_rows", parse.PrimaryKey("code"))
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	for _, d := range []parse.Dialect{filter.PostgresDialect{}, filter.PostgresDialect{Collation: filter.CollationC}, filter.SQLiteDialect{}} {
		plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{"b"}, Patch: p}, {Key: parse.Key{"a"}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Contains(t, plan.Chunks[0].Current.SQL, " ORDER BY "+d.CodePointOrder(`"coded_rows"."code"`)+d.RowLock())
	}
}
