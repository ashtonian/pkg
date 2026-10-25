//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type session struct {
	ID      string   `json:"id"      db:"id"      parse:"filter,sort"`
	Addr    string   `json:"addr"    db:"addr"    parse:"patch"`
	Net     *string  `json:"net"     db:"net"`
	Every   string   `json:"every"   db:"every"`
	Allowed []string `json:"allowed" db:"allowed"`
	Nets    []string `json:"nets"    db:"nets"`
	Spans   []string `json:"spans"   db:"spans"`
	Count   int      `json:"count"   db:"count"`
}

func sessionResource(t *testing.T, opts ...parse.ResourceOption) (*parse.Resource[session], error) {
	t.Helper()
	return parse.NewResource[session]("sessions", append([]parse.ResourceOption{
		parse.ColumnType("addr", core.SQLInet),
		parse.ColumnType("net", core.SQLCIDR),
		parse.ColumnType("every", core.SQLInterval),
		parse.ColumnType("allowed", core.SQLInetArray),
		parse.ColumnType("nets", core.SQLCIDRArray),
		parse.ColumnType("spans", core.SQLIntervalArray),
	}, opts...)...)
}

// A declared column is read as the dialect reads its type, under its own
// name; the others, and every column on SQLite, are read as they are.
func TestColumnType_Projection(t *testing.T) {
	r, err := sessionResource(t)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sessions.id",
		"abbrev(sessions.addr) AS addr",
		"sessions.net::text AS net",
		"sessions.every::text AS every",
		"CASE WHEN sessions.allowed IS NULL THEN NULL ELSE ARRAY(SELECT abbrev(a) FROM unnest(sessions.allowed) WITH ORDINALITY AS u(a, n) ORDER BY n) END AS allowed",
		"sessions.nets::text[] AS nets",
		"sessions.spans::text[] AS spans",
		"sessions.count",
	}, r.Columns(filter.PostgresDialect{}))
	assert.Equal(t, []string{
		"sessions.id", "sessions.addr", "sessions.net", "sessions.every",
		"sessions.allowed", "sessions.nets", "sessions.spans", "sessions.count",
	}, r.Columns(filter.SQLiteDialect{}))

	only, err := sessionResource(t, parse.ReadColumns("id", "addr"))
	require.NoError(t, err)
	assert.Equal(t, []string{"sessions.id", "abbrev(sessions.addr) AS addr"}, only.Columns(filter.PostgresDialect{}))
}

// A list's page and an update's RETURNING read the same projection.
func TestColumnType_PlansReadTheProjection(t *testing.T) {
	r, err := sessionResource(t)
	require.NoError(t, err)
	d := filter.PostgresDialect{}
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	list, err := r.BuildList(d, req, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, list.Page.SQL, "SELECT sessions.id, abbrev(sessions.addr) AS addr, ")

	p, err := r.ParseMergePatch([]byte(`{"addr":"10.0.0.1"}`))
	require.NoError(t, err)
	upd, err := r.BuildUpdate(d, p, parse.ByID("s1"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, upd.Owner.SQL, " RETURNING sessions.id, abbrev(sessions.addr) AS addr, ")
}

func TestColumnType_RefusesWhatItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  parse.ResourceOption
		want string
	}{
		"not a column":      {parse.ColumnType("ip", core.SQLInet), `column type for "ip", which is not a column`},
		"declared twice":    {parse.ColumnType("addr", core.SQLCIDR), `column "addr" has its type declared twice`},
		"unknown type":      {parse.ColumnType("count", "money"), `declares type "money", which has no read rule`},
		"scalar into int":   {parse.ColumnType("count", core.SQLInet), `column "count" is inet, read into a string, not int`},
		"array into string": {parse.ColumnType("id", core.SQLInetArray), `column "id" is inet[], read into a slice of strings, not string`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sessionResource(t, tc.opt)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
