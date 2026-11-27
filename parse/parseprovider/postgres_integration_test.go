//go:build integration

package parseprovider_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// postgresDSN is the test server's DSN on a fresh schema, which the DSN
// names as its search_path startup parameter.
func postgresDSN(t *testing.T) string {
	t.Helper()
	config := dbtest.PostgresPoolConfig(t)
	dsn := config.ConnString()
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + url.QueryEscape(config.ConnConfig.RuntimeParams["search_path"])
}

// Provider postgres opens a pool with the configured limits, which
// resources run on.
func TestOpen_Postgres(t *testing.T) {
	cfg := defaults(t)
	cfg.Provider = parsepgx.Name
	cfg.DSN = postgresDSN(t)
	cfg.Postgres.MaxConns = 3
	cfg.Postgres.MaxConnIdleTime = time.Minute
	opened, err := parseprovider.Open(dbtest.Context(t), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	assert.Equal(t, parsepgx.Name, opened.Provider().Name())
	_, isSQLite := opened.SQLite()
	assert.False(t, isSQLite)
	pool, ok := opened.Postgres()
	require.True(t, ok)
	assert.Equal(t, int32(3), pool.Config().MaxConns)
	assert.Equal(t, time.Minute, pool.Config().MaxConnIdleTime)
	assert.Equal(t, cfg.Postgres.ConnectTimeout, pool.Config().ConnConfig.ConnectTimeout)
	require.NoError(t, opened.Ping(t.Context()))

	_, err = pool.Exec(t.Context(), `CREATE TABLE notes (id text PRIMARY KEY, body text NOT NULL)`)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO notes VALUES ('n1', 'first')`)
	require.NoError(t, err)
	notes := parse.MustResource[note](opened.Provider(), "notes")
	patch, err := notes.ParseMergePatch([]byte(`{"body":"edited"}`))
	require.NoError(t, err)
	res, err := notes.Update(t.Context(), patch, parse.ByID("n1"), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	page, err := notes.List(t.Context(), nil, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "edited", page.Items[0].Body)
}
