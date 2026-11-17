//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLTextValuesSQL writes each value type on both backends, reads back
// its canonical text, and checks that every filter the type offers selects
// the same rows on both.
func TestSQLTextValuesSQL(t *testing.T) {
	for _, backend := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect parse.Dialect
		ddl     string
		array   func(...string) any
	}{
		{
			name: "sqlite", open: func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, dialect: filter.SQLiteDialect{},
			ddl: `CREATE TABLE network_events (id TEXT PRIMARY KEY, at TEXT, retention TEXT, actor_ip TEXT, network TEXT, device TEXT,
				allowed_ips TEXT, spans TEXT)`,
			array: func(items ...string) any {
				b, _ := json.Marshal(items) // a []string always marshals
				return string(b)
			},
		},
		{
			name: "postgres", dialect: filter.PostgresDialect{},
			open: func(t *testing.T) *sql.DB {
				db := dbtest.Postgres(t)
				// One connection, so the session's IntervalStyle holds for every
				// statement: reads must not depend on it.
				db.SetMaxOpenConns(1)
				_, err := db.Exec(`SET IntervalStyle = sql_standard`)
				require.NoError(t, err)
				return db
			},
			ddl: `CREATE TABLE network_events (id TEXT PRIMARY KEY, at TIME, retention INTERVAL, actor_ip INET, network CIDR, device MACADDR,
				allowed_ips INET[], spans INTERVAL[])`,
			array: func(items ...string) any { return items },
		},
	} {
		t.Run(backend.name, func(t *testing.T) {
			db := backend.open(t)
			_, err := db.Exec(backend.ddl)
			require.NoError(t, err)
			resource := parse.MustResource[networkEvent]("network_events")
			insert := func(id string, at core.LocalTime, retention core.Interval, actorIP, network string, device any, spans []string, allowed ...string) {
				t.Helper()
				ip, err := core.ParseInet(actorIP)
				require.NoError(t, err)
				cidr, err := core.ParseCIDR(network)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO network_events VALUES (`+placeholders(backend.dialect, 8)+`)`,
					id, at, retention, ip, cidr, device, backend.array(allowed...), backend.array(spans...))
				require.NoError(t, err)
			}
			// LocalTime and MACAddr values converted from non-canonical text
			// write their canonical text.
			insert("a", "10:30:00.500", core.Interval{Months: 1}, "192.168.0.5/32", "10.0.0.0/8", core.MACAddr("08-00-2B-01-02-03"),
				[]string{"P1M"}, "10.0.0.1", "2001:db8::1")
			insert("b", "09:00:00", core.Interval{Days: 30}, "2001:db8::1", "2001:db8::/32", core.MACAddr("08:00:2b:01:02:04"),
				[]string{"P30D", "PT1H"}, "10.0.0.2")
			insert("c", "23:59:59.999999", core.Interval{Days: 1, Microseconds: -3_600_000_000}, "192.168.0.5/24", "192.168.0.0/24", nil, nil)

			list := func(filterJSON, sort string) []networkEvent {
				t.Helper()
				query, err := resource.ParseList(url.Values{"filter": {filterJSON}, "sort": {sort}})
				require.NoError(t, err)
				plan, err := resource.BuildList(backend.dialect, query, parse.Scope{})
				require.NoError(t, err)
				rows, err := db.Query(plan.Page.SQL, plan.Page.Args...)
				require.NoError(t, err)
				defer rows.Close()
				var found []networkEvent
				for rows.Next() {
					found = append(found, scanNetworkEvent(t, rows))
				}
				require.NoError(t, rows.Err())
				return found
			}
			ids := func(filterJSON string) []string {
				t.Helper()
				var out []string
				for _, row := range list(filterJSON, "id") {
					out = append(out, row.ID)
				}
				return out
			}

			for _, tt := range []struct {
				filter string
				want   []string
			}{
				// PostgreSQL's own interval equality matches P30D with P1M.
				{`{"retention":"P30D"}`, []string{"b"}},
				{`{"retention":{"$in":["P1M"]}}`, []string{"a"}},
				{`{"retention":{"$ne":"P1M"}}`, []string{"b", "c"}},
				{`{"retention":"P1DT-1H"}`, []string{"c"}},
				// A host and the same address with a /24 prefix differ.
				{`{"actor_ip":"192.168.0.5"}`, []string{"a"}},
				{`{"actor_ip":"2001:0DB8::0001"}`, []string{"b"}},
				{`{"actor_ip":{"$in":["192.168.0.5/24","2001:db8::1"]}}`, []string{"b", "c"}},
				{`{"network":"2001:DB8::/32"}`, []string{"b"}},
				{`{"device":"08-00-2B-01-02-03"}`, []string{"a"}},
				{`{"device":{"$ne":"08:00:2b:01:02:03"}}`, []string{"b"}},
				{`{"at":{"$gte":"10:30:00.500"}}`, []string{"a", "c"}},
				{`{"at":{"$lt":"10:30:00.5"}}`, []string{"b"}},
				{`{"allowed_ips":{"$contains":["2001:0db8::1"]}}`, []string{"a"}},
				{`{"allowed_ips":{"$some":{"$in":["10.0.0.2","192.0.2.1"]}}}`, []string{"b"}},
				{`{"allowed_ips":{"$none":{"$eq":"10.0.0.1"}}}`, []string{"b", "c"}},
				// As for a scalar interval, P30D is not P1M.
				{`{"spans":{"$contains":["P30D"]}}`, []string{"b"}},
				{`{"spans":{"$some":{"$eq":"P1M"}}}`, []string{"a"}},
				{`{"spans":{"$some":{"$in":["PT60M"]}}}`, []string{"b"}},
			} {
				assert.Equal(t, tt.want, ids(tt.filter), tt.filter)
			}

			ordered := list(`{}`, "at")
			require.Len(t, ordered, 3)
			assert.Equal(t, []string{"b", "a", "c"}, []string{ordered[0].ID, ordered[1].ID, ordered[2].ID}, "time sorts chronologically")
			wire, err := json.Marshal(ordered[1])
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"a","at":"10:30:00.5","retention":"P1M","actor_ip":"192.168.0.5","network":"10.0.0.0/8",
				"device":"08:00:2b:01:02:03","allowed_ips":["10.0.0.1","2001:db8::1"],"spans":["P1M"]}`, string(wire))
			wire, err = json.Marshal(ordered[2])
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"c","at":"23:59:59.999999","retention":"P1DT-1H","actor_ip":"192.168.0.5/24","network":"192.168.0.0/24",
				"device":null,"allowed_ips":[],"spans":[]}`, string(wire))

			patch, err := resource.ParseMergePatch([]byte(`{"at":"07:15:00.250000","retention":"PT90M","actor_ip":"::ffff:10.0.0.9",
				"network":"10.1.0.0/16","device":"0800.2b01.0205","allowed_ips":["2001:0db8:0:0:0:0:0:2"],"spans":["P1W","PT3600S"]}`))
			require.NoError(t, err)
			update, err := resource.BuildUpdate(backend.dialect, patch, parse.ByID("b"), parse.Scope{})
			require.NoError(t, err)
			rows, err := db.Query(update.Owner.SQL, update.Owner.Args...)
			require.NoError(t, err)
			require.True(t, rows.Next())
			updated := scanNetworkEvent(t, rows)
			require.NoError(t, rows.Close())
			wire, err = json.Marshal(updated)
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"b","at":"07:15:00.25","retention":"PT1H30M","actor_ip":"::ffff:10.0.0.9","network":"10.1.0.0/16",
				"device":"08:00:2b:01:02:05","allowed_ips":["2001:db8::2"],"spans":["P7D","PT1H"]}`, string(wire))
			assert.Equal(t, []string{"b"}, ids(`{"retention":"PT90M"}`), "the patch stored the canonical text")
		})
	}
}

// scanNetworkEvent scans a row read through the resource's Columns; the
// array column is read as text and decoded.
func scanNetworkEvent(t *testing.T, rows *sql.Rows) networkEvent {
	t.Helper()
	var row networkEvent
	var allowed, spans sql.NullString
	require.NoError(t, rows.Scan(&row.ID, &row.At, &row.Retention, &row.ActorIP, &row.Network, &row.Device, &allowed, &spans))
	row.AllowedIPs = []core.Inet{}
	for _, text := range arrayElements(t, allowed.String) {
		var ip core.Inet
		require.NoError(t, ip.Scan(text))
		row.AllowedIPs = append(row.AllowedIPs, ip)
	}
	row.Spans = []core.Interval{}
	for _, text := range arrayElements(t, spans.String) {
		var span core.Interval
		require.NoError(t, span.Scan(text))
		row.Spans = append(row.Spans, span)
	}
	return row
}

// arrayElements reads a JSON array (SQLite) or a PostgreSQL array literal of
// unquoted elements, as abbrev() writes addresses.
func arrayElements(t *testing.T, text string) []string {
	t.Helper()
	if text == "" || text == "{}" {
		return nil
	}
	var items []string
	if json.Unmarshal([]byte(text), &items) == nil {
		return items
	}
	require.True(t, len(text) > 1 && text[0] == '{' && text[len(text)-1] == '}', text)
	start := 1
	for i := 1; i < len(text); i++ {
		if text[i] == ',' || i == len(text)-1 {
			items = append(items, text[start:i])
			start = i + 1
		}
	}
	return items
}

// placeholders is n bind parameters in the dialect's syntax.
func placeholders(d parse.Dialect, n int) string {
	out := ""
	for i := 1; i <= n; i++ {
		if i > 1 {
			out += ", "
		}
		if d.Name() == "postgres" {
			out += fmt.Sprintf("$%d", i)
		} else {
			out += fmt.Sprintf("?%d", i)
		}
	}
	return out
}
