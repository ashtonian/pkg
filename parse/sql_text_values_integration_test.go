//go:build integration

package parse_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func networkEventTable() sqlTable {
	table := sqlTable{name: "network_events", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[core.LocalTime]("at"), column[core.Interval]("retention"), column[core.Inet]("actor_ip"),
		column[core.CIDR]("network"), column[core.MACAddr]("device"), column[[]core.Inet]("allowed_ips"), column[[]core.Interval]("spans"),
	}}
	return table
}

// TestSQLTextValuesSQL writes each value type on both backends, reads back
// its canonical text, and checks that every filter the type offers selects
// the same rows on both.
func TestSQLTextValuesSQL(t *testing.T) {
	// PostgreSQL's connections write intervals in another style than its
	// default: reads must not depend on it.
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		table := networkEventTable()
		b.create(t, db, table)
		resource := parse.MustResource[networkEvent]("network_events")
		insert := func(id string, at core.LocalTime, retention core.Interval, actorIP, network string, device any, spans []string, allowed ...string) {
			t.Helper()
			ip, err := core.ParseInet(actorIP)
			require.NoError(t, err)
			cidr, err := core.ParseCIDR(network)
			require.NoError(t, err)
			b.insert(t, db, table, []any{id, at, retention, ip, cidr, device, allowed, spans})
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
			plan, err := resource.BuildList(b.dialect, query, parse.Scope{})
			require.NoError(t, err)
			return resourceRows(t, db, resource, b.dialect, plan.Page)
		}
		ids := func(filterJSON string) []string {
			t.Helper()
			return itemIDs(list(filterJSON, "id"), func(e networkEvent) string { return e.ID })
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
			"device":null,"allowed_ips":null,"spans":null}`, string(wire))

		patch, err := resource.ParseMergePatch([]byte(`{"at":"07:15:00.250000","retention":"PT90M","actor_ip":"::ffff:10.0.0.9",
			"network":"10.1.0.0/16","device":"0800.2b01.0205","allowed_ips":["2001:0db8:0:0:0:0:0:2"],"spans":["P1W","PT3600S"]}`))
		require.NoError(t, err)
		update, err := resource.BuildUpdate(b.dialect, patch, parse.ByID("b"), parse.Scope{})
		require.NoError(t, err)
		updated := resourceRows(t, db, resource, b.dialect, update.Owner)
		require.Len(t, updated, 1)
		wire, err = json.Marshal(updated[0])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"b","at":"07:15:00.25","retention":"PT1H30M","actor_ip":"::ffff:10.0.0.9","network":"10.1.0.0/16",
			"device":"08:00:2b:01:02:05","allowed_ips":["2001:db8::2"],"spans":["P7D","PT1H"]}`, string(wire))
		assert.Equal(t, []string{"b"}, ids(`{"retention":"PT90M"}`), "the patch stored the canonical text")
	}, sessionParams(map[string]string{"IntervalStyle": "sql_standard"}))
}
