//go:build unit

package parsesql_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parseprovider"
	"github.com/ashtonian/pkg/parse/parsesql"
)

type note struct {
	ID   string `json:"id"   db:"id"   parse:"filter,sort"`
	Body string `json:"body" db:"body" parse:"filter,patch"`
}

// The defaults open an in-memory database that resources run on, so a
// service starts with no setting given.
func TestOpen_DefaultsRunInMemory(t *testing.T) {
	opened, err := parsesql.Open(t.Context(), providertest.Defaults[parsesql.Config](t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	assert.Equal(t, parsesql.Name, opened.Provider().Name())
	require.NoError(t, opened.Ping(t.Context()))

	db := opened.DB()
	_, err = db.ExecContext(t.Context(), `CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO notes VALUES ('n1', 'first')`)
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
	assert.Equal(t, "edited", page.Items[0].Body, "the one connection keeps the in-memory database")
}

// The settle timeout's default tag is parse's default, so configuration
// and New agree.
func TestConfig_DefaultsMatchTheProvider(t *testing.T) {
	assert.Equal(t, parse.DefaultSettleTimeout, providertest.Defaults[parsesql.Config](t).SettleTimeout)
}

// parseprovider opens the SQLite provider by its name, and refuses another
// name, naming the one the binary links.
func TestChoice_OpensByName(t *testing.T) {
	cfg := providertest.Defaults[parsesql.Config](t)
	opened, err := parseprovider.Open(t.Context(), parsesql.Name, parsesql.Choice(cfg))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	db, ok := opened.(*parsesql.Database)
	require.True(t, ok, "the opened database is the provider's own, for its handle")
	require.NoError(t, db.DB().PingContext(t.Context()))

	_, err = parseprovider.Open(t.Context(), "postgres", parsesql.Choice(cfg))
	require.EqualError(t, err, `parseprovider: provider: "postgres": this binary links sqlite`)
}

// Open begins every transaction IMMEDIATE, so a second writer waits for the
// first rather than committing between an update's read and its write.
func TestOpen_BeginsTransactionsImmediate(t *testing.T) {
	cfg := providertest.Defaults[parsesql.Config](t)
	cfg.DSN = filepath.Join(t.TempDir(), "app.db")
	cfg.MaxOpenConns = 2
	cfg.BusyTimeout = 0
	opened, err := parsesql.Open(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	first, err := opened.DB().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Rollback() })
	_, err = opened.DB().BeginTx(t.Context(), nil)
	require.ErrorContains(t, err, "database is locked", "the first transaction took the write lock as it began")
}

func TestOpen_RefusesNamingTheSetting(t *testing.T) {
	const secret = "s3cret"
	for _, tc := range []struct {
		name string
		edit func(*parsesql.Config)
		want string
	}{
		{"no DSN", func(c *parsesql.Config) { c.DSN = "" }, "parsesql: dsn: empty"},
		{"no settle timeout", func(c *parsesql.Config) { c.SettleTimeout = 0 }, "parsesql: settle_timeout: 0s: must be positive"},
		{"a PostgreSQL DSN", func(c *parsesql.Config) { c.DSN = "postgres://app:" + secret + "@db/app" }, "parsesql: dsn: a postgres connection string"},
		{"a bare file name", func(c *parsesql.Config) { c.DSN = "app.db" }, "parsesql: dsn: parse: unsupported DSN"},
		{"a sqlite:// URL", func(c *parsesql.Config) { c.DSN = "sqlite:///data/app.db" }, "parsesql: dsn: a sqlite:// URL"},
		{"an extended-length path", func(c *parsesql.Config) { c.DSN = `\\?\C:\data\app.db` }, "parsesql: dsn: an extended-length path"},
		{"the DSN's write mode", func(c *parsesql.Config) { c.DSN = "./app.db?_txlock=deferred" }, "parsesql: dsn: _txlock: Open sets it"},
		{"the DSN's busy timeout", func(c *parsesql.Config) { c.DSN = "./app.db?_busy_timeout=1" }, "parsesql: dsn: _busy_timeout: Open sets it (the busy_timeout setting)"},
		{"the DSN's busy timeout alias", func(c *parsesql.Config) { c.DSN = "./app.db?_timeout=1" }, "parsesql: dsn: _timeout: Open sets it"},
		{"a malformed parameter", func(c *parsesql.Config) { c.DSN = "./app.db?x=%zz" + secret }, "parsesql: dsn: its parameters are no URL query"},
		{"no connection", func(c *parsesql.Config) { c.MaxOpenConns = 0 }, "parsesql: max_open_conns: 0: must be at least 1"},
		{"a negative busy timeout", func(c *parsesql.Config) { c.BusyTimeout = -time.Second }, "parsesql: busy_timeout: -1s: must not be negative"},
		{"connections to a private in-memory database", func(c *parsesql.Config) { c.MaxOpenConns = 2 }, "parsesql: max_open_conns: 2: the DSN names an in-memory database without cache=shared"},
		{"connections to a private in-memory URI", func(c *parsesql.Config) {
			c.DSN = "file:app?mode=memory"
			c.MaxOpenConns = 2
		}, "an in-memory database without cache=shared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := providertest.Defaults[parsesql.Config](t)
			tc.edit(&cfg)
			opened, err := parsesql.Open(t.Context(), cfg)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, opened)
			assert.NotContains(t, err.Error(), secret, "an error never quotes the DSN")
		})
	}
}

// A file: URI to a shared in-memory database is one database on every
// connection, so it opens with several.
func TestOpen_SharedInMemoryOnSeveralConnections(t *testing.T) {
	cfg := providertest.Defaults[parsesql.Config](t)
	cfg.DSN = "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	cfg.MaxOpenConns = 2
	opened, err := parsesql.Open(t.Context(), cfg)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
}
