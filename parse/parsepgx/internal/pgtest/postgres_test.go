//go:build integration

package pgtest

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// formDSNEnv names the server of a test that sets its DSN itself.
const formDSNEnv = "PARSE_DBTEST_FORM_DSN"

// stopped ends a recordingTB's test the way FailNow and SkipNow end a real one.
type stopped struct{}

// recordingTB records how a fixture ended a test instead of ending it.
type recordingTB struct {
	testing.TB
	fatal, skip string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(stopped{})
}

func (r *recordingTB) Skipf(format string, args ...any) {
	r.skip = fmt.Sprintf(format, args...)
	panic(stopped{})
}

// Without a DSN a fixture skips its test, unless PostgreSQL is required: then
// it fails the test and names the variable that is missing.
func TestMissingDSN(t *testing.T) {
	tests := []struct {
		name     string
		require  string
		wantFail bool
	}{
		{"skips by default", "", false},
		{"skips unless the requirement is 1", "true", false},
		{"fails when required", "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(PostgresDSNEnv, "")
			t.Setenv(RequirePostgresEnv, tt.require)
			r := record(t, func(r *recordingTB) { Postgres(r) })
			if tt.wantFail {
				assert.Contains(t, r.fatal, PostgresDSNEnv)
				assert.Empty(t, r.skip)
			} else {
				assert.Contains(t, r.skip, PostgresDSNEnv)
				assert.Empty(t, r.fatal)
			}
		})
	}
}

// A test's deadline is TimeoutEnv's duration, 30 seconds when it is unset;
// a value that is no positive duration fails the test and names the
// variable.
func TestTimeout(t *testing.T) {
	tests := []struct {
		value    string
		want     time.Duration
		wantFail bool
	}{
		{"", 30 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"90", 0, true},
		{"0s", 0, true},
		{"-1s", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(TimeoutEnv, tt.value)
			var got time.Duration
			r := record(t, func(r *recordingTB) { got = Timeout(r) })
			if tt.wantFail {
				assert.Contains(t, r.fatal, TimeoutEnv)
				return
			}
			assert.Empty(t, r.fatal)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Every connection a test opens is on a schema of its own, with public after
// it, whichever form the server's DSN takes and however the test connects.
func TestSchemaSearchPath(t *testing.T) {
	postgresServer.requireSet(t)
	base, err := pgx.ParseConfig(os.Getenv(PostgresDSNEnv))
	require.NoError(t, err)
	urlForm, keywordForm := dsnForms(base)
	forms := []struct{ name, dsn string }{
		{"URL", urlForm},
		{"keyword-value", keywordForm},
	}
	const query = `SELECT array_to_string(current_schemas(false), ',')`
	opens := []struct {
		name string
		path func(t *testing.T, s *server) string
	}{
		{"sql.DB", func(t *testing.T, s *server) string {
			var path string
			require.NoError(t, s.open(t).QueryRowContext(Context(t), query).Scan(&path))
			return path
		}},
		{"pgx", func(t *testing.T, s *server) string {
			ctx := Context(t)
			conn, err := pgx.ConnectConfig(ctx, s.poolConfig(t).ConnConfig)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close(ctx) })
			var path string
			require.NoError(t, conn.QueryRow(ctx, query).Scan(&path))
			return path
		}},
		{"pgxpool", func(t *testing.T, s *server) string {
			ctx := Context(t)
			pool, err := pgxpool.NewWithConfig(ctx, s.poolConfig(t))
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			var path string
			require.NoError(t, pool.QueryRow(ctx, query).Scan(&path))
			return path
		}},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			t.Setenv(formDSNEnv, form.dsn)
			for _, open := range opens {
				t.Run(open.name, func(t *testing.T) {
					// current_schemas lists only schemas that exist.
					schemas := strings.Split(open.path(t, newServer(formDSNEnv, nil)), ",")
					require.Len(t, schemas, 2)
					assert.True(t, strings.HasPrefix(schemas[0], "t_"), "the test's schema is first: %v", schemas)
					assert.Equal(t, "public", schemas[1])
				})
			}
		})
	}
}

// A DSN pgx cannot read fails the test that asks for its server, naming the
// variable and not the password.
func TestMalformedDSN(t *testing.T) {
	t.Setenv(formDSNEnv, "host=127.0.0.1 password=hunter2 port=none")
	r := record(t, func(r *recordingTB) { newServer(formDSNEnv, nil).poolConfig(r) })
	assert.Contains(t, r.fatal, formDSNEnv)
	assert.NotContains(t, r.fatal, "hunter2")
}

// dsnForms spells the server c names as a URL and as keyword/value pairs.
func dsnForms(c *pgx.ConnConfig) (urlForm, keywordForm string) {
	sslmode := "disable"
	if c.TLSConfig != nil {
		sslmode = "require"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))),
		Path:     "/" + c.Database,
		RawQuery: "sslmode=" + sslmode,
	}
	quote := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace
	keywordForm = fmt.Sprintf("host='%s' port=%d user='%s' password='%s' dbname='%s' sslmode=%s",
		quote(c.Host), c.Port, quote(c.User), quote(c.Password), quote(c.Database), sslmode)
	return u.String(), keywordForm
}

// record runs f on a recordingTB over t and returns it, f stopped where a
// real test would have ended.
func record(t *testing.T, f func(*recordingTB)) (r *recordingTB) {
	r = &recordingTB{TB: t}
	defer func() {
		p := recover()
		if p != nil {
			_, ok := p.(stopped)
			if !ok {
				panic(p)
			}
		}
	}()
	f(r)
	return r
}
