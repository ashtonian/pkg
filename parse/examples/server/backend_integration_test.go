//go:build integration

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// backend is a database provider the integration tests serve the products
// from, each test on a fresh, seeded database of its own.
type backend struct {
	name string
	// config is the db settings of a fresh database.
	config func(t *testing.T) dbSettings
}

// The backends: SQLite in memory, and PostgreSQL on a fresh schema of the
// server PARSE_POSTGRES_DSN names. Without that variable the PostgreSQL
// subtests skip, unless PARSE_REQUIRE_PG=1 fails them instead.
var backends = []backend{
	{name: "sqlite", config: func(t *testing.T) dbSettings {
		return dbSettingsOf(t, map[string]any{})
	}},
	{name: "postgres", config: func(t *testing.T) dbSettings {
		return dbSettingsOf(t, map[string]any{"provider": "postgres", "postgres": map[string]any{"dsn": freshSchema(t)}})
	}},
}

// eachBackend runs test as a subtest on each backend.
func eachBackend(t *testing.T, test func(t *testing.T, b backend)) {
	t.Helper()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { test(t, b) })
	}
}

// dbSettingsOf are the db settings values set over their defaults, loaded
// and checked as the server loads them.
func dbSettingsOf(t *testing.T, values map[string]any) dbSettings {
	t.Helper()
	cfg, err := config.Decode[dbSettings](t.Context(), values)
	require.NoError(t, err)
	return *cfg
}

// freshSchema is PARSE_POSTGRES_DSN on a schema created for the test, first
// on the search path, and dropped once it ends.
func freshSchema(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PARSE_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("PARSE_REQUIRE_PG") == "1" {
			t.Fatal("PARSE_REQUIRE_PG=1 and PARSE_POSTGRES_DSN is unset")
		}
		t.Skip("PARSE_POSTGRES_DSN is unset")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	schema := "parse_example_" + strings.ToLower(rand.Text())
	_, err = conn.Exec(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		// t.Context is done before cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		assert.NoError(t, err)
		assert.NoError(t, conn.Close(ctx))
	})
	// pgx sends a URL parameter it does not read as a session setting, and
	// a keyword/value string takes one more pair.
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return dsn + " search_path=" + schema
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// open opens a fresh, seeded database, closed once the test ends.
func (b backend) open(t *testing.T) parseprovider.Database {
	t.Helper()
	return openAt(t, b.config(t))
}

// server serves the routes on a fresh, seeded database.
func (b backend) server(t *testing.T) *httptest.Server {
	t.Helper()
	return serverOn(t, b.open(t))
}

// openAt opens the database cfg names, seeded, and closes it once the test
// ends; closing it sooner is harmless, as Close may be called again.
func openAt(t *testing.T, cfg dbSettings) parseprovider.Database {
	t.Helper()
	db, err := openDB(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db
}

// serverOn serves the routes on db, logging nothing.
func serverOn(t *testing.T, db database) *httptest.Server {
	t.Helper()
	h, err := newServer(db, slog.New(slog.DiscardHandler), 5*time.Second, rand.Text)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// The seed keeps what a database already holds: opened again, a database
// that persists keeps a product's change and holds each product once.
func TestOpenDB_SeedKeepsWhatTheDatabaseHolds(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		cfg := b.config(t)
		if cfg.Provider == "sqlite" {
			cfg.SQLite.DSN = filepath.Join(t.TempDir(), "products.db")
		}
		db := openAt(t, cfg)
		srv := serverOn(t, db)
		var patched struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p01", `{"price":1}`, &patched))
		srv.Close()
		require.NoError(t, db.Close())

		srv = serverOn(t, openAt(t, cfg))
		var p page
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?id=p01", "", &p))
		require.Len(t, p.Items, 1)
		assert.InDelta(t, 1.0, p.Items[0].Price, 0, "the change is kept")
		var all page
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?include_total=true", "", &all))
		require.NotNil(t, all.PageInfo.TotalCount)
		assert.Equal(t, int64(len(seedProducts)), *all.PageInfo.TotalCount, "each product once")
	})
}

// The readiness probe answers 200 while the database answers, and 503 with
// a WARN entry once it does not.
func TestServer_ReadinessFollowsTheDatabase(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		db := b.open(t)
		var logs strings.Builder
		h, err := newServer(db, newLogger(&logs, slog.LevelDebug), 5*time.Second, rand.Text)
		require.NoError(t, err)
		probe := func() (int, map[string]any) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
			return rec.Code, body
		}

		status, body := probe()
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[string]any{"status": "ready"}, body)
		assert.Empty(t, logs.String(), "the probe's request entry is trace detail")

		require.NoError(t, db.Close())
		status, body = probe()
		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, codeNotReady, body["code"])
		assert.Contains(t, logs.String(), `"msg":"database not ready"`)
		assert.Contains(t, logs.String(), `"level":"WARN"`)
	})
}

// run opens the database the db settings name, serves on it, and closes it
// once the drain ends: with no settings but the address, on in-memory
// SQLite; with the environment's db settings, on PostgreSQL.
func TestRun_ServesOnTheConfiguredDatabase(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		cfg := b.config(t)
		if cfg.Provider != "sqlite" {
			t.Setenv("PARSE_SERVER_DB_PROVIDER", cfg.Provider)
			t.Setenv("PARSE_SERVER_DB_POSTGRES_DSN", cfg.Postgres.DSN)
		}
		out, in := io.Pipe()
		level := new(slog.LevelVar)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ran := make(chan error, 1)
		go func() {
			err := run(ctx, cancel, []string{"--addr", "127.0.0.1:0", "--log-level", "info"}, level, newLogger(in, level))
			assert.NoError(t, in.Close())
			ran <- err
		}()
		// Every entry is read, so a log write never holds up the server.
		entries := make(chan map[string]any, 64)
		go func() {
			defer close(entries)
			lines := bufio.NewScanner(out)
			for lines.Scan() {
				var entry map[string]any
				assert.NoError(t, json.Unmarshal(lines.Bytes(), &entry), lines.Text())
				entries <- entry
			}
		}()
		until := func(msg string) map[string]any {
			t.Helper()
			for entry := range entries {
				if entry["msg"] == msg {
					return entry
				}
			}
			t.Fatalf("no %q entry", msg)
			return nil
		}

		assert.Equal(t, cfg.Provider, until("database opened")["provider"])
		addr, ok := until("listening")["addr"].(string)
		require.True(t, ok)
		get := func(path string, out any) int {
			t.Helper()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
			require.NoError(t, err)
			res, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { assert.NoError(t, res.Body.Close()) }()
			require.NoError(t, json.NewDecoder(res.Body).Decode(out))
			return res.StatusCode
		}
		var p page
		require.Equal(t, http.StatusOK, get("/api/products?category=books&include_total=true", &p))
		require.NotNil(t, p.PageInfo.TotalCount)
		assert.Equal(t, int64(3), *p.PageInfo.TotalCount)
		var ready map[string]any
		assert.Equal(t, http.StatusOK, get("/readyz", &ready))

		cancel()
		require.NoError(t, <-ran)
		until("shutting down")
		for range entries {
		}
	})
}
