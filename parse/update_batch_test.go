//go:build unit

package parse_test

import (
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

func TestUpdateBatchRefusals(t *testing.T) {
	r := parsetest.UpdateBatchResource(t, onPostgres)
	patch := parsetest.UpdateBatchPatch(t, r, `{"reference":"new"}`)
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
		{"relation", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: parsetest.UpdateBatchPatch(t, r, `{"tags":["x"]}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"key-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.Increment("id")}},
		{"tenant-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.StampSQL("tenant_id", "'t2'")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(test.rows, test.scope, test.limits, test.opts...)
			require.Error(t, err)
			require.Nil(t, plan)
		})
	}
	empty, err := r.BuildUpdateBatch(nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Empty(t, empty.Chunks)
}

// A batch's targets are found by their keys' canonical values: the row a
// key names is its target, a key no row holds fails the batch, and a key
// spelled two ways is one row twice.
func TestUpdateBatchCanonicalKeys(t *testing.T) {
	type row struct {
		ID   uuid.UUID `db:"id"`
		Name string    `json:"name" db:"name" parse:"patch"`
	}
	id := uuid.MustParse("018c018c-0123-7123-8123-0123456789ab")
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db, `CREATE TABLE uuid_rows (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO uuid_rows VALUES ('`+id.String()+`', 'old')`)
	r, err := parse.NewResource[row](provider, "uuid_rows")
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	assert.Equal(t, []row{{ID: id, Name: "new"}}, res.Changed)
	_, err = r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{uuid.MustParse("018c018c-0123-7123-8123-0123456789ac")}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{id}, Patch: p}, {Key: parse.Key{strings.ToUpper(id.String())}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.Error(t, err)
}

// A batch locks its rows in the key's code-point order, under the collation
// every list orders the key by.
func TestUpdateBatchLocksInTheDialectsCodePointOrder(t *testing.T) {
	type row struct {
		Code string `db:"code"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.PostgresDialect{Collation: filter.CollationC}, filter.SQLiteDialect{}} {
		r, err := parse.NewResource[row](providertest.Compile(d), "coded_rows", parse.PrimaryKey("code"))
		require.NoError(t, err)
		p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{"b"}, Patch: p}, {Key: parse.Key{"a"}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Contains(t, plan.Chunks[0].Current.SQL, " ORDER BY "+d.CodePointOrder(`"coded_rows"."code"`)+d.RowLock())
	}
}
