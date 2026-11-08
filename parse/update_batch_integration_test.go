//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type updateBatchImage struct {
	id, version int64
	tenant      string
	reference   *string
	meta        updateBatchDocument
	stamp       string
	deleted     *string
}

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
		requests = append(requests, parse.RowUpdate{Key: id, Patch: updateBatchPatch(t, r, raw), Policy: updateBatchVersion(3)})
	}
	plan, err := r.BuildUpdateBatch(d, requests, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 40}, parse.SkipUnchanged(), parse.Stamp("updated_at", stamp), parse.Increment("version"))
	require.NoError(t, err)
	require.Greater(t, len(plan.Chunks), 1)
	require.Less(t, len(plan.Chunks), len(requests))
	changed, statements, err := executeUpdateBatch(t.Context(), db, d, plan)
	require.NoError(t, err)
	require.Len(t, changed, 38)
	assert.Equal(t, 2*len(plan.Chunks), statements)
	for _, row := range changed {
		assert.Equal(t, "t1", row.tenant)
		assert.Equal(t, int64(4), row.version)
		assert.Equal(t, core.FormatTimestamp(stamp), row.stamp)
		assert.Equal(t, 9, row.meta.Count)
		if row.id == 5 {
			assert.Nil(t, row.reference)
		} else {
			require.NotNil(t, row.reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.id), *row.reference)
			assert.Equal(t, fmt.Sprintf("new-%d", row.id), row.meta.Value)
		}
	}
	for _, id := range []int64{3, 4, 41, 42} {
		var version int64
		var updated string
		require.NoError(t, db.QueryRowContext(t.Context(), "SELECT version,updated_at FROM batch_rows WHERE id="+d.Placeholder(1), id).Scan(&version, &updated))
		assert.Equal(t, int64(3), version)
		assert.Equal(t, "old", updated)
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
				{Key: int64(1), Patch: updateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
				{Key: test.key, Patch: updateBatchPatch(t, r, `{"reference":"also-refused"}`), Policy: updateBatchVersion(test.rev)},
			}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
			require.NoError(t, err)
			_, statements, err := executeUpdateBatch(t.Context(), db, d, plan)
			require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
			assert.LessOrEqual(t, statements, len(plan.Chunks))
		})
	}
	t.Run("late-constraint-rolls-back-earlier-chunk", func(t *testing.T) {
		plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{
			{Key: int64(1), Patch: updateBatchPatch(t, r, `{"reference":"must-not-write"}`), Policy: updateBatchVersion(4)},
			{Key: int64(2), Patch: updateBatchPatch(t, r, `{"reference":"refused"}`), Policy: updateBatchVersion(4)},
		}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 4}, parse.SkipUnchanged())
		require.NoError(t, err)
		_, statements, err := executeUpdateBatch(t.Context(), db, d, plan)
		require.Error(t, err)
		assert.Greater(t, statements, len(plan.Chunks))
	})
	var reference string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT reference FROM batch_rows WHERE id=1").Scan(&reference))
	assert.Equal(t, "new-1", reference)
}

func executeUpdateBatch(ctx context.Context, db *sql.DB, d parse.Dialect, plan *parse.UpdateBatchPlan) (out []updateBatchImage, statements int, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	begin := "BEGIN"
	if d.Name() == "sqlite" {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return nil, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
			err = errors.Join(err, rollbackErr)
			out = nil
		}
	}()
	for _, chunk := range plan.Chunks {
		statements++
		rows, err := queryUpdateBatch(ctx, conn, chunk.Current)
		if err != nil {
			return nil, statements, err
		}
		keys := make([]any, len(rows))
		for i, row := range rows {
			keys[i] = row.id
		}
		if err := chunk.CheckCurrent(keys); err != nil {
			return nil, statements, err
		}
	}
	for _, chunk := range plan.Chunks {
		if chunk.Owner == nil {
			continue
		}
		statements++
		rows, err := queryUpdateBatch(ctx, conn, *chunk.Owner)
		if err != nil {
			return nil, statements, err
		}
		out = append(out, rows...)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, statements, err
	}
	committed = true
	return out, statements, nil
}

func queryUpdateBatch(ctx context.Context, conn *sql.Conn, statement parse.Statement) (out []updateBatchImage, err error) {
	rows, err := conn.QueryContext(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var row updateBatchImage
		var raw string
		if err := rows.Scan(&row.id, &row.tenant, &row.reference, &row.version, &raw, &row.stamp, &row.deleted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &row.meta); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
