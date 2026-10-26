//go:build integration

package parsepgx_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type datedRow struct {
	ID  string     `json:"id" db:"id" parse:"filter,sort"`
	Day *core.Date `json:"day" db:"day" parse:"filter,patch,sort"`
}

func TestCivilDateNativeRowsAndCursor(t *testing.T) {
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	ctx := testContext(t)
	setup := c.open(t, pgx.QueryExecModeCacheStatement)
	_, err := setup.Exec(ctx, `CREATE TABLE dated_rows (id TEXT PRIMARY KEY, day DATE); INSERT INTO dated_rows VALUES ('a','2026-10-26'),('b','2028-02-29'),('c',NULL)`)
	require.NoError(t, err)
	resource := parse.MustResource[datedRow]("dated_rows")
	for name, mode := range map[string]pgx.QueryExecMode{"extended": pgx.QueryExecModeCacheStatement, "simple": pgx.QueryExecModeSimpleProtocol} {
		t.Run(name, func(t *testing.T) {
			pool := c.open(t, mode)
			conn, err := pool.Acquire(ctx)
			require.NoError(t, err)
			defer conn.Release()
			for _, zone := range []string{"Pacific/Kiritimati", "Etc/GMT+12"} {
				_, err = conn.Exec(ctx, "SET TIME ZONE '"+zone+"'")
				require.NoError(t, err)
				cursor := ""
				var rows []datedRow
				for range 3 {
					request, err := resource.ParseList(url.Values{"sort": {"day,id"}, "page_size": {"1"}, "cursor": {cursor}})
					require.NoError(t, err)
					plan, err := resource.BuildList(filter.PostgresDialect{}, request, parse.Scope{})
					require.NoError(t, err)
					page, err := parsepgx.QueryPage(ctx, conn, resource, plan)
					require.NoError(t, err)
					rows = append(rows, page.Items...)
					if !page.PageInfo.HasNextPage {
						break
					}
					cursor = string(page.PageInfo.EndCursor)
				}
				wire, err := json.Marshal(rows)
				require.NoError(t, err)
				assert.JSONEq(t, `[{"id":"a","day":"2026-10-26"},{"id":"b","day":"2028-02-29"},{"id":"c","day":null}]`, string(wire))
			}
		})
	}
}
