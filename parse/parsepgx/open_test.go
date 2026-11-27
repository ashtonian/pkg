//go:build unit

package parsepgx_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// The settle timeout's default tag is parse's default, so configuration
// and New agree.
func TestConfig_DefaultsMatchTheProvider(t *testing.T) {
	assert.Equal(t, parse.DefaultSettleTimeout, providertest.Defaults[parsepgx.Config](t).SettleTimeout)
}

// Open refuses every setting it checks before it connects, naming it.
func TestOpen_RefusesNamingTheSetting(t *testing.T) {
	const secret = "s3cret"
	for _, tc := range []struct {
		name string
		edit func(*parsepgx.Config)
		want string
	}{
		{"no DSN", func(c *parsepgx.Config) { c.DSN = "" }, "parsepgx: dsn: empty"},
		{"no settle timeout", func(c *parsepgx.Config) { c.SettleTimeout = 0 }, "parsepgx: settle_timeout: 0s: must be positive"},
		{"a SQLite DSN", func(c *parsepgx.Config) { c.DSN = "./app.db" }, "parsepgx: dsn: a sqlite connection string"},
		{"CockroachDB", func(c *parsepgx.Config) { c.DSN = "cockroach://root:" + secret + "@db:26257/app" }, `parsepgx: dsn: parse: unsupported DSN: scheme "cockroach": CockroachDB is not supported`},
		{"no PostgreSQL connection string", func(c *parsepgx.Config) { c.DSN = "host=db port=notaport password=" + secret }, "parsepgx: dsn: not a PostgreSQL connection string pgx reads"},
		{"the DSN's pool size", func(c *parsepgx.Config) { c.DSN = "postgres://app:" + secret + "@db/app?pool_max_conns=50" }, "parsepgx: dsn: pool_max_conns: the pool is set by the max_conns, min_conns and other pool settings"},
		{"another collation", func(c *parsepgx.Config) { c.Collation = "en_US" }, `parsepgx: collation: "en_US": want pg_c_utf8 or C`},
		{"no connection", func(c *parsepgx.Config) { c.MaxConns = 0 }, "parsepgx: max_conns: 0: must be at least 1"},
		{"more idle connections than the pool holds", func(c *parsepgx.Config) { c.MinConns = 11 }, "parsepgx: min_conns: 11: must be from 0 to max_conns (10)"},
		{"connections that never expire", func(c *parsepgx.Config) { c.MaxConnLifetime = 0 }, "parsepgx: max_conn_lifetime: 0s: must be positive"},
		{"connections never idle out", func(c *parsepgx.Config) { c.MaxConnIdleTime = 0 }, "parsepgx: max_conn_idle_time: 0s: must be positive"},
		{"no health check", func(c *parsepgx.Config) { c.HealthCheckPeriod = 0 }, "parsepgx: health_check_period: 0s: must be positive"},
		{"no connect timeout", func(c *parsepgx.Config) { c.ConnectTimeout = 0 }, "parsepgx: connect_timeout: 0s: must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configOn(t, "postgres://app:"+secret+"@db/app")
			tc.edit(&cfg)
			opened, err := parsepgx.Open(t.Context(), cfg)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, opened)
			assert.NotContains(t, err.Error(), secret, "an error never quotes the DSN")
		})
	}
}

// parseprovider refuses a name the binary does not link, naming the ones
// it does, before any connection is made.
func TestChoice_RefusesAnotherName(t *testing.T) {
	_, err := parseprovider.Open(t.Context(), "sqlite", parsepgx.Choice(configOn(t, "postgres://db/app")))
	require.EqualError(t, err, `parseprovider: provider: "sqlite": this binary links postgres`)
}
