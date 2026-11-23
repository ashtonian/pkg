//go:build integration

package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresDSNEnv and PostGISDSNEnv name the servers the helpers use, each a
// DSN pgx reads: a URL (postgres://user:password@host:port/db?sslmode=disable)
// or keyword/value pairs (host=… port=… user=… dbname=…). A test that asks
// for a server whose variable is unset skips, unless RequirePostgresEnv is 1.
// `make parse-postgres-up` starts one PostGIS server that serves both and
// prints the exports.
const (
	PostgresDSNEnv = "PARSE_POSTGRES_DSN"
	PostGISDSNEnv  = "PARSE_POSTGIS_DSN"
)

// RequirePostgresEnv set to 1 makes an unset DSN variable fail the test that
// asks for its server instead of skipping it: a CI job whose coverage is the
// PostgreSQL run cannot pass with no statement executed.
const RequirePostgresEnv = "PARSE_REQUIRE_PG"

// TimeoutEnv names the deadline of a PostgreSQL test, a Go duration
// (default 30s): each statement the fixture runs, connection included, and
// the context Context returns. It is bounded so an unreachable server fails
// the first call rather than stalling for the operating system's connect
// timeout; raise it on a host contended enough to miss it.
const TimeoutEnv = "PARSE_TEST_TIMEOUT"

const defaultTimeout = 30 * time.Second

// postgisLockKey serializes CREATE EXTENSION across test processes that share
// a server: concurrent first creations collide on the catalog's unique index.
const postgisLockKey = 0x7061727365676973

// server is a PostgreSQL server named by an environment variable.
type server struct {
	dsnEnv string
	// ready parses the server's DSN, reaches the server and runs its setup
	// once per process; every later call answers with the first call's
	// result. The config is shared: callers copy it.
	ready func() (*pgxpool.Config, error)
}

var (
	postgresServer = newServer(PostgresDSNEnv, nil)
	postgisServer  = newServer(PostGISDSNEnv, enablePostGIS)
)

func newServer(dsnEnv string, setup func(context.Context, *pgx.Conn) error) *server {
	s := &server{dsnEnv: dsnEnv}
	s.ready = sync.OnceValues(func() (*pgxpool.Config, error) {
		limit, err := timeout()
		if err != nil {
			return nil, err
		}
		// pgxpool's parse is pgx's, less the pool's own pool_* settings, so
		// one parse serves connections, database/sql handles and pools.
		config, err := pgxpool.ParseConfig(os.Getenv(dsnEnv))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		conn, err := pgx.ConnectConfig(ctx, config.ConnConfig)
		if err != nil {
			return nil, fmt.Errorf("reaching the server: %w", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if setup != nil {
			err = setup(ctx, conn)
			if err != nil {
				return nil, err
			}
		}
		return config, nil
	})
	return s
}

func enablePostGIS(ctx context.Context, conn *pgx.Conn) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(postgisLockKey))
		if err != nil {
			return fmt.Errorf("locking extension setup: %w", err)
		}
		_, err = tx.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS postgis SCHEMA public")
		if err != nil {
			return fmt.Errorf("enabling postgis: %w", err)
		}
		return nil
	})
}

// timeout is TimeoutEnv's duration, or its default when it is unset.
func timeout() (time.Duration, error) {
	v := os.Getenv(TimeoutEnv)
	if v == "" {
		return defaultTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s=%q is no positive duration", TimeoutEnv, v)
	}
	return d, nil
}

// Timeout is the deadline TimeoutEnv sets. A value that is no positive
// duration fails the test.
func Timeout(t testing.TB) time.Duration {
	t.Helper()
	d, err := timeout()
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	return d
}

// Context is a context with Timeout's deadline, for a test that runs
// statements of its own. It is not the test's context, which is cancelled
// before cleanups run, so a cleanup can use it too.
func Context(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout(t))
	t.Cleanup(cancel)
	return ctx
}

// Postgres returns a database/sql handle on the PARSE_POSTGRES_DSN server,
// scoped to a fresh schema per test so tests share one server without
// colliding. Skips when the variable is unset, unless PARSE_REQUIRE_PG=1; a
// server that is set and unavailable fails the test.
func Postgres(t testing.TB) *sql.DB {
	t.Helper()
	return postgresServer.open(t)
}

// PostGIS is Postgres on the PARSE_POSTGIS_DSN server, with the PostGIS
// extension enabled in its public schema, for geo predicates.
func PostGIS(t testing.TB) *sql.DB {
	t.Helper()
	return postgisServer.open(t)
}

// PostgresConfig is the connection config of a fresh schema on the Postgres
// server, for a test that connects itself: pgx.ConnectConfig, or
// stdlib.OpenDB after setting runtime parameters of its own.
func PostgresConfig(t testing.TB) *pgx.ConnConfig {
	t.Helper()
	return postgresServer.poolConfig(t).ConnConfig
}

// PostgresPoolConfig is PostgresConfig for a test that opens pgxpool pools.
// Each pool on the schema takes a Copy of it.
func PostgresPoolConfig(t testing.TB) *pgxpool.Config {
	t.Helper()
	return postgresServer.poolConfig(t)
}

func (s *server) open(t testing.TB) *sql.DB {
	t.Helper()
	db := stdlib.OpenDB(*s.poolConfig(t).ConnConfig)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// poolConfig creates a schema for the test, dropped at cleanup, and returns
// the server's config with that schema first on the search path.
func (s *server) poolConfig(t testing.TB) *pgxpool.Config {
	t.Helper()
	s.requireSet(t)
	base, err := s.ready()
	if err != nil {
		t.Fatalf("dbtest: %s server: %v", s.dsnEnv, err)
	}

	// Random, not the clock: test binaries of several packages share one
	// server, and two fixtures can start in the same clock tick.
	schema := "t_" + strings.ToLower(rand.Text())
	limit := Timeout(t)
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	err = exec(ctx, base.ConnConfig, "CREATE SCHEMA "+schema)
	if err != nil {
		t.Fatalf("dbtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		// The test's context is already cancelled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		err := exec(ctx, base.ConnConfig, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Logf("dbtest: drop schema %s: %v", schema, err)
		}
	})

	config := base.Copy()
	// A startup parameter, so every connection opened from the config has
	// it, whichever form the DSN takes. public stays on the path for
	// extension functions (PostGIS).
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	return config
}

// requireSet skips the test when the server's variable is unset, or fails it
// when RequirePostgresEnv is 1.
func (s *server) requireSet(t testing.TB) {
	t.Helper()
	if os.Getenv(s.dsnEnv) != "" {
		return
	}
	if os.Getenv(RequirePostgresEnv) == "1" {
		t.Fatalf("dbtest: %s is unset and %s=1 requires PostgreSQL", s.dsnEnv, RequirePostgresEnv)
	}
	t.Skipf("dbtest: set %s to run PostgreSQL tests", s.dsnEnv)
}

func exec(ctx context.Context, config *pgx.ConnConfig, stmt string) error {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, stmt)
	return err
}
