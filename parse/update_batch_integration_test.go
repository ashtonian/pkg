//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateBatchSQLite(t *testing.T) {
	testUpdateBatchDatabase(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}
func TestUpdateBatchPostgres(t *testing.T) {
	testUpdateBatchDatabase(t, dbtest.Postgres(t), filter.PostgresDialect{})
}

func testUpdateBatchDatabase(t *testing.T, db *sql.DB, d parse.Dialect) {
	t.Helper()
	r := updateBatchResource(t)
	jsonType := "TEXT"
	if d.Name() == "postgres" {
		jsonType = "JSONB"
	}
	_, err := db.ExecContext(t.Context(), `CREATE TABLE batch_rows (id BIGINT PRIMARY KEY,tenant_id TEXT NOT NULL,reference TEXT CHECK(reference <> 'refused'),version BIGINT NOT NULL,meta `+jsonType+` NOT NULL,updated_at TEXT NOT NULL,deleted_at TEXT)`)
	require.NoError(t, err)
	for id := int64(1); id <= 42; id++ {
		tenant := "t1"
		var deleted *string
		if id == 41 {
			tenant = "t2"
		}
		if id == 42 {
			deleted = new("deleted")
		}
		_, err := db.ExecContext(t.Context(), `INSERT INTO batch_rows VALUES(`+d.Placeholder(1)+`,`+d.Placeholder(2)+`,`+d.Placeholder(3)+`,`+d.Placeholder(4)+`,`+d.Placeholder(5)+`,`+d.Placeholder(6)+`,`+d.Placeholder(7)+`)`, id, tenant, fmt.Sprintf("old-%d", id), int64(3), `{"value":"old","count":9}`, "old", deleted)
		require.NoError(t, err)
	}
	stamp := time.Date(2026, time.November, 8, 17, 0, 0, 0, time.UTC)
	var requests []parse.RowUpdate
	for id := int64(40); id >= 1; id-- {
		raw := fmt.Sprintf(`{"reference":"new-%d","meta":{"value":"new-%d"}}`, id, id)
		if id == 3 {
			raw = `{"reference":"old-3","meta":{"value":"old"}}`
		}
		if id == 4 {
			raw = `{}`
		}
		if id == 5 {
			raw = `{"reference":null}`
		}
		requests = append(requests, parse.RowUpdate{Key: parse.Key{id}, Patch: updateBatchPatch(t, r, raw), Policy: updateBatchVersion(3)})
	}
	plan, err := r.BuildUpdateBatch(d, requests, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 40}, parse.SkipUnchanged(), parse.Stamp("updated_at", stamp), parse.Increment("version"))
	require.NoError(t, err)
	require.Greater(t, len(plan.Chunks), 1)
	require.Less(t, len(plan.Chunks), len(requests))
	res, err := applyUpdateBatch(t, db, r, d, plan)
	require.NoError(t, err)
	require.Len(t, res.Changed, 38)
	for _, row := range res.Changed {
		assert.Equal(t, "t1", row.TenantID)
		assert.Equal(t, int64(4), row.Version)
		assert.Equal(t, core.FormatTimestamp(stamp), row.UpdatedAt)
		assert.Equal(t, 9, row.Meta.Count)
		if row.ID == 5 {
			assert.Nil(t, row.Reference)
		} else {
			require.NotNil(t, row.Reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.ID), *row.Reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.ID), row.Meta.Value)
		}
	}
	unchanged := make([]int64, len(res.Unchanged))
	for i, row := range res.Unchanged {
		unchanged[i] = row.ID
		assert.Equal(t, int64(3), row.Version, "row %d keeps its version", row.ID)
	}
	assert.ElementsMatch(t, []int64{3, 4}, unchanged, "3 already holds its patch; 4's patch is empty")
	assert.Zero(t, res.RelationRows)
	for _, id := range []int64{3, 4, 41, 42} {
		var version int64
		var updated string
		require.NoError(t, db.QueryRowContext(t.Context(), "SELECT version,updated_at FROM batch_rows WHERE id="+d.Placeholder(1), id).Scan(&version, &updated))
		assert.Equal(t, int64(3), version)
		assert.Equal(t, "old", updated)
	}
	for _, test := range []struct {
		name, raw, reference string
		key, version         int64
		opt                  parse.UpdateOption
	}{
		{"server-counter-owns-value", `{"reference":"counter-control","version":777}`, "counter-control", 3, 4, parse.Increment("version")},
		{"server-stamp-owns-value", `{"reference":"stamp-conflict","meta":{"value":"stamp-control"}}`, "server-stamp", 4, 3, parse.StampSQL("reference", "'server-stamp'")},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{test.key}, Patch: updateBatchPatch(t, r, test.raw)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, test.opt)
			require.NoError(t, err)
			res, err := applyUpdateBatch(t, db, r, d, plan)
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)
			assert.Empty(t, res.Unchanged)
			assert.Equal(t, test.version, res.Changed[0].Version)
			require.NotNil(t, res.Changed[0].Reference)
			assert.Equal(t, test.reference, *res.Changed[0].Reference)
		})
	}
	for _, test := range []struct {
		name string
		key  int64
		rev  int64
	}{
		{"stale", 2, 3}, {"missing", 99, 4}, {"foreign", 41, 3}, {"deleted", 42, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{
				{Key: parse.Key{int64(1)}, Patch: updateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
				{Key: parse.Key{test.key}, Patch: updateBatchPatch(t, r, `{"reference":"also-refused"}`), Policy: updateBatchVersion(test.rev)},
			}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
			require.NoError(t, err)
			res, err := applyUpdateBatch(t, db, r, d, plan)
			require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
			assert.Zero(t, res)
			assert.Equal(t, "new-1", storedReference(t, db, d, 1), "no chunk wrote")
		})
	}
	t.Run("late-constraint-rolls-back-earlier-chunk", func(t *testing.T) {
		plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{
			{Key: parse.Key{int64(1)}, Patch: updateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
			{Key: parse.Key{int64(2)}, Patch: updateBatchPatch(t, r, `{"reference":"refused"}`), Policy: updateBatchVersion(4)},
		}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
		require.NoError(t, err)
		res, err := applyUpdateBatch(t, db, r, d, plan)
		require.Error(t, err)
		assert.NotErrorIs(t, err, parse.ErrUpdateBatchTargets, "every target was found; the second owner failed")
		assert.Zero(t, res)
		assert.Equal(t, "new-1", storedReference(t, db, d, 1), "the first chunk's write is undone")
	})
}

func storedReference(t *testing.T, db *sql.DB, d parse.Dialect, id int64) string {
	t.Helper()
	var reference string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT reference FROM batch_rows WHERE id="+d.Placeholder(1), id).Scan(&reference))
	return reference
}

func updateBatchVersion(version int64) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "version", Operator: filter.OpEq, Value: version}}}
}
