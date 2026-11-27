//go:build integration

package parsepgx_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsepgx/internal/pgtest"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

type note struct {
	ID   string `json:"id"   db:"id"   parse:"filter,sort"`
	Body string `json:"body" db:"body" parse:"filter,patch"`
}

// serverDSN is the test server's DSN on a fresh schema, which the DSN names
// as its search_path startup parameter.
func serverDSN(t *testing.T) string {
	t.Helper()
	config := pgtest.PostgresPoolConfig(t)
	dsn := config.ConnString()
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + url.QueryEscape(config.ConnConfig.RuntimeParams["search_path"])
}

// Open opens a pool with the configured limits, which resources run on, and
// parseprovider opens it by the provider's name.
func TestOpen_PoolRunsResources(t *testing.T) {
	cfg := configOn(t, serverDSN(t))
	cfg.MaxConns = 3
	cfg.MaxConnIdleTime = time.Minute
	chosen, err := parseprovider.Open(pgtest.Context(t), parsepgx.Name, parsepgx.Choice(cfg))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, chosen.Close()) })
	opened, ok := chosen.(*parsepgx.Database)
	require.True(t, ok, "the opened database is the provider's own, for its pool")
	assert.Equal(t, parsepgx.Name, opened.Provider().Name())
	pool := opened.Pool()
	assert.Equal(t, int32(3), pool.Config().MaxConns)
	assert.Equal(t, time.Minute, pool.Config().MaxConnIdleTime)
	assert.Equal(t, cfg.ConnectTimeout, pool.Config().ConnConfig.ConnectTimeout)
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
