package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/examples/cookbook/internal/pgtest"
)

// backend is a database the recipe runs on. Each test that reaches the
// table runs once on each: SQLite always, PostgreSQL when pgtest has a
// server, so the SQL the recipe documents for Postgres runs against one.
type backend struct {
	name string
	// fresh is an empty database for the test, dropped when it ends.
	fresh func(t *testing.T) database
	// tables lists the tables of the database a connection is open on.
	tables string
}

var (
	sqliteBackend = backend{
		name: "sqlite",
		fresh: func(t *testing.T) database {
			dir := t.TempDir()
			// A connection waits up to busy_timeout for another replica's write.
			return database{driver: "sqlite", dsn: filepath.Join(dir, "settings.db") + "?_pragma=busy_timeout(10000)", dir: dir}
		},
		tables: `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`,
	}
	postgresBackend = backend{
		name: "postgres",
		fresh: func(t *testing.T) database {
			return database{driver: pgtest.Driver, dsn: pgtest.DSN(t), dir: t.TempDir()}
		},
		tables: `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() ORDER BY table_name`,
	}
	backends = []backend{sqliteBackend, postgresBackend}
)

// onEveryBackend runs test once on each backend, as a subtest named for it.
func onEveryBackend(t *testing.T, test func(t *testing.T, b backend)) {
	t.Helper()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { test(t, b) })
	}
}

// database is one test's settings database. Every replica opens a pool of
// its own on it, and reads config.yaml from dir, where it is absent unless
// the test writes one: the table over defaults.
type database struct {
	driver, dsn, dir string
}

// open is a pool on d, closed when the test ends.
func (d database) open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(d.driver, d.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (d database) sources() Sources {
	return appSources(filepath.Join(d.dir, "config.yaml"), slog.New(slog.DiscardHandler))
}

// replica opens the settings table in d as one replica of the service
// does, with a pool and Settings of its own, the schema migrated.
func replica(t *testing.T, d database, maxRows int) (*Settings, *sql.DB) {
	return replicaWith(t, d, TableLimits{Rows: maxRows, Bytes: 1 << 20}, slog.New(slog.DiscardHandler))
}

// replicaWith is replica with limits and a logger of the test's.
func replicaWith(t *testing.T, d database, limits TableLimits, logger *slog.Logger) (*Settings, *sql.DB) {
	t.Helper()
	db := d.open(t)
	require.NoError(t, migrate(t.Context(), db, 10*time.Second))
	return NewSettings(db, d.sources(), 10*time.Second, limits, logger), db
}

func newReplica(t *testing.T, b backend, maxRows int) (*Settings, *sql.DB) {
	return replica(t, b.fresh(t), maxRows)
}

func write(t *testing.T, s *Settings, edits map[string]any) {
	t.Helper()
	_, err := s.Write(t.Context(), edits)
	require.NoError(t, err)
}

func load(t *testing.T, s *Settings) *AppConfig {
	t.Helper()
	cfg, err := config.Load[AppConfig](t.Context(), s.Options()...)
	require.NoError(t, err)
	return cfg
}

// rowsOf is the table as stored: each key and its JSON text.
func rowsOf(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT key, value FROM settings`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		require.NoError(t, rows.Scan(&key, &value))
		out[key] = value
	}
	require.NoError(t, rows.Err())
	return out
}

func refusal(t *testing.T, err error) *RefusedError {
	t.Helper()
	refused, ok := errors.AsType[*RefusedError](err)
	require.True(t, ok, "a *RefusedError, not %v", err)
	return refused
}

// A write is checked by the load that reads it, and a reload applies it:
// limits each invalid alone are valid together, and keys holding a dot or
// an index load back as written. A write that load refuses stores nothing
// and names only its own keys.
func TestSettings_WriteIsCheckedByTheLoad(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		r, err := config.NewReloader[AppConfig](t.Context(), s.Options(), config.WithPollInterval(time.Hour))
		require.NoError(t, err)
		t.Cleanup(func() { _ = r.Stop(t.Context()) })
		apply := func(edits map[string]any) {
			t.Helper()
			write(t, s, edits)
			_, err := r.Reload(t.Context())
			require.NoError(t, err)
		}

		apply(map[string]any{"limits.min": 150, "limits.max": 200})
		assert.Equal(t, Limits{Min: 150, Max: 200, Bytes: 1 << 20}, r.Config().Limits)
		apply(map[string]any{
			`upstreams["api.internal"]`: map[string]any{"url": "https://api.internal.example"},
			"routes[0]":                 map[string]any{"path": "/v1", "upstream": "api.internal"},
		})
		apply(map[string]any{`upstreams["api.internal"].timeout`: "2s", "routes[1].path": "/v2", "routes[1].upstream": "api.internal"})
		assert.Equal(t, map[string]Upstream{"api.internal": {URL: "https://api.internal.example", Timeout: 2 * time.Second}}, r.Config().Upstreams)
		assert.Equal(t, []Route{{"/v1", "api.internal"}, {"/v2", "api.internal"}}, r.Config().Routes)
		assert.Equal(t, `"2s"`, rowsOf(t, db)[`upstreams["api.internal"].timeout`])

		stored := rowsOf(t, db)
		for name, tc := range map[string]struct {
			edits  map[string]any
			reason string
			keys   []string
		}{
			"a cross-field rule":                 {map[string]any{"limits.min": 300}, ReasonInvalid, []string{"limits.min"}},
			"a route naming no upstream":         {map[string]any{"routes[0].upstream": "missing"}, ReasonInvalid, []string{"routes[0].upstream"}},
			"deleting an upstream a route names": {map[string]any{`upstreams["api.internal"]`: nil}, ReasonInvalid, []string{`upstreams["api.internal"]`}},
			"an index past the list's end":       {map[string]any{"routes[5].path": "/v5"}, ReasonInvalid, []string{"routes[5].path"}},
			"an unknown key":                     {map[string]any{"limits.maxx": 250, "limits.max": 250}, ReasonUnknown, []string{"limits.maxx"}},
			"an unknown key inside a value":      {map[string]any{"limits": map[string]any{"maxx": 250}}, ReasonUnknown, []string{"limits"}},
			"one key spelled twice":              {map[string]any{"limits.max": 250, "LIMITS.Max": 260}, ReasonRepeated, []string{"limits.max"}},
			"a key and its former name":          {map[string]any{"limits.max": 250, "limits.ceiling": 260}, ReasonRepeated, []string{"limits.max"}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := s.Write(t.Context(), tc.edits)
				refused := refusal(t, err)
				assert.Equal(t, tc.reason, refused.Reason)
				assert.Equal(t, tc.keys, refused.Keys)
			})
		}
		assert.Equal(t, stored, rowsOf(t, db))
	})
}

// Keys are stored as CanonicalKey spells them, so every spelling of a key
// writes one row: a former name, any letter case, and a row another writer
// stored under another spelling.
func TestSettings_KeysAreStoredCanonically(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		write(t, s, map[string]any{"LIMITS.Ceiling": 250})
		assert.Equal(t, map[string]string{"limits.max": "250"}, rowsOf(t, db))
		write(t, s, map[string]any{"Limits.MAX": 260, `UPSTREAMS["Api"].URL`: "https://api.example"})
		assert.Equal(t, map[string]string{"limits.max": "260", `upstreams["Api"].url`: `"https://api.example"`}, rowsOf(t, db))

		_, err := db.Exec(`DELETE FROM settings WHERE key = 'limits.max'`)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO settings (key, value) VALUES ('LIMITS.MAX', '270')`)
		require.NoError(t, err)
		assert.Equal(t, 270, load(t, s).Limits.Max)
		write(t, s, map[string]any{"limits.max": 280})
		assert.Equal(t, map[string]string{"limits.max": "280", `upstreams["Api"].url`: `"https://api.example"`}, rowsOf(t, db))

		cfg := load(t, s)
		assert.Equal(t, 280, cfg.Limits.Max)
		assert.Equal(t, map[string]Upstream{"Api": {URL: "https://api.example", Timeout: time.Second}}, cfg.Upstreams)
	})
}

// A key's write replaces the rows stored beneath it, and a key set to null
// deletes its row and theirs.
func TestSettings_AWriteReplacesTheRowsBeneathIt(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		write(t, s, map[string]any{
			`upstreams["a"]`:     map[string]any{"url": "https://a.example"},
			"routes[0]":          map[string]any{"path": "/v1", "upstream": "a"},
			"routes[1].path":     "/v2",
			"routes[1].upstream": "a",
			"limits.min":         5,
			"limits.max":         300,
		})
		write(t, s, map[string]any{`upstreams["a"].timeout`: "2s"})

		write(t, s, map[string]any{`upstreams["a"]`: map[string]any{"url": "https://b.example"}})
		assert.NotContains(t, rowsOf(t, db), `upstreams["a"].timeout`)
		assert.Equal(t, map[string]Upstream{"a": {URL: "https://b.example", Timeout: time.Second}}, load(t, s).Upstreams)

		write(t, s, map[string]any{"routes": []any{map[string]any{"path": "/only", "upstream": "a"}}})
		assert.Equal(t, []Route{{"/only", "a"}}, load(t, s).Routes)

		write(t, s, map[string]any{"limits.max": nil})
		assert.Equal(t, Limits{Min: 5, Max: 100, Bytes: 1 << 20}, load(t, s).Limits, "the default again")
		write(t, s, map[string]any{"routes": nil, `upstreams["a"]`: nil})
		assert.Equal(t, map[string]string{"limits.min": "5"}, rowsOf(t, db))
		assert.Empty(t, load(t, s).Upstreams)
	})
}

// Writes from every replica take turns: two writes each valid alone but
// not together never both commit, whichever replica makes them.
func TestSettings_WritersOnEveryReplicaTakeTurns(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		one, _ := replica(t, d, 100)
		two, _ := replica(t, d, 100)
		for round := range 20 {
			start := make(chan struct{})
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i, w := range []struct {
				s     *Settings
				edits map[string]any
			}{{one, map[string]any{"limits.min": 90}}, {two, map[string]any{"limits.max": 50}}} {
				wg.Go(func() {
					<-start
					_, errs[i] = w.s.Write(t.Context(), w.edits)
				})
			}
			close(start)
			wg.Wait()
			refused := 0
			for _, err := range errs {
				if err != nil {
					assert.Equal(t, ReasonInvalid, refusal(t, err).Reason, "round %d", round)
					refused++
				}
			}
			require.Equal(t, 1, refused, "round %d: the second write is checked against the first", round)
			load(t, one)
			write(t, one, map[string]any{"limits.min": nil, "limits.max": nil})
		}
	})
}

// The table holds at most its row cap: a write that would grow it past the
// cap is refused, one that replaces rows is not, and a table grown past
// twice the cap by another writer fails the read rather than load unbounded.
func TestSettings_RowCap(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 3)
		write(t, s, map[string]any{"limits.min": 2, "limits.max": 50, "limits.bytes": 10})
		_, err := s.Write(t.Context(), map[string]any{`upstreams["a"]`: map[string]any{"url": "https://a.example"}})
		refused := refusal(t, err)
		assert.Equal(t, ReasonFull, refused.Reason)
		assert.Equal(t, []string{`upstreams["a"]`}, refused.Keys)
		write(t, s, map[string]any{"limits.max": 60})
		write(t, s, map[string]any{"limits": map[string]any{"min": 2, "max": 60, "bytes": 10}})
		assert.Len(t, rowsOf(t, db), 1)

		for _, key := range []string{"limits.min", "limits.max", "limits.bytes", "routes[0].path", "routes[0].upstream", "routes[1].path"} {
			_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, '1')`, key)
			require.NoError(t, err)
		}
		_, err = config.Load[AppConfig](t.Context(), s.Options()...)
		require.ErrorIs(t, err, config.ErrSource)
		assert.Contains(t, err.Error(), "more than 6 rows")
		_, err = s.Write(t.Context(), map[string]any{"routes": nil})
		assert.ErrorContains(t, err, "more than 6 rows")
	})
}

func put(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
	return rec
}

// Over HTTP a write is a 204 that tells the reloader; a refused one is a
// 422 naming only the client's keys, never a value or a stored key; a body
// that is not one JSON object, or exceeds the cap, is refused before the
// store sees it. Integers keep every digit.
func TestSettingsHandler(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		changed := 0
		h := settingsHandler(s, func() { changed++ }, 64, slog.New(slog.DiscardHandler))

		rec := put(h, `{"limits.min": 150, "limits.max": 200}`)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, 1, changed)

		rec = put(h, `{"limits.min": "FAKE_SECRET_VALUE"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		var refused RefusedError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
		assert.Equal(t, RefusedError{Reason: ReasonInvalid, Keys: []string{"limits.min"}}, refused)
		assert.NotContains(t, rec.Body.String(), "FAKE_SECRET_VALUE")
		assert.NotContains(t, rec.Body.String(), "limits.max")
		assert.Equal(t, 1, changed)

		for body, status := range map[string]int{
			`[1]`:                  http.StatusBadRequest,
			`null`:                 http.StatusBadRequest,
			`{"limits.max": 1} {}`: http.StatusBadRequest,
			`{"limits.max": 1`:     http.StatusBadRequest,
			`{"limits.max": ` + strings.Repeat(" ", 64) + `1}`: http.StatusRequestEntityTooLarge,
		} {
			assert.Equal(t, status, put(h, body).Code, body)
		}

		rec = put(h, `{"limits.bytes": 9007199254740993}`)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, "9007199254740993", rowsOf(t, db)["limits.bytes"])
		assert.Equal(t, int64(9007199254740993), load(t, s).Limits.Bytes)
	})
}

// A store that fails is a 503, logged at Error with its cause: a stored
// table that no longer loads, which no write is at fault for, and a
// database that is gone.
func TestSettingsHandler_StoreFailures(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		var logs bytes.Buffer
		level := new(slog.LevelVar)
		level.Set(slog.LevelWarn)
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level})))
		logged := func() []map[string]any {
			var entries []map[string]any
			for line := range strings.Lines(logs.String()) {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				entries = append(entries, entry)
			}
			logs.Reset()
			return entries
		}

		_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('limits.min', '"lots"')`)
		require.NoError(t, err)
		rec := put(h, `{"limits.max": 300}`)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		entries := logged()
		require.Len(t, entries, 1)
		assert.Equal(t, "ERROR", entries[0]["level"])
		assert.Equal(t, "writing settings", entries[0]["msg"])
		assert.Contains(t, entries[0]["error"], "the settings fail to load without this write")

		require.NoError(t, db.Close())
		rec = put(h, `{"limits.max": 300}`)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		entries = logged()
		require.Len(t, entries, 1)
		assert.Equal(t, "ERROR", entries[0]["level"])
		assert.Contains(t, entries[0]["error"], "database is closed")
	})
}

// The boot defaults load, and the startup entry is written whatever the
// level, its database URL withheld.
func TestLogStarting(t *testing.T) {
	const url = "postgres://settings:FAKE_PASSWORD@db.example/settings"
	boot, err := config.Load[BootConfig](t.Context(), config.WithEnvPrefix("SETTINGS"),
		config.WithEnviron(func() []string { return []string{"SETTINGS_DATABASE_URL=" + url} }))
	require.NoError(t, err)
	var logs bytes.Buffer
	level := new(slog.LevelVar)
	require.NoError(t, level.UnmarshalText([]byte(boot.LogLevel)))
	require.NoError(t, logStarting(t.Context(), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level})), level, "serve", boot))

	var entry struct {
		Level, Msg, LogLevel string
		Config               map[string]string
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "starting", entry.Msg)
	assert.Equal(t, ":8080", entry.Config["addr"])
	assert.NotContains(t, logs.String(), "FAKE_PASSWORD")
	assert.Equal(t, slog.LevelWarn, level.Level())
}

// txLevels records the isolation level each transaction on a connection
// begins at.
type txLevels struct {
	mu     sync.Mutex
	levels []sql.IsolationLevel
}

// sqliteConn is what a modernc.org/sqlite connection implements.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
}

// recordingConn is a SQLite connection that records its transactions'
// isolation levels.
type recordingConn struct {
	sqliteConn
	levels *txLevels
}

func (c recordingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.levels.mu.Lock()
	c.levels.levels = append(c.levels.levels, sql.IsolationLevel(opts.Isolation))
	c.levels.mu.Unlock()
	return c.sqliteConn.BeginTx(ctx, opts)
}

type recordingConnector struct {
	dsn    string
	levels *txLevels
}

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	full, ok := conn.(sqliteConn)
	if !ok {
		return nil, errors.New("the SQLite connection lacks a method recordingConn forwards")
	}
	return recordingConn{full, c.levels}, nil
}

func (recordingConnector) Driver() driver.Driver { return &sqlite.Driver{} }

// Every write begins at READ COMMITTED, the level at which the lock row
// serializes writers on Postgres whatever the server's default.
func TestSettings_WritesBeginAtReadCommitted(t *testing.T) {
	var levels txLevels
	dir := t.TempDir()
	db := sql.OpenDB(recordingConnector{dsn: filepath.Join(dir, "settings.db") + "?_pragma=busy_timeout(10000)", levels: &levels})
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, migrate(t.Context(), db, time.Second))
	s := NewSettings(db, appSources(filepath.Join(dir, "config.yaml"), slog.New(slog.DiscardHandler)), 10*time.Second, TableLimits{Rows: 100, Bytes: 1 << 20}, slog.New(slog.DiscardHandler))
	write(t, s, map[string]any{"limits.max": 300})
	write(t, s, map[string]any{"limits.max": nil})
	assert.Equal(t, []sql.IsolationLevel{sql.LevelReadCommitted, sql.LevelReadCommitted}, levels.levels)
}

// When the settings fail to load without the write, the 503 says so and
// names the sources beneath the table to check, not the table alone.
func TestSettingsHandler_SettingsThatFailWithoutTheWrite(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		s, _ := replica(t, d, 100)
		file := filepath.Join(d.dir, "config.yaml")
		require.NoError(t, os.WriteFile(file, []byte("limits:\n  max: lots\n"), 0o600))
		var logs bytes.Buffer
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.NewJSONHandler(&logs, nil)))
		rec := put(h, `{"limits.min": 2}`)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var entry struct{ Level, Msg, Error string }
		require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
		assert.Equal(t, "ERROR", entry.Level)
		assert.Contains(t, entry.Error, "the settings fail to load without this write")
		assert.Contains(t, entry.Error, file)
		assert.Contains(t, entry.Error, "APP_")
	})
}

// A write that times out waiting for its turn, behind this replica's own
// write or another replica's lock row, is contention: a 503 with
// Retry-After, logged at Warn. Error is for failures.
func TestSettingsHandler_ContentionIsA503ToRetry(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		db := d.open(t)
		require.NoError(t, migrate(t.Context(), db, 10*time.Second))
		s := NewSettings(db, d.sources(), 200*time.Millisecond, TableLimits{Rows: 100, Bytes: 1 << 20}, slog.New(slog.DiscardHandler))
		var logs bytes.Buffer
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.NewJSONHandler(&logs, nil)))
		contended := func(t *testing.T) {
			t.Helper()
			logs.Reset()
			rec := put(h, `{"limits.max": 300}`)
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "1", rec.Header().Get("Retry-After"))
			var entry struct{ Level, Msg string }
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.Equal(t, "WARN", entry.Level, entry.Msg)
		}

		t.Run("behind this replica's write", func(t *testing.T) {
			s.writing <- struct{}{}
			defer func() { <-s.writing }()
			contended(t)
		})
		t.Run("behind another replica's lock row", func(t *testing.T) {
			other, _ := replica(t, d, 100)
			tx, err := other.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.Exec("UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
			require.NoError(t, err)
			contended(t)
		})
	})
}

// tablesOf are the tables of the database db is open on.
func tablesOf(t *testing.T, b backend, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(b.tables)
	require.NoError(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}

// Serving runs no DDL: on a database without the schema it fails at boot,
// naming the migrate step, and creates nothing. migrate applies the schema,
// as often as it runs, and serving's check then passes.
func TestRun_SchemaIsAnExplicitStep(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		environ := func() []string {
			return []string{"SETTINGS_DATABASE_URL=" + d.dsn, "SETTINGS_CONFIG_FILE=" + filepath.Join(d.dir, "config.yaml")}
		}
		open := func(url string) (*sql.DB, error) { return sql.Open(d.driver, url) }
		command := func(args ...string) error {
			return run(t.Context(), args, environ, open, slog.New(slog.DiscardHandler), new(slog.LevelVar))
		}
		db := d.open(t)

		err := command()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "settingsstore migrate")
		assert.Empty(t, tablesOf(t, b, db))

		require.NoError(t, command("migrate"))
		require.NoError(t, command("migrate"))
		assert.Equal(t, []string{"settings", "settings_lock"}, tablesOf(t, b, db))
		assert.NoError(t, checkSchema(t.Context(), db, 10*time.Second))

		assert.ErrorContains(t, command("serve", "now"), "usage")
	})
}

// A row whose key the schema no longer reads, its field removed by a
// release, is deleted by a null write of its key, with the rows beneath it.
// Only null may name such a key, only to delete such rows, and only where
// one is stored: a spelling the schema refuses never deletes a row it reads.
func TestSettings_ANullDeletesARowTheSchemaNoLongerReads(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		write(t, s, map[string]any{"limits.max": 200})
		for key, value := range map[string]string{"legacy": "1", "legacy.part": "2"} {
			_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2)`, key, value)
			require.NoError(t, err)
		}
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.DiscardHandler))
		assert.Equal(t, http.StatusNoContent, put(h, `{"legacy": null}`).Code)
		assert.Equal(t, map[string]string{"limits.max": "200"}, rowsOf(t, db))

		for _, body := range []string{`{"legacy": 1}`, `{"legacy": null}`, `{"limits.maxx": null}`, `{"limits[\"max\"]": null}`} {
			rec := put(h, body)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, body)
			assert.Contains(t, rec.Body.String(), `"reason":"unknown"`, body)
		}
		assert.Equal(t, map[string]string{"limits.max": "200"}, rowsOf(t, db))
	})
}

// What a removed field leaves in the table, a row or a part of a row's
// value, is left out of every load: the service restarts, reloads and
// writes as though it were gone. What a write sets is not left out, so a key
// the schema refuses inside a written value still refuses the write.
func TestSettings_LoadsLeaveOutWhatTheSchemaNoLongerReads(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		var logs bytes.Buffer
		s, db := replicaWith(t, b.fresh(t), TableLimits{Rows: 100, Bytes: 1 << 20}, slog.New(slog.NewJSONHandler(&logs, nil)))
		write(t, s, map[string]any{"limits.max": 200})
		for key, value := range map[string]string{
			"legacy":    "1",
			"limits":    `{"min": 2, "old": 5}`,
			"upstreams": `{"a": {"url": "https://a.example", "retries": 3}}`,
		} {
			_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2)`, key, value)
			require.NoError(t, err)
		}
		cfg := load(t, s)
		assert.Equal(t, Limits{Min: 2, Max: 200, Bytes: 1 << 20}, cfg.Limits)
		assert.Equal(t, map[string]Upstream{"a": {URL: "https://a.example", Timeout: time.Second}}, cfg.Upstreams)
		var entry struct {
			Level string
			Keys  []string
		}
		require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
		assert.Equal(t, "WARN", entry.Level)
		assert.Equal(t, []string{"legacy", "limits.old", `upstreams["a"].retries`}, entry.Keys)

		write(t, s, map[string]any{"limits.max": 300})
		assert.Equal(t, 300, load(t, s).Limits.Max)
		_, err := s.Write(t.Context(), map[string]any{"limits": map[string]any{"min": 3, "typo": 5}})
		assert.Equal(t, ReasonUnknown, refusal(t, err).Reason)
	})
}

// A key no field reads inside a value a write sets refuses the write as
// unknown whatever its name holds, a separator or a quote among them; one
// inside a stored row is tolerated like any other.
func TestSettings_UnknownKeysOfAnyNameInsideAValue(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		for _, name := range []string{"a.b", "max.x", "a[0]", `q"z`, ""} {
			_, err := s.Write(t.Context(), map[string]any{"limits": map[string]any{"min": 3, name: 5}})
			assert.Equal(t, ReasonUnknown, refusal(t, err).Reason, name)
		}
		_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2)`, "limits", `{"min": 2, "max.x": 5, "a.b": 1}`)
		require.NoError(t, err)
		assert.Equal(t, 2, load(t, s).Limits.Min)
		write(t, s, map[string]any{"limits.max": 300})
		assert.Equal(t, Limits{Min: 2, Max: 300, Bytes: 1 << 20}, load(t, s).Limits)
	})
}

// A write of a key that a stored row above it sets takes effect: the key is
// cut out of that row's value, under any spelling the value holds it by, in
// the same transaction and checked by the same load. A row left setting
// nothing is deleted. A key beneath a stored list or scalar cannot be cut
// out, and refuses the write.
func TestSettings_AWriteTakesEffectBeneathAStoredRowAboveIt(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		write(t, s, map[string]any{"limits": map[string]any{"min": 2, "ceiling": 300}})
		write(t, s, map[string]any{"limits.max": nil})
		assert.Equal(t, Limits{Min: 2, Max: 100, Bytes: 1 << 20}, load(t, s).Limits, "deleted: the default again")
		assert.Equal(t, map[string]string{"limits": `{"min":2}`}, rowsOf(t, db))

		write(t, s, map[string]any{"limits": map[string]any{"max": 300}})
		write(t, s, map[string]any{"limits.max": 250})
		assert.Equal(t, 250, load(t, s).Limits.Max, "set")
		assert.Equal(t, map[string]string{"limits.max": "250"}, rowsOf(t, db), "the row above, left setting nothing, deleted")

		write(t, s, map[string]any{"upstreams": map[string]any{"a": map[string]any{"url": "https://a.example", "timeout": "3s"}}})
		write(t, s, map[string]any{`upstreams["a"].timeout`: "2s"})
		assert.Equal(t, map[string]Upstream{"a": {URL: "https://a.example", Timeout: 2 * time.Second}}, load(t, s).Upstreams)
		_, err := s.Write(t.Context(), map[string]any{`upstreams["a"]`: map[string]any{"timeout": "4s"}})
		assert.Equal(t, ReasonInvalid, refusal(t, err).Reason, "the entry replaced whole, its url with it")
		assert.Equal(t, `{"a":{"url":"https://a.example"}}`, rowsOf(t, db)["upstreams"])

		for name, row := range map[string]struct{ key, value, write string }{
			"a list":   {"routes", `[{"path": "/a", "upstream": "a"}]`, "routes[0].path"},
			"a scalar": {`upstreams["b"]`, `"https://b.example"`, `upstreams["b"].timeout`},
		} {
			_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2)`, row.key, row.value)
			require.NoError(t, err)
			before := rowsOf(t, db)
			_, err = s.Write(t.Context(), map[string]any{row.write: "/b"})
			refused := refusal(t, err)
			assert.Equal(t, ReasonEnclosed, refused.Reason, name)
			assert.Equal(t, []string{row.write}, refused.Keys, name)
			assert.Equal(t, before, rowsOf(t, db), name)
			_, err = db.Exec(`DELETE FROM settings WHERE key = $1`, row.key)
			require.NoError(t, err)
		}
	})
}

// A table over its row cap, the cap lowered beneath it, still loads, and
// takes every write that leaves it no larger, deletes among them, so the
// API can bring it back under the cap; a write that grows it is refused.
func TestSettings_ATableOverItsRowCapRecovers(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		wide, _ := replica(t, d, 10)
		write(t, wide, map[string]any{"limits.min": 2, "limits.max": 50, "limits.bytes": 10, `upstreams["a"].url`: "https://a.example"})
		var logs bytes.Buffer
		narrow, db := replicaWith(t, d, TableLimits{Rows: 2, Bytes: 1 << 20}, slog.New(slog.NewJSONHandler(&logs, nil)))

		assert.Equal(t, 50, load(t, narrow).Limits.Max, "a reload, and a restart, load the table")
		var entry struct{ Level, Msg string }
		require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
		assert.Equal(t, "WARN", entry.Level)
		assert.Equal(t, "settings table over its cap", entry.Msg)
		write(t, narrow, map[string]any{"limits.max": 60})
		_, err := narrow.Write(t.Context(), map[string]any{"routes[0].path": "/a", "routes[0].upstream": "a"})
		assert.Equal(t, ReasonFull, refusal(t, err).Reason)
		write(t, narrow, map[string]any{"limits": nil})
		assert.Equal(t, map[string]string{`upstreams["a"].url`: `"https://a.example"`}, rowsOf(t, db))
	})
}

// The table holds at most its byte cap, each row's key and its value's JSON
// text, by its row cap's rule: a write that would grow it past the cap is
// refused and one that does not is taken, a table over a lowered cap loads
// and takes the writes that bring it back under, and a read fails past
// twice the cap.
func TestSettings_ByteCap(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		s, db := replicaWith(t, d, TableLimits{Rows: 100, Bytes: 200}, slog.New(slog.DiscardHandler))
		long := strings.Repeat("x", 100)
		write(t, s, map[string]any{`upstreams["a"].url`: "https://a.example/" + long}) // 18 + 120 bytes
		_, err := s.Write(t.Context(), map[string]any{`upstreams["b"].url`: "https://b.example/" + long})
		assert.Equal(t, ReasonFull, refusal(t, err).Reason)
		write(t, s, map[string]any{`upstreams["a"].url`: "https://a.example/"}) // 18 + 20 bytes

		var logs bytes.Buffer
		narrow, _ := replicaWith(t, d, TableLimits{Rows: 100, Bytes: 30}, slog.New(slog.NewJSONHandler(&logs, nil)))
		assert.Equal(t, "https://a.example/", load(t, narrow).Upstreams["a"].URL, "over the cap, under twice it: loads")
		var entry struct{ Level, Msg string }
		require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
		assert.Equal(t, "WARN", entry.Level)
		assert.Equal(t, "settings table over its cap", entry.Msg)
		_, err = narrow.Write(t.Context(), map[string]any{"limits.max": 5})
		assert.Equal(t, ReasonFull, refusal(t, err).Reason)
		write(t, narrow, map[string]any{`upstreams["a"]`: nil})
		assert.Empty(t, rowsOf(t, db))

		_, err = db.Exec(`INSERT INTO settings (key, value) VALUES ('routes', $1)`, `"`+long+`"`)
		require.NoError(t, err)
		_, err = config.Load[AppConfig](t.Context(), narrow.Options()...)
		require.ErrorIs(t, err, config.ErrSource)
		assert.Contains(t, err.Error(), "more than 60 bytes")
		_, err = narrow.Write(t.Context(), map[string]any{"routes": nil})
		assert.ErrorContains(t, err, "more than 60 bytes")
	})
}

// One write naming a key and a key within it, under any spellings, is
// refused naming both: the load would merge the two, so the client sends
// the parent with the child folded in.
func TestSettings_AWriteNamingAKeyAndOneWithinIt(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, db := newReplica(t, b, 100)
		for name, tc := range map[string]struct {
			edits map[string]any
			keys  []string
		}{
			"a field and one within it": {map[string]any{"limits": map[string]any{"min": 2}, "limits.max": 300}, []string{"limits", "limits.max"}},
			"a list and an element's":   {map[string]any{"routes": []any{}, "routes[0].path": "/b"}, []string{"routes", "routes[0].path"}},
			"a deleted entry and its":   {map[string]any{`upstreams["a"]`: nil, `upstreams["a"].timeout`: "2s"}, []string{`upstreams["a"]`, `upstreams["a"].timeout`}},
			"other spellings":           {map[string]any{"LIMITS": map[string]any{"min": 2}, "limits.ceiling": 300}, []string{"limits", "limits.max"}},
			"three deep, one refusal":   {map[string]any{"routes": nil, "routes[0]": nil, "routes[0].path": "/b", "limits.min": 2}, []string{"routes", "routes[0]", "routes[0].path"}},
		} {
			_, err := s.Write(t.Context(), tc.edits)
			refused := refusal(t, err)
			assert.Equal(t, ReasonOverlap, refused.Reason, name)
			assert.Equal(t, tc.keys, refused.Keys, name)
		}
		assert.Empty(t, rowsOf(t, db))
	})
}

// On PostgreSQL a write waits on the lock row another replica's write holds,
// then reads what that write committed. It asks for READ COMMITTED, so it
// does so even on a session whose default is SERIALIZABLE, where the
// waiting update would fail to serialize instead.
func TestSettings_AWriteWaitsOnTheLockRowAndReadsWhatCommitted(t *testing.T) {
	d := postgresBackend.fresh(t)
	_, db := replica(t, d, 100)
	u, err := url.Parse(d.dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("default_transaction_isolation", "serializable")
	u.RawQuery = q.Encode()
	serializable := d
	serializable.dsn = u.String()
	second, _ := replica(t, serializable, 100)

	// The first replica's write, under way: it holds the lock row and has
	// stored a limits.max under which the second write's limits.min loads.
	first, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = first.Rollback() }()
	var pid int
	require.NoError(t, first.QueryRow("SELECT pg_backend_pid()").Scan(&pid))
	_, err = first.Exec("UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
	require.NoError(t, err)
	_, err = first.Exec(`INSERT INTO settings (key, value) VALUES ('limits.max', '300')`)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := second.Write(t.Context(), map[string]any{"limits.min": 200})
		done <- err
	}()
	assert.Eventually(t, func() bool {
		var waiting int
		err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&waiting)
		return err == nil && waiting == 1
	}, 5*time.Second, 10*time.Millisecond, "the second write waits on the lock row the first holds")
	require.NoError(t, first.Commit())
	require.NoError(t, <-done, "read after the first committed, limits.max 300 lets limits.min 200 load")
	assert.Equal(t, map[string]string{"limits.max": "300", "limits.min": "200"}, rowsOf(t, db))
}

// cutSchema holds the fields beneath which config spells keys no field
// describes: a map of interfaces, an interface, and a ",remain" field.
type cutSchema struct {
	Labels map[string]any `koanf:"labels"`
	Plugin any            `koanf:"plugin"`
	Tuning struct {
		Workers int            `koanf:"workers"`
		Extra   map[string]any `koanf:",remain"`
	} `koanf:"tuning"`
}

// A key beneath an interface or one a ",remain" field collects, written as
// CanonicalKey spells it, is cut out of the stored row above it, under the
// spelling the row's value holds it by: a name holding a dot among them.
func TestApply_CutsBeneathInterfacesAndRemainFields(t *testing.T) {
	stored := map[string]any{
		"labels": map[string]any{"team": map[string]any{"owner": "a", "pager": "b"}},
		"plugin": map[string]any{"name": "x", "opts": map[string]any{"depth": json.Number("2"), "a.b": true}},
		"tuning": map[string]any{"workers": json.Number("4"), "cache_size": json.Number("10")},
	}
	for name, tc := range map[string]struct {
		key       string
		value     any
		rewritten map[string]any
	}{
		"beneath a map of interfaces": {`labels["team"]["owner"]`, nil, map[string]any{
			"labels": map[string]any{"team": map[string]any{"pager": "b"}}}},
		"beneath an interface": {`plugin["opts"].depth`, nil, map[string]any{
			"plugin": map[string]any{"name": "x", "opts": map[string]any{"a.b": true}}}},
		"a dotted name beneath an interface": {`plugin.opts["a.b"]`, nil, map[string]any{
			"plugin": map[string]any{"name": "x", "opts": map[string]any{"depth": json.Number("2")}}}},
		"collected by a remain field, set": {`tuning["cache_size"]`, 20, map[string]any{
			"tuning": map[string]any{"workers": json.Number("4")}}},
	} {
		key, err := config.CanonicalKey[cutSchema](tc.key)
		require.NoError(t, err, name)
		c, err := apply[cutSchema](stored, map[string]any{key: tc.value}, nil)
		require.NoError(t, err, name)
		assert.Equal(t, tc.rewritten, c.rewritten, name)
		if tc.value != nil {
			assert.Equal(t, tc.value, c.table[key], name)
		}
	}
	assert.Equal(t, map[string]any{"owner": "a", "pager": "b"}, stored["labels"].(map[string]any)["team"], "stored is not changed")
}
