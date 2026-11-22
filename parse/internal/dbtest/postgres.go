//go:build integration

package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// PostgresDSNEnv and PostGISDSNEnv name the servers the helpers use. A test
// that asks for a server whose variable is unset skips, unless
// RequirePostgresEnv is 1. `make parse-postgres-up` starts one PostGIS server
// that serves both and prints the exports.
const (
	PostgresDSNEnv = "PARSE_POSTGRES_DSN"
	PostGISDSNEnv  = "PARSE_POSTGIS_DSN"
)

// RequirePostgresEnv set to 1 makes an unset DSN variable fail the test that
// asks for its server instead of skipping it: a CI job whose coverage is the
// PostgreSQL run cannot pass with no statement executed.
const RequirePostgresEnv = "PARSE_REQUIRE_PG"

// driverName is the database/sql driver the helpers open servers with.
const driverName = "pgx"

// statementTimeout bounds each statement the fixture itself runs, connection
// included, so an unreachable server fails the first call rather than
// stalling it for the operating system's connect timeout.
const statementTimeout = 30 * time.Second

// postgisLockKey serializes CREATE EXTENSION across test processes that share
// a server: concurrent first creations collide on the catalog's unique index.
const postgisLockKey = 0x7061727365676973

// server is a PostgreSQL server named by an environment variable.
type server struct {
	dsnEnv string
	// ready reaches the server once per process and runs its setup; every
	// later call answers with the first call's result.
	ready func() error
}

var (
	postgresServer = newServer(PostgresDSNEnv, nil)
	postgisServer  = newServer(PostGISDSNEnv, enablePostGIS)
)

func newServer(dsnEnv string, setup func(context.Context, *sql.DB) error) *server {
	s := &server{dsnEnv: dsnEnv}
	s.ready = sync.OnceValue(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
		defer cancel()
		db, err := sql.Open(driverName, os.Getenv(dsnEnv))
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("reaching the server: %w", err)
		}
		if setup != nil {
			return setup(ctx, db)
		}
		return nil
	})
	return s
}

func enablePostGIS(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(postgisLockKey)); err != nil {
		return fmt.Errorf("locking extension setup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS postgis SCHEMA public"); err != nil {
		return fmt.Errorf("enabling postgis: %w", err)
	}
	return tx.Commit()
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

// PostgresDSN is the DSN of a fresh schema on the Postgres server, for a test
// that opens its own pool (pgxpool) rather than a database/sql handle.
func PostgresDSN(t testing.TB) string {
	t.Helper()
	return postgresServer.schemaDSN(t)
}

func (s *server) open(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, s.schemaDSN(t))
	if err != nil {
		t.Fatalf("dbtest: open postgres schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// schemaDSN creates a schema for the test, dropped at cleanup, and returns
// the server's DSN with that schema first on the search path.
func (s *server) schemaDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(s.dsnEnv)
	if dsn == "" {
		if os.Getenv(RequirePostgresEnv) == "1" {
			t.Fatalf("dbtest: %s is unset and %s=1 requires PostgreSQL", s.dsnEnv, RequirePostgresEnv)
		}
		t.Skipf("dbtest: set %s to run PostgreSQL tests", s.dsnEnv)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("dbtest: parse %s: %v", s.dsnEnv, err)
	}
	if err := s.ready(); err != nil {
		t.Fatalf("dbtest: %s server: %v", s.dsnEnv, err)
	}

	// Random, not the clock: test binaries of several packages share one
	// server, and two fixtures can start in the same clock tick.
	schema := "t_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(t.Context(), statementTimeout)
	defer cancel()
	if err := exec(ctx, dsn, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("dbtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		// The test's context is already cancelled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
		defer cancel()
		if err := exec(ctx, dsn, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("dbtest: drop schema %s: %v", schema, err)
		}
	})

	q := u.Query()
	// public stays on the path for extension functions (PostGIS).
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

func exec(ctx context.Context, dsn, stmt string) error {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, stmt)
	return err
}
