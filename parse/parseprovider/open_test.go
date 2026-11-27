//go:build unit || integration

package parseprovider_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parseprovider"
	"github.com/ashtonian/pkg/parse/parsesql"
)

type note struct {
	ID   string `json:"id"   db:"id"   parse:"filter,sort"`
	Body string `json:"body" db:"body" parse:"filter,patch"`
}

// The defaults open an in-memory SQLite database that resources run on,
// so a service starts with no setting given.
func TestOpen_DefaultsRunOnInMemorySQLite(t *testing.T) {
	opened, err := parseprovider.Open(t.Context(), defaults(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	assert.Equal(t, parsesql.Name, opened.Provider().Name())
	_, isPostgres := opened.Postgres()
	assert.False(t, isPostgres)
	db, ok := opened.SQLite()
	require.True(t, ok)
	require.NoError(t, opened.Ping(t.Context()))

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

// The tags state what the providers take, so configuration validation and
// Open agree.
func TestConfig_TagsMatchTheProviders(t *testing.T) {
	cfg := defaults(t)
	assert.Equal(t, parse.DefaultSettleTimeout, cfg.SettleTimeout)
	field, ok := reflect.TypeFor[parseprovider.Config]().FieldByName("Provider")
	require.True(t, ok)
	assert.Equal(t, "oneof="+parsesql.Name+" "+parsepgx.Name, field.Tag.Get("validate"))
}

// Open begins every SQLite transaction IMMEDIATE, so a second writer waits
// for the first rather than committing between an update's read and its
// write.
func TestOpen_BeginsSQLiteTransactionsImmediate(t *testing.T) {
	cfg := defaults(t)
	cfg.DSN = filepath.Join(t.TempDir(), "app.db")
	cfg.SQLite.MaxOpenConns = 2
	cfg.SQLite.BusyTimeout = 0
	opened, err := parseprovider.Open(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	db, _ := opened.SQLite()

	first, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Rollback() })
	_, err = db.BeginTx(t.Context(), nil)
	require.ErrorContains(t, err, "database is locked", "the first transaction took the write lock as it began")
}

func TestOpen_RefusesNamingTheSetting(t *testing.T) {
	const secret = "s3cret"
	for _, tc := range []struct {
		name string
		edit func(*parseprovider.Config)
		want string
	}{
		{"no DSN", func(c *parseprovider.Config) { c.DSN = "" }, "parseprovider: dsn: empty"},
		{"no settle timeout", func(c *parseprovider.Config) { c.SettleTimeout = 0 }, "parseprovider: settle_timeout: 0s: must be positive"},
		{"another provider", func(c *parseprovider.Config) { c.Provider = "mysql" }, `parseprovider: provider: "mysql": want sqlite or postgres`},
		{"a PostgreSQL DSN for SQLite", func(c *parseprovider.Config) { c.DSN = "postgres://app:" + secret + "@db/app" }, "parseprovider: dsn: a postgres connection string for provider sqlite"},
		{"a bare file name", func(c *parseprovider.Config) { c.DSN = "app.db" }, "parseprovider: dsn: parse: unsupported DSN"},
		{"a sqlite:// URL", func(c *parseprovider.Config) { c.DSN = "sqlite:///data/app.db" }, "parseprovider: dsn: a sqlite:// URL"},
		{"an extended-length path", func(c *parseprovider.Config) { c.DSN = `\\?\C:\data\app.db` }, "parseprovider: dsn: an extended-length path"},
		{"the DSN's write mode", func(c *parseprovider.Config) { c.DSN = "./app.db?_txlock=deferred" }, "parseprovider: dsn: _txlock: Open sets it"},
		{"the DSN's busy timeout", func(c *parseprovider.Config) { c.DSN = "./app.db?_busy_timeout=1" }, "parseprovider: dsn: _busy_timeout: Open sets it (the sqlite.busy_timeout setting)"},
		{"the DSN's busy timeout alias", func(c *parseprovider.Config) { c.DSN = "./app.db?_timeout=1" }, "parseprovider: dsn: _timeout: Open sets it"},
		{"a malformed parameter", func(c *parseprovider.Config) { c.DSN = "./app.db?x=%zz" + secret }, "parseprovider: dsn: its parameters are no URL query"},
		{"no SQLite connection", func(c *parseprovider.Config) { c.SQLite.MaxOpenConns = 0 }, "parseprovider: sqlite.max_open_conns: 0: must be at least 1"},
		{"a negative busy timeout", func(c *parseprovider.Config) { c.SQLite.BusyTimeout = -time.Second }, "parseprovider: sqlite.busy_timeout: -1s: must not be negative"},
		{"connections to a private in-memory database", func(c *parseprovider.Config) { c.SQLite.MaxOpenConns = 2 }, "parseprovider: sqlite.max_open_conns: 2: the DSN names an in-memory database without cache=shared"},
		{"connections to a private in-memory URI", func(c *parseprovider.Config) {
			c.DSN = "file:app?mode=memory"
			c.SQLite.MaxOpenConns = 2
		}, "an in-memory database without cache=shared"},
		{"a SQLite DSN for PostgreSQL", postgres("./app.db"), "parseprovider: dsn: a sqlite connection string for provider postgres"},
		{"CockroachDB", postgres("cockroach://root:" + secret + "@db:26257/app"), `parseprovider: dsn: parse: unsupported DSN: scheme "cockroach": CockroachDB is not supported`},
		{"no PostgreSQL connection string", postgres("host=db port=notaport password=" + secret), "parseprovider: dsn: not a PostgreSQL connection string pgx reads"},
		{"the DSN's pool size", postgres("postgres://app:" + secret + "@db/app?pool_max_conns=50"), "parseprovider: dsn: pool_max_conns: the pool is set by the postgres.* settings"},
		{"another collation", func(c *parseprovider.Config) {
			postgres("postgres://db/app")(c)
			c.Postgres.Collation = "en_US"
		}, `parseprovider: postgres.collation: "en_US": want pg_c_utf8 or C`},
		{"no PostgreSQL connection", func(c *parseprovider.Config) {
			postgres("postgres://db/app")(c)
			c.Postgres.MaxConns = 0
		}, "parseprovider: postgres.max_conns: 0: must be at least 1"},
		{"more idle connections than the pool holds", func(c *parseprovider.Config) {
			postgres("postgres://db/app")(c)
			c.Postgres.MinConns = 11
		}, "parseprovider: postgres.min_conns: 11: must be from 0 to postgres.max_conns (10)"},
		{"connections that never expire", func(c *parseprovider.Config) {
			postgres("postgres://db/app")(c)
			c.Postgres.MaxConnLifetime = 0
		}, "parseprovider: postgres.max_conn_lifetime: 0s: must be positive"},
		{"no connect timeout", func(c *parseprovider.Config) {
			postgres("postgres://db/app")(c)
			c.Postgres.ConnectTimeout = 0
		}, "parseprovider: postgres.connect_timeout: 0s: must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults(t)
			tc.edit(&cfg)
			opened, err := parseprovider.Open(t.Context(), cfg)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, opened)
			assert.NotContains(t, err.Error(), secret, "an error never quotes the DSN")
		})
	}
}

// postgres selects provider postgres on dsn.
func postgres(dsn string) func(*parseprovider.Config) {
	return func(c *parseprovider.Config) {
		c.Provider = parsepgx.Name
		c.DSN = dsn
	}
}

// A file: URI to a shared in-memory database is one database on every
// connection, so it opens with several.
func TestOpen_SharedInMemoryOnSeveralConnections(t *testing.T) {
	cfg := defaults(t)
	cfg.DSN = "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	cfg.SQLite.MaxOpenConns = 2
	opened, err := parseprovider.Open(t.Context(), cfg)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
}
