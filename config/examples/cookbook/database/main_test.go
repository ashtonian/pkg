//go:build unit

package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/config"
)

func TestSettingsTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "settings.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO settings VALUES ('rate_limit', '200'), ('features.export', 'true');`)
	require.NoError(t, err)

	r, err := newReloader(t.Context(), db, &BootConfig{QueryTimeout: time.Second, PollInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	assert.Equal(t, 200, r.Config().RateLimit)
	assert.Equal(t, map[string]bool{"export": true}, r.Config().Features)

	_, err = db.Exec(`UPDATE settings SET value = '50' WHERE key = 'rate_limit'`)
	require.NoError(t, err)
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 50, r.Config().RateLimit)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, "rate_limit", res.Changes[0].Key)

	// A row naming no key, or a value out of range, keeps the last valid
	// settings.
	_, err = db.Exec(`INSERT INTO settings VALUES ('rate_limt', '10')`)
	require.NoError(t, err)
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrValidation)
	assert.Contains(t, err.Error(), "rate_limt")
	_, err = db.Exec(`DELETE FROM settings WHERE key = 'rate_limt'; UPDATE settings SET value = '0' WHERE key = 'rate_limit'`)
	require.NoError(t, err)
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrValidation)
	assert.Equal(t, 50, r.Config().RateLimit)
}
