//go:build unit

package parsepgx_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// New refuses a nil pool, a settle timeout that is not positive and a
// collation filter does not define before it sends any statement, so none
// needs a server.
func TestNew_RefusesBeforeAnyStatement(t *testing.T) {
	_, err := parsepgx.New(t.Context(), nil)
	require.EqualError(t, err, "parsepgx: nil pool")

	// The pool connects lazily: nothing here dials the address.
	cfg, err := pgxpool.ParseConfig("postgres://parse@192.0.2.1:5432/parse")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	for _, d := range []string{"0s", "-1s"} {
		timeout, err := time.ParseDuration(d)
		require.NoError(t, err)
		_, err = parsepgx.New(t.Context(), pool, parsepgx.SettleTimeout(timeout))
		require.EqualError(t, err, "parsepgx: settle timeout "+d+": must be positive")
	}
	_, err = parsepgx.New(t.Context(), pool, parsepgx.Collation(filter.PostgresCollation(9)))
	require.EqualError(t, err, "parsepgx: collation 9: no such collation")
}
