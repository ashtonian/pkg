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
)

// replica opens the settings table in the SQLite database at path as one
// replica of the service does, with a pool and Settings of its own. A
// connection waits up to busy_timeout for another replica's write.
func replica(t *testing.T, path string, maxRows int) (*Settings, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, migrate(t.Context(), db, time.Second))
	file := filepath.Join(filepath.Dir(path), "config.yaml") // absent: the table over defaults
	return NewSettings(db, appSources(file, slog.New(slog.DiscardHandler)), 10*time.Second, maxRows), db
}

func newReplica(t *testing.T, maxRows int) (*Settings, *sql.DB) {
	return replica(t, filepath.Join(t.TempDir(), "settings.db"), maxRows)
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
	s, db := newReplica(t, 100)
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
}

// Keys are stored as CanonicalKey spells them, so every spelling of a key
// writes one row: a former name, any letter case, and a row another writer
// stored under another spelling.
func TestSettings_KeysAreStoredCanonically(t *testing.T) {
	s, db := newReplica(t, 100)
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
}

// A key's write replaces the rows stored beneath it, and a key set to null
// deletes its row and theirs.
func TestSettings_AWriteReplacesTheRowsBeneathIt(t *testing.T) {
	s, db := newReplica(t, 100)
	write(t, s, map[string]any{
		`upstreams["a"]`:         map[string]any{"url": "https://a.example"},
		`upstreams["a"].timeout`: "2s",
		"routes[0]":              map[string]any{"path": "/v1", "upstream": "a"},
		"routes[1].path":         "/v2",
		"routes[1].upstream":     "a",
		"limits.min":             5,
		"limits.max":             300,
	})

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
}

// Writes from every replica take turns: two writes each valid alone but
// not together never both commit, whichever replica makes them.
func TestSettings_WritersOnEveryReplicaTakeTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	a, _ := replica(t, path, 100)
	b, _ := replica(t, path, 100)
	for round := range 20 {
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, w := range []struct {
			s     *Settings
			edits map[string]any
		}{{a, map[string]any{"limits.min": 90}}, {b, map[string]any{"limits.max": 50}}} {
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
		load(t, a)
		write(t, a, map[string]any{"limits.min": nil, "limits.max": nil})
	}
}

// The table holds at most its row cap: a write that would grow it past the
// cap is refused, one that replaces rows is not, and a table grown past it
// by another writer fails the read rather than load unbounded.
func TestSettings_RowCap(t *testing.T) {
	s, db := newReplica(t, 3)
	write(t, s, map[string]any{"limits.min": 2, "limits.max": 50, "limits.bytes": 10})
	_, err := s.Write(t.Context(), map[string]any{`upstreams["a"]`: map[string]any{"url": "https://a.example"}})
	refused := refusal(t, err)
	assert.Equal(t, ReasonFull, refused.Reason)
	assert.Equal(t, []string{`upstreams["a"]`}, refused.Keys)
	write(t, s, map[string]any{"limits.max": 60})
	write(t, s, map[string]any{"limits": map[string]any{"min": 2, "max": 60, "bytes": 10}})
	assert.Len(t, rowsOf(t, db), 1)

	for _, key := range []string{"limits.min", "limits.max", "routes"} {
		_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, '1')`, key)
		require.NoError(t, err)
	}
	_, err = config.Load[AppConfig](t.Context(), s.Options()...)
	require.ErrorIs(t, err, config.ErrSource)
	assert.Contains(t, err.Error(), "more than 3 rows")
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
	s, db := newReplica(t, 100)
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
}

// A store that fails is a 503, logged at Error with its cause: a stored
// table that no longer loads, which no write is at fault for, and a
// database that is gone.
func TestSettingsHandler_StoreFailures(t *testing.T) {
	s, db := newReplica(t, 100)
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

	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('limits.bogus', '1')`)
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
	s := NewSettings(db, appSources(filepath.Join(dir, "config.yaml"), slog.New(slog.DiscardHandler)), 10*time.Second, 100)
	write(t, s, map[string]any{"limits.max": 300})
	write(t, s, map[string]any{"limits.max": nil})
	assert.Equal(t, []sql.IsolationLevel{sql.LevelReadCommitted, sql.LevelReadCommitted}, levels.levels)
}

// When the settings fail to load without the write, the 503 says so and
// names the sources beneath the table to check, not the table alone.
func TestSettingsHandler_SettingsThatFailWithoutTheWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	s, _ := replica(t, path, 100)
	file := filepath.Join(filepath.Dir(path), "config.yaml")
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
}

// A write that times out waiting for its turn, behind this replica's own
// write or another replica's lock row, is contention: a 503 with
// Retry-After, logged at Warn. Error is for failures.
func TestSettingsHandler_ContentionIsA503ToRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, migrate(t.Context(), db, time.Second))
	s := NewSettings(db, appSources(filepath.Join(filepath.Dir(path), "config.yaml"), slog.New(slog.DiscardHandler)), 200*time.Millisecond, 100)
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
		other, _ := replica(t, path, 100)
		tx, err := other.db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.Exec("UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
		require.NoError(t, err)
		contended(t)
	})
}

// sqliteTables are the tables of the SQLite database at path.
func sqliteTables(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.db")
	environ := func() []string {
		return []string{"SETTINGS_DATABASE_URL=" + path, "SETTINGS_CONFIG_FILE=" + filepath.Join(dir, "config.yaml")}
	}
	open := func(url string) (*sql.DB, error) { return sql.Open("sqlite", url) }
	command := func(args ...string) error {
		return run(t.Context(), args, environ, open, slog.New(slog.DiscardHandler), new(slog.LevelVar))
	}

	err := command()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "settingsstore migrate")
	assert.Empty(t, sqliteTables(t, path))

	require.NoError(t, command("migrate"))
	require.NoError(t, command("migrate"))
	assert.Equal(t, []string{"settings", "settings_lock"}, sqliteTables(t, path))
	db, err := open(path)
	require.NoError(t, err)
	defer db.Close()
	assert.NoError(t, checkSchema(t.Context(), db, time.Second))

	assert.ErrorContains(t, command("serve", "now"), "usage")
}

// A row whose key the schema no longer reads, its field removed by a
// release, is deleted by a null write of its key, with the rows beneath it.
// Only null may name such a key, only to delete such rows, and only where
// one is stored: a spelling the schema refuses never deletes a row it reads.
func TestSettings_ANullDeletesARowTheSchemaNoLongerReads(t *testing.T) {
	s, db := newReplica(t, 100)
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
}
