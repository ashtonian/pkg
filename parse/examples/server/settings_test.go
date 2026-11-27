//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsFrom loads the settings args name once, with no reloads.
func settingsFrom(t *testing.T, args ...string) (*serverSettings, error) {
	t.Helper()
	r, err := loadSettings(t.Context(), args)
	if err != nil {
		return nil, err
	}
	require.NoError(t, r.Stop(t.Context()))
	return r.Config(), nil
}

func TestSettings_ExplicitFlagsOverrideEnvironmentWithoutMaskingIt(t *testing.T) {
	t.Setenv("PARSE_SERVER_ADDR", "127.0.0.1:9090")
	t.Setenv("PARSE_SERVER_SHUTDOWN_GRACE", "30s")
	settings, err := settingsFrom(t, "--addr", "127.0.0.1:9091")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9091", settings.Addr)
	assert.Equal(t, 30*time.Second, settings.ShutdownGrace)
	assert.Equal(t, "warn", settings.LogLevel)
	assert.Equal(t, 5*time.Second, settings.RequestTimeout)
	assert.Equal(t, 5*time.Second, settings.ReadHeaderTimeout)
	assert.Equal(t, 10*time.Second, settings.ReadTimeout)
	assert.Equal(t, 10*time.Second, settings.WriteTimeout)
	assert.Equal(t, 60*time.Second, settings.IdleTimeout)
}

// With no settings the database is in-memory SQLite; the environment
// selects PostgreSQL and sets its pool, or the memory provider and its
// bound.
func TestSettings_DatabaseDefaultsToInMemorySQLite(t *testing.T) {
	settings, err := settingsFrom(t)
	require.NoError(t, err)
	assert.Equal(t, "sqlite", settings.DB.Provider)
	assert.Equal(t, ":memory:", settings.DB.SQLite.DSN)
	assert.Equal(t, 10*time.Second, settings.DB.SQLite.SettleTimeout)
	assert.Equal(t, 1, settings.DB.SQLite.MaxOpenConns)
	assert.Equal(t, 5*time.Second, settings.DB.SQLite.BusyTimeout)

	t.Setenv("PARSE_SERVER_DB_PROVIDER", "postgres")
	t.Setenv("PARSE_SERVER_DB_POSTGRES_DSN", "postgres://app@db.example.com/app")
	t.Setenv("PARSE_SERVER_DB_POSTGRES_MAX_CONNS", "4")
	t.Setenv("PARSE_SERVER_DB_POSTGRES_COLLATION", "C")
	settings, err = settingsFrom(t)
	require.NoError(t, err)
	assert.Equal(t, "postgres", settings.DB.Provider)
	assert.Equal(t, "postgres://app@db.example.com/app", settings.DB.Postgres.DSN)
	assert.Equal(t, 10*time.Second, settings.DB.Postgres.SettleTimeout)
	assert.Equal(t, int32(4), settings.DB.Postgres.MaxConns)
	assert.Equal(t, "C", settings.DB.Postgres.Collation)
	assert.Equal(t, time.Hour, settings.DB.Postgres.MaxConnLifetime)
	assert.Equal(t, 100_000, settings.DB.Memory.MaxRows)

	t.Setenv("PARSE_SERVER_DB_PROVIDER", "memory")
	t.Setenv("PARSE_SERVER_DB_MEMORY_MAX_ROWS", "500")
	settings, err = settingsFrom(t)
	require.NoError(t, err)
	assert.Equal(t, "memory", settings.DB.Provider)
	assert.Equal(t, 500, settings.DB.Memory.MaxRows)
}

func TestSettings_RejectsAMemoryProviderHoldingNoRows(t *testing.T) {
	t.Setenv("PARSE_SERVER_DB_PROVIDER", "memory")
	t.Setenv("PARSE_SERVER_DB_MEMORY_MAX_ROWS", "0")
	_, err := settingsFrom(t)
	require.ErrorIs(t, err, config.ErrValidation)
}

// The database is set by the environment or the settings file, never by a
// flag, so a DSN's password stays out of the process list.
func TestSettings_TheDatabaseHasNoFlags(t *testing.T) {
	_, err := settingsFrom(t, "--db-postgres-dsn=postgres://app@db.example.com/app")
	require.ErrorContains(t, err, "unknown flag --db-postgres-dsn")
}

func TestSettings_RejectsAnUnknownProvider(t *testing.T) {
	t.Setenv("PARSE_SERVER_DB_PROVIDER", "mysql")
	_, err := settingsFrom(t)
	require.ErrorIs(t, err, config.ErrValidation)
}

func TestSettings_RejectsAnUnboundedShutdown(t *testing.T) {
	_, err := settingsFrom(t, "--shutdown-grace=0s")
	require.ErrorIs(t, err, config.ErrValidation)
}

func TestSettings_RejectsAnUnknownLogLevel(t *testing.T) {
	_, err := settingsFrom(t, "--log-level=verbose")
	require.ErrorIs(t, err, config.ErrValidation)
}

// The settings file sits beneath the environment and the flags.
func TestSettings_FileSitsBeneathEnvironmentAndFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	require.NoError(t, os.WriteFile(path, []byte("addr: 127.0.0.1:9092\nrequest_timeout: 3s\nlog_level: debug\n"), 0o600))
	t.Setenv("PARSE_SERVER_REQUEST_TIMEOUT", "4s")
	settings, err := settingsFrom(t, "--config", path, "--log-level=error")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9092", settings.Addr)
	assert.Equal(t, 4*time.Second, settings.RequestTimeout)
	assert.Equal(t, "error", settings.LogLevel)
}

// A reload applies the file's log_level to the running logger, and says so
// at any level.
func TestSettings_AReloadChangesTheLogLevelLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	require.NoError(t, os.WriteFile(path, []byte("log_level: warn\n"), 0o600))
	level := new(slog.LevelVar)
	var logs bytes.Buffer
	log := newLogger(&logs, level)
	r, err := loadSettings(t.Context(), []string{"--config", path}, levelApplier(level, log))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, r.Stop(context.WithoutCancel(t.Context()))) })
	level.Set(slog.LevelWarn)

	for _, tt := range []struct {
		setting string
		want    slog.Level
		logged  string
	}{
		{"trace", levelTrace, "TRACE"},
		{"error", slog.LevelError, "ERROR"},
	} {
		logs.Reset()
		require.NoError(t, os.WriteFile(path, []byte("log_level: "+tt.setting+"\n"), 0o600))
		_, err = r.Reload(t.Context())
		require.NoError(t, err)
		assert.Equal(t, tt.want, level.Level())
		var entry struct {
			Level, Msg string
			LogLevel   string `json:"log_level"`
		}
		require.NoError(t, json.Unmarshal(logs.Bytes(), &entry), logs.String())
		assert.Equal(t, "log level changed", entry.Msg)
		assert.Equal(t, "INFO", entry.Level)
		assert.Equal(t, tt.logged, entry.LogLevel)
	}
}

// The startup entry is written at any level, with the service, its version,
// the effective level and the settings.
func TestLogStart_WritesTheStartupEntryPastTheLevel(t *testing.T) {
	t.Setenv("PARSE_SERVER_DB_SQLITE_DSN", "file:products.db?mode=rwc&password-like=hunter2")
	t.Setenv("PARSE_SERVER_DB_POSTGRES_DSN", "postgres://app:hunter3@db.example.com/app")
	settings, err := settingsFrom(t, "--request-timeout=7s")
	require.NoError(t, err)
	var logs bytes.Buffer
	level := new(slog.LevelVar)
	level.Set(slog.LevelError)
	logStart(t.Context(), newLogger(&logs, level), levelTrace, settings)

	var entry struct {
		Level, Msg, Service, Version string
		LogLevel                     string         `json:"log_level"`
		Config                       map[string]any `json:"config"`
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry), logs.String())
	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "starting", entry.Msg)
	assert.Equal(t, serviceName, entry.Service)
	assert.NotEmpty(t, entry.Version)
	assert.Equal(t, "TRACE", entry.LogLevel)
	assert.Equal(t, "7s", entry.Config["request_timeout"])
	assert.Equal(t, "127.0.0.1:8080", entry.Config["addr"])
	db, ok := entry.Config["db"].(map[string]any)
	require.True(t, ok, "the db settings are in the entry")
	assert.Equal(t, "sqlite", db["provider"])
	assert.NotContains(t, logs.String(), "hunter2", "the SQLite DSN is redacted")
	assert.NotContains(t, logs.String(), "hunter3", "the PostgreSQL DSN is redacted")
}
