//go:build integration

package parsepgx_test

import (
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

type endpoint struct {
	ID      uuid.UUID `json:"id"      db:"id"      parse:"filter,sort"`
	Addr    string    `json:"addr"    db:"addr"    parse:"patch"`
	Net     *string   `json:"net"     db:"net"`
	Every   string    `json:"every"   db:"every"`
	Allowed []string  `json:"allowed" db:"allowed" parse:"patch"`
	Nets    []string  `json:"nets"    db:"nets"`
	Spans   []string  `json:"spans"   db:"spans"`
}

var endpointTypes = []parse.ResourceOption{
	parse.ColumnType("addr", core.SQLInet),
	parse.ColumnType("net", core.SQLCIDR),
	parse.ColumnType("every", core.SQLInterval),
	parse.ColumnType("allowed", core.SQLInetArray),
	parse.ColumnType("nets", core.SQLCIDRArray),
	parse.ColumnType("spans", core.SQLIntervalArray),
}

// Network and interval columns read into string fields on PostgreSQL, whose
// binary encoding of them pgx scans into no string: lists and updates read
// them as text, in their display form.
func TestColumnType_ReadsNetworkAndIntervalColumns(t *testing.T) {
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	ctx := testContext(t)
	setup := c.open(t, pgx.QueryExecModeCacheStatement)
	full, empty := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []string{
		`CREATE TABLE endpoints (id uuid PRIMARY KEY, addr inet NOT NULL, net cidr, every interval NOT NULL,
			allowed inet[], nets cidr[], spans interval[])`,
		`INSERT INTO endpoints VALUES ('` + full.String() + `', '10.0.0.1', '10.0.0.0/8', '1 day 2 hours',
			'{10.0.0.1,10.1.0.0/16,::1}', '{192.168.0.0/16}', '{"1 hour","3 days"}')`,
		`INSERT INTO endpoints VALUES ('` + empty.String() + `', '10.1.0.0/16', NULL, '0', NULL, '{}', NULL)`,
	} {
		_, err := setup.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	want := []endpoint{
		{ID: full, Addr: "10.0.0.1", Net: new("10.0.0.0/8"), Every: "1 day 02:00:00",
			Allowed: []string{"10.0.0.1", "10.1.0.0/16", "::1"}, Nets: []string{"192.168.0.0/16"}, Spans: []string{"01:00:00", "3 days"}},
		{ID: empty, Addr: "10.1.0.0/16", Every: "00:00:00", Nets: []string{}},
	}
	list := func(t *testing.T, r *parse.Resource[endpoint]) *parse.ListPlan {
		t.Helper()
		req, err := r.ParseList(url.Values{"sort": {"id"}})
		require.NoError(t, err)
		plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
		require.NoError(t, err)
		return plan
	}

	plain := parse.MustResource[endpoint]("endpoints")
	_, err := parsepgx.QueryPage(ctx, setup, plain, list(t, plain))
	require.ErrorContains(t, err, "cannot scan inet", "the defect the declarations fix")

	typed := parse.MustResource[endpoint]("endpoints", endpointTypes...)
	for name, mode := range map[string]pgx.QueryExecMode{
		"extended": pgx.QueryExecModeCacheStatement,
		"simple":   pgx.QueryExecModeSimpleProtocol,
	} {
		t.Run(name, func(t *testing.T) {
			pool := c.open(t, mode)
			page, err := parsepgx.QueryPage(ctx, pool, typed, list(t, typed))
			require.NoError(t, err)
			assert.Equal(t, want, page.Items)
		})
	}

	p, err := typed.ParseMergePatch([]byte(`{"addr":"192.168.1.1","allowed":["::2","10.2.0.0/24"]}`))
	require.NoError(t, err)
	plan, err := typed.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(empty), parse.Scope{})
	require.NoError(t, err)
	got, err := parsepgx.Update(ctx, setup, typed, plan)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "192.168.1.1", got[0].Addr, "RETURNING reads the column as the list does")
	assert.Equal(t, []string{"::2", "10.2.0.0/24"}, got[0].Allowed)
}
