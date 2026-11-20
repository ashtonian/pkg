package parse_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// networkEvent has a column of each value type with one canonical text.
type networkEvent struct {
	ID         string          `json:"id" db:"id" parse:"filter,sort"`
	At         *core.LocalTime `json:"at" db:"at" parse:"filter,patch,sort,facet"`
	Retention  *core.Interval  `json:"retention" db:"retention" parse:"filter,patch"`
	ActorIP    *core.Inet      `json:"actor_ip" db:"actor_ip" parse:"filter,patch"`
	Network    *core.CIDR      `json:"network" db:"network" parse:"filter,patch"`
	Device     *core.MACAddr   `json:"device" db:"device" parse:"filter,patch,facet"`
	AllowedIPs []core.Inet     `json:"allowed_ips" db:"allowed_ips" parse:"filter,patch"`
	Spans      []core.Interval `json:"spans" db:"spans" parse:"filter,patch"`
}

func TestSQLTextValueFilters(t *testing.T) {
	resource := parse.MustResource[networkEvent]("network_events")
	for _, tt := range []struct {
		filter, canonical string
	}{
		{`{"at":{"$gte":"10:30:00.500"}}`, "10:30:00.5"},
		{`{"retention":"PT90M"}`, "PT1H30M"},
		{`{"actor_ip":"2001:0DB8::0001"}`, "2001:db8::1"},
		{`{"actor_ip":{"$in":["192.168.0.5/32"]}}`, "192.168.0.5"},
		{`{"network":"2001:DB8::/32"}`, "2001:db8::/32"},
		{`{"device":"08-00-2B-01-02-03"}`, "08:00:2b:01:02:03"},
		{`{"allowed_ips":{"$contains":["10.0.0.1/32"]}}`, "10.0.0.1"},
		{`{"spans":{"$contains":["PT90M"]}}`, "PT1H30M"},
	} {
		t.Run(tt.filter, func(t *testing.T) {
			query, err := resource.ParseList(url.Values{"filter": {tt.filter}})
			require.NoError(t, err)
			for _, dialect := range []parse.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
				plan, err := resource.BuildList(dialect, query, parse.Scope{})
				require.NoError(t, err)
				assert.Contains(t, boundText(plan.Page.Args), tt.canonical, "%s binds the canonical text", dialect.Name())
			}
		})
	}

	for _, tt := range []struct {
		name, filter, code string
	}{
		{"time with an offset", `{"at":"10:30:00+02:00"}`, "E2073"},
		{"24:00:00", `{"at":"24:00:00"}`, "E2073"},
		{"interval in postgres style", `{"retention":"1 day"}`, "E2073"},
		{"inet with a zone", `{"actor_ip":"fe80::1%eth0"}`, "E2073"},
		{"cidr with host bits", `{"network":"10.0.0.1/8"}`, "E2073"},
		{"EUI-64", `{"device":"08:00:2b:ff:fe:01:02:03"}`, "E2073"},
		{"inet range", `{"actor_ip":{"$gt":"10.0.0.1"}}`, ""},
		{"interval range", `{"retention":{"$lt":"P1D"}}`, ""},
		{"inet pattern", `{"actor_ip":{"$like":"10.%"}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resource.ParseList(url.Values{"filter": {tt.filter}})
			require.Error(t, err)
			if tt.code != "" {
				var parseErr *core.ParseError
				require.True(t, errors.As(err, &parseErr), "%v", err)
				assert.Equal(t, tt.code, parseErr.Code)
			}
		})
	}

	types := map[string]string{}
	for _, f := range resource.FilterSchema().Fields {
		types[f.ExternalName] = string(f.DataType)
	}
	assert.Equal(t, map[string]string{
		"id": "string", "at": "time", "retention": "interval", "actor_ip": "inet", "network": "cidr", "device": "macaddr",
		"allowed_ips": "array", "spans": "array",
	}, types)
}

// TestSQLTextValueIntervalEquality checks that PostgreSQL compares an
// interval's canonical text, field by field: its own equality would match
// P1M against P30D, which SQLite's text comparison does not.
func TestSQLTextValueIntervalEquality(t *testing.T) {
	resource := parse.MustResource[networkEvent]("network_events")
	query, err := resource.ParseList(url.Values{"filter": {`{"retention":"P30D"}`}})
	require.NoError(t, err)
	plan, err := resource.BuildList(filter.PostgresDialect{}, query, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "isfinite(network_events.retention)")
	columns, err := resource.Columns(filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(columns, ","), "isfinite(network_events.retention)",
		"an Interval column reads as ISO 8601 text under any IntervalStyle")
	declared, ok := resource.ColumnSQLType("retention")
	require.True(t, ok)
	assert.Equal(t, core.SQLInterval, declared)
}

func TestSQLTextValuePatch(t *testing.T) {
	resource := parse.MustResource[networkEvent]("network_events")
	patch, err := resource.ParseMergePatch([]byte(`{"at":"10:30:00.500","retention":"PT90M","actor_ip":"192.168.0.5/32",
		"network":"10.0.0.0/8","device":"08-00-2B-01-02-03","allowed_ips":["10.0.0.1","2001:0db8::1"],"spans":["P14M"]}`))
	require.NoError(t, err)
	target, err := resource.ApplyValidatedPatch(patch, networkEvent{ID: "one"})
	require.NoError(t, err)
	wire, err := json.Marshal(target)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"one","at":"10:30:00.5","retention":"PT1H30M","actor_ip":"192.168.0.5",
		"network":"10.0.0.0/8","device":"08:00:2b:01:02:03","allowed_ips":["10.0.0.1","2001:db8::1"],"spans":["P1Y2M"]}`, string(wire))
	for _, dialect := range []parse.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		plan, err := resource.BuildUpdate(dialect, patch, parse.ByID("one"), parse.Scope{})
		require.NoError(t, err)
		args := boundText(plan.Owner.Args)
		for _, canonical := range []string{"10:30:00.5", "PT1H30M", "192.168.0.5", "10.0.0.0/8", "08:00:2b:01:02:03", "P1Y2M"} {
			assert.Contains(t, args, canonical, "%s writes the canonical text", dialect.Name())
		}
	}

	for _, body := range []string{`{"at":"10:30"}`, `{"retention":"P1.5D"}`, `{"actor_ip":"not-an-ip"}`,
		`{"network":"10.0.0.0"}`, `{"device":"08:00:2b"}`, `{"allowed_ips":["10.0.0.256"]}`} {
		_, err := resource.ParseMergePatch([]byte(body))
		assert.Error(t, err, body)
	}
}

func TestSQLTextValueDeclarations(t *testing.T) {
	_, err := parse.NewResource[networkEvent]("network_events",
		parse.ColumnType("actor_ip", core.SQLInet), parse.ColumnType("network", core.SQLCIDR),
		parse.ColumnType("retention", core.SQLInterval), parse.ColumnType("allowed_ips", core.SQLInetArray))
	require.NoError(t, err)
	for _, tt := range []struct {
		column  string
		storage core.SQLType
	}{
		{"actor_ip", core.SQLCIDR},
		{"network", core.SQLInet},
		{"at", core.SQLInterval},
		{"device", core.SQLTextArray},
		{"allowed_ips", core.SQLCIDRArray},
		{"retention", core.SQLDate},
	} {
		_, err := parse.NewResource[networkEvent]("network_events", parse.ColumnType(tt.column, tt.storage))
		assert.Error(t, err, "%s declared %s", tt.column, tt.storage)
	}

	_, err = parse.NewResource[struct {
		ID string     `db:"id"`
		IP *core.Inet `db:"ip" parse:"sort"`
	}]("unsortable")
	assert.ErrorContains(t, err, "inet fields are not sortable")
	_, err = parse.NewResource[struct {
		ID  string        `db:"id"`
		TTL core.Interval `db:"ttl" parse:"filter,facet"`
	}]("unfacetable")
	assert.ErrorContains(t, err, "interval values have no facet key")
	_, err = parse.NewResource[struct {
		ID  string      `db:"id"`
		IPs []core.CIDR `db:"ips" parse:"filter,facet"`
	}]("unfacetable")
	assert.ErrorContains(t, err, "cidr values have no facet key")
}

// boundText is the text of each bound argument, with the elements of an
// array argument and of a JSON array text, which SQLite binds sets as.
func boundText(args []any) []string {
	var out []string
	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			out = append(out, v)
			var items []string
			if json.Unmarshal([]byte(v), &items) == nil {
				out = append(out, items...)
			}
		case []string:
			out = append(out, v...)
		}
	}
	return out
}
