//go:build integration

package parsepgx_test

import (
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse/parsepgx/internal/pgtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
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
// them as text, an address in its display form and an interval as ISO 8601.
func TestColumnType_ReadsNetworkAndIntervalColumns(t *testing.T) {
	c := &catalog{config: pgtest.PostgresPoolConfig(t)}
	ctx := testContext(t)
	setup := c.open(t, ctx, pgx.QueryExecModeCacheStatement)
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
		{ID: full, Addr: "10.0.0.1", Net: new("10.0.0.0/8"), Every: "P1DT2H",
			Allowed: []string{"10.0.0.1", "10.1.0.0/16", "::1"}, Nets: []string{"192.168.0.0/16"}, Spans: []string{"PT1H", "P3D"}},
		{ID: empty, Addr: "10.1.0.0/16", Every: "PT0S", Nets: []string{}},
	}
	list := func(t *testing.T, r *parse.Resource[endpoint]) (*filter.Page[endpoint], error) {
		t.Helper()
		req, err := r.ParseList(url.Values{"sort": {"id"}})
		require.NoError(t, err)
		return r.List(ctx, req, parse.Scope{})
	}

	onSetup := provider(t, ctx, setup)
	_, err := list(t, parse.MustResource[endpoint](onSetup, "endpoints"))
	require.ErrorContains(t, err, "cannot scan inet", "the defect the declarations fix")

	for name, mode := range map[string]pgx.QueryExecMode{
		"extended": pgx.QueryExecModeCacheStatement,
		"simple":   pgx.QueryExecModeSimpleProtocol,
	} {
		t.Run(name, func(t *testing.T) {
			typed := parse.MustResource[endpoint](provider(t, ctx, c.open(t, ctx, mode)), "endpoints", endpointTypes...)
			page, err := list(t, typed)
			require.NoError(t, err)
			assert.Equal(t, want, page.Items)
		})
	}

	typed := parse.MustResource[endpoint](onSetup, "endpoints", endpointTypes...)
	p, err := typed.ParseMergePatch([]byte(`{"addr":"192.168.1.1","allowed":["::2","10.2.0.0/24"]}`))
	require.NoError(t, err)
	got, err := typed.Update(ctx, p, parse.ByID(empty), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, got.Changed, 1)
	assert.Equal(t, "192.168.1.1", got.Changed[0].Addr, "RETURNING reads the column as the list does")
	assert.Equal(t, []string{"::2", "10.2.0.0/24"}, got.Changed[0].Allowed)
}
