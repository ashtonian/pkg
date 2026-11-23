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

type networkRow struct {
	ID         string          `json:"id" db:"id" parse:"filter,sort"`
	At         *core.LocalTime `json:"at" db:"at" parse:"filter,patch,sort"`
	Retention  *core.Interval  `json:"retention" db:"retention" parse:"filter,patch"`
	ActorIP    *core.Inet      `json:"actor_ip" db:"actor_ip" parse:"filter,patch"`
	Network    *core.CIDR      `json:"network" db:"network" parse:"filter,patch"`
	Device     *core.MACAddr   `json:"device" db:"device" parse:"filter,patch"`
	AllowedIPs []core.Inet     `json:"allowed_ips" db:"allowed_ips" parse:"filter,patch"`
	Spans      []core.Interval `json:"spans" db:"spans" parse:"filter,patch"`
}

// pgx scans the value types through their sql.Scanner from its binary
// (extended) and text (simple) results, whatever the session's
// IntervalStyle, into the canonical text; a time cursor pages in time order.
func TestSQLTextValuesNativeRows(t *testing.T) {
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	ctx := testContext(t)
	setup := c.open(t, pgx.QueryExecModeCacheStatement)
	_, err := setup.Exec(ctx, `CREATE TABLE network_rows (id TEXT PRIMARY KEY, at TIME, retention INTERVAL, actor_ip INET, network CIDR,
		device MACADDR, allowed_ips INET[], spans INTERVAL[]);
		INSERT INTO network_rows VALUES
		('a', '10:30:00.500', '1 mon', '192.168.0.5/32', '10.0.0.0/8', '08-00-2B-01-02-03', '{10.0.0.1,2001:0db8::1}', '{"1 mon","-1 day 02:00:00"}'),
		('b', '09:00', '30 days', '2001:db8::1', '2001:db8::/32', NULL, '{}', NULL),
		('c', NULL, NULL, '192.168.0.5/24', NULL, '0800.2b01.0204', NULL, '{}')`)
	require.NoError(t, err)
	resource := parse.MustResource[networkRow]("network_rows")
	want := `[
		{"id":"b","at":"09:00:00","retention":"P30D","actor_ip":"2001:db8::1","network":"2001:db8::/32","device":null,"allowed_ips":[],"spans":null},
		{"id":"a","at":"10:30:00.5","retention":"P1M","actor_ip":"192.168.0.5","network":"10.0.0.0/8","device":"08:00:2b:01:02:03",
			"allowed_ips":["10.0.0.1","2001:db8::1"],"spans":["P1M","P-1DT2H"]},
		{"id":"c","at":null,"retention":null,"actor_ip":"192.168.0.5/24","network":null,"device":"08:00:2b:01:02:04","allowed_ips":null,"spans":[]}]`
	for name, mode := range map[string]pgx.QueryExecMode{"extended": pgx.QueryExecModeCacheStatement, "simple": pgx.QueryExecModeSimpleProtocol} {
		t.Run(name, func(t *testing.T) {
			pool := c.open(t, mode)
			conn, err := pool.Acquire(ctx)
			require.NoError(t, err)
			defer conn.Release()
			_, err = conn.Exec(ctx, "SET IntervalStyle = sql_standard")
			require.NoError(t, err)

			cursor := ""
			var rows []networkRow
			for range 4 {
				request, err := resource.ParseList(url.Values{"sort": {"at,id"}, "page_size": {"1"}, "cursor": {cursor}})
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
			assert.JSONEq(t, want, string(wire))

			request, err := resource.ParseList(url.Values{"filter": {`{"retention":"P30D","actor_ip":{"$in":["2001:0DB8::1"]}}`}})
			require.NoError(t, err)
			plan, err := resource.BuildList(filter.PostgresDialect{}, request, parse.Scope{})
			require.NoError(t, err)
			page, err := parsepgx.QueryPage(ctx, conn, resource, plan)
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, "b", page.Items[0].ID, "P30D is not P1M")
		})
	}

	patch, err := resource.ParseMergePatch([]byte(`{"at":"23:59:59.999999000","retention":"PT90M","device":"08:00:2B:01:02:05",
		"allowed_ips":["::ffff:10.0.0.9"],"spans":["P1W"]}`))
	require.NoError(t, err)
	update, err := resource.BuildUpdate(filter.PostgresDialect{}, patch, parse.ByID("b"), parse.Scope{})
	require.NoError(t, err)
	got, err := parsepgx.Update(ctx, setup, resource, update)
	require.NoError(t, err)
	require.Len(t, got.Changed, 1)
	wire, err := json.Marshal(got.Changed[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"b","at":"23:59:59.999999","retention":"PT1H30M","actor_ip":"2001:db8::1","network":"2001:db8::/32",
		"device":"08:00:2b:01:02:05","allowed_ips":["::ffff:10.0.0.9"],"spans":["P7D"]}`, string(wire))
}
