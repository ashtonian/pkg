//go:build integration

package dbtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// PostgresDSNEnv and PostGISDSNEnv name the servers the helpers use. A test
// that asks for a server whose variable is unset skips. `make
// parse-postgres-up` starts one PostGIS server that serves both and prints
// the exports.
const (
	PostgresDSNEnv = "PARSE_POSTGRES_DSN"
	PostGISDSNEnv  = "PARSE_POSTGIS_DSN"
)

// driverName is the database/sql driver the helpers open servers with.
const driverName = "pgx"

// server is a PostgreSQL server named by an environment variable.
type server struct {
	dsnEnv string
}

var (
	postgresServer = &server{dsnEnv: PostgresDSNEnv}
	postgisServer  = &server{dsnEnv: PostGISDSNEnv}
)

// Postgres returns a database/sql handle on the PARSE_POSTGRES_DSN server,
// scoped to a fresh schema per test so tests share one server without
// colliding. Skips when the variable is unset; a server that is set and
// unavailable fails the test.
func Postgres(t testing.TB) *sql.DB {
	t.Helper()
	return postgresServer.open(t)
}

// PostGIS is Postgres on the PARSE_POSTGIS_DSN server, which must have the
// PostGIS extension available, for geo predicates.
func PostGIS(t testing.TB) *sql.DB {
	t.Helper()
	db := postgisServer.open(t)
	if _, err := db.Exec("CREATE EXTENSION IF NOT EXISTS postgis SCHEMA public"); err != nil {
		t.Fatalf("dbtest: enable postgis: %v", err)
	}
	return db
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
		t.Skipf("dbtest: set %s to run PostgreSQL tests", s.dsnEnv)
	}

	schema := fmt.Sprintf("t_%d", time.Now().UnixNano())
	admin, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("dbtest: open postgres: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("dbtest: create schema: %v", err)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("dbtest: parse dsn: %v", err)
	}
	q := u.Query()
	// public stays on the path for extension functions (PostGIS).
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	t.Cleanup(func() {
		if cleanup, err := sql.Open(driverName, dsn); err == nil {
			_, _ = cleanup.Exec("DROP SCHEMA " + schema + " CASCADE")
			_ = cleanup.Close()
		}
	})
	return u.String()
}
