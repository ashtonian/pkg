// Package pgtest gives a cookbook test a schema of its own on the PostgreSQL
// server COOKBOOK_POSTGRES_DSN names, so tests share one server without
// colliding. Its twin is parse/internal/dbtest, which serves parse's tests
// and which this module cannot import across the internal boundary: a change
// to the semantics of one belongs in both.
package pgtest

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

// DSNEnv names the server. A test that asks for it while it is unset skips,
// unless RequireEnv is 1. `make cookbook-postgres-up` starts one and prints
// the export.
const DSNEnv = "COOKBOOK_POSTGRES_DSN"

// RequireEnv set to 1 makes an unset DSNEnv fail the test that asks for the
// server instead of skipping it: a CI job whose coverage is the PostgreSQL
// run cannot pass with no statement executed.
const RequireEnv = "COOKBOOK_REQUIRE_PG"

// Driver is the database/sql driver DSN's data sources open with.
const Driver = "pgx"

// statementTimeout bounds each statement the fixture itself runs, connection
// included, so an unreachable server fails the first call rather than
// stalling it for the operating system's connect timeout.
const statementTimeout = 30 * time.Second

// ready reaches the server once per process; every later call answers with
// the first call's result.
var ready = sync.OnceValue(func() error {
	ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
	defer cancel()
	db, err := sql.Open(Driver, os.Getenv(DSNEnv))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("reaching the server: %w", err)
	}
	return nil
})

// DSN is the server's DSN with a fresh schema first on the search path, for
// the test to open as many pools on as it needs. The schema is dropped when
// the test ends. A server that is set and unreachable fails the test.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		if os.Getenv(RequireEnv) == "1" {
			t.Fatalf("pgtest: %s is unset and %s=1 requires PostgreSQL", DSNEnv, RequireEnv)
		}
		t.Skipf("pgtest: set %s to run PostgreSQL tests", DSNEnv)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("pgtest: parse %s: %v", DSNEnv, err)
	}
	if err := ready(); err != nil {
		t.Fatalf("pgtest: %s server: %v", DSNEnv, err)
	}

	// Random, not the clock: test binaries of several packages share one
	// server, and two fixtures can start in the same clock tick.
	schema := "t_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(t.Context(), statementTimeout)
	defer cancel()
	if err := exec(ctx, dsn, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("pgtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		// The test's context is already cancelled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
		defer cancel()
		if err := exec(ctx, dsn, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("pgtest: drop schema %s: %v", schema, err)
		}
	})

	q := u.Query()
	// Unlike dbtest's, public is off the path: no test needs an extension
	// there, and a table another user left in public must not stand in for
	// one the test has not created.
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func exec(ctx context.Context, dsn, stmt string) error {
	db, err := sql.Open(Driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, stmt)
	return err
}
