//go:build integration

package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresDSNEnv and PostGISDSNEnv point the helpers at existing servers
// instead of starting containers.
const (
	PostgresDSNEnv = "PARSE_POSTGRES_DSN"
	PostGISDSNEnv  = "PARSE_POSTGIS_DSN"
)

// server is one lazily started PostgreSQL server shared by a test binary.
type server struct {
	image    string
	platform string // empty: the host's
	dsnEnv   string
	once     sync.Once
	dsn      string
	err      error
}

var (
	postgresServer = &server{image: "postgres:18", dsnEnv: PostgresDSNEnv}
	// postgis/postgis publishes amd64 images only; arm64 hosts run it
	// emulated.
	postgisServer = &server{image: "postgis/postgis:18-3.6", platform: "linux/amd64", dsnEnv: PostGISDSNEnv}
)

// Postgres returns a database/sql handle on a PostgreSQL 18 server, scoped
// to a fresh schema per test so tests share one server without colliding.
// The server is PARSE_POSTGRES_DSN when set; otherwise, with
// PARSE_INTEG_PG=1, one testcontainers
// postgres:18 per test binary. Skips only when neither is requested;
// a requested backend that cannot start fails the test.
func Postgres(t testing.TB) *sql.DB {
	t.Helper()
	return postgresServer.open(t)
}

// PostGIS is Postgres on a PostGIS 3.6 server (PARSE_POSTGIS_DSN, or a
// postgis/postgis:18-3.6 container), for geo predicates.
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
	db, err := sql.Open("pgx", s.schemaDSN(t))
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
		if os.Getenv("PARSE_INTEG_PG") != "1" {
			t.Skipf("dbtest: set %s or PARSE_INTEG_PG=1 to run PostgreSQL tests", s.dsnEnv)
		}
		s.once.Do(s.start)
		if s.err != nil {
			t.Fatalf("dbtest: requested %s unavailable: %v", s.image, s.err)
		}
		dsn = s.dsn
	}

	schema := fmt.Sprintf("t_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
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
		if cleanup, err := sql.Open("pgx", dsn); err == nil {
			_, _ = cleanup.Exec("DROP SCHEMA " + schema + " CASCADE")
			_ = cleanup.Close()
		}
	})
	return u.String()
}

func (s *server) start() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	opts := []testcontainers.ContainerCustomizer{
		tcpostgres.WithDatabase("parse_test"),
		tcpostgres.WithUsername("parse"),
		tcpostgres.WithPassword("parse"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(3 * time.Minute),
		),
	}
	if s.platform != "" {
		opts = append(opts, testcontainers.WithImagePlatform(s.platform))
	}
	c, err := tcpostgres.Run(ctx, s.image, opts...)
	if err != nil {
		s.err = err
		return
	}
	// The container lives for the test binary; testcontainers' reaper
	// removes it when the process exits.
	s.dsn, s.err = c.ConnectionString(ctx, "sslmode=disable")
}
