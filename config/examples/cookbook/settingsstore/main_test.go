package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/config"
)

func TestSettingsStore(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "settings.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	t.Chdir(t.TempDir()) // no config.yaml: the table and defaults alone

	settings := &Settings{db: db, timeout: time.Second}
	r, err := newReloader(t.Context(), settings, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	write := func(edits map[string]any) error {
		if err := settings.Write(t.Context(), r, edits); err != nil {
			return err
		}
		_, err := r.Reload(t.Context())
		return err
	}

	// Validate, persist, reload: limits each invalid alone are valid
	// together, and keys holding a dot or an index load back as written.
	require.NoError(t, write(map[string]any{"limits.min": 150, "limits.max": 200}))
	assert.Equal(t, Limits{Min: 150, Max: 200}, r.Config().Limits)
	require.NoError(t, write(map[string]any{
		`upstreams["api.internal"]`: map[string]any{"url": "https://api.internal.example"},
		"routes[0]":                 map[string]any{"path": "/v1", "upstream": "api.internal"},
	}))
	require.NoError(t, write(map[string]any{`upstreams["api.internal"].timeout`: "2s", "routes[1].path": "/v2", "routes[1].upstream": "api.internal"}))
	assert.Equal(t, map[string]Upstream{"api.internal": {URL: "https://api.internal.example", Timeout: 2 * time.Second}}, r.Config().Upstreams)
	assert.Equal(t, []Route{{"/v1", "api.internal"}, {"/v2", "api.internal"}}, r.Config().Routes)
	var stored string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key = 'upstreams["api.internal"].timeout'`).Scan(&stored))
	assert.Equal(t, `"2s"`, stored)

	// A batch the service would refuse is never stored: a cross-field rule,
	// a key spelled two ways across writes, an unknown key.
	rows := countRows(t, db)
	for _, edits := range []map[string]any{
		{"limits.min": 300},
		{"routes[0].upstream": "missing"},
		{"LIMITS.Max": 250},
		{"limits.maxx": 250},
		{"routes[5].path": "/v5"},
	} {
		err := settings.Write(t.Context(), r, edits)
		require.Error(t, err)
		assert.True(t, errorsAreKeyed(err), err.Error())
	}
	assert.Equal(t, rows, countRows(t, db))
	assert.Equal(t, Limits{Min: 150, Max: 200}, r.Config().Limits)

	// Over HTTP a refused batch is a 422 naming keys, never values.
	rec := httptest.NewRecorder()
	settingsHandler(settings, r).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"limits.min": "FAKE_SECRET_VALUE"}`)))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "limits.min")
	assert.NotContains(t, rec.Body.String(), "FAKE_SECRET_VALUE")
	rec = httptest.NewRecorder()
	settingsHandler(settings, r).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"limits.max": 300}`)))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func errorsAreKeyed(err error) bool {
	return errors.Is(err, config.ErrKey) || errors.Is(err, config.ErrValidation)
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM settings`).Scan(&n))
	return n
}
