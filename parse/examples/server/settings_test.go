//go:build unit

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/ashtonian/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettings_ExplicitFlagsOverrideEnvironmentWithoutMaskingIt(t *testing.T) {
	t.Setenv("PARSE_SERVER_ADDR", "127.0.0.1:9090")
	t.Setenv("PARSE_SERVER_SHUTDOWN_GRACE", "30s")
	settings, err := loadSettings(t.Context(), []string{"--addr", "127.0.0.1:9091"})
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

func TestSettings_RejectsAnUnboundedShutdown(t *testing.T) {
	_, err := loadSettings(t.Context(), []string{"--shutdown-grace=0s"})
	require.ErrorIs(t, err, config.ErrValidation)
}

func TestSettings_RejectsAnUnknownLogLevel(t *testing.T) {
	_, err := loadSettings(t.Context(), []string{"--log-level=verbose"})
	require.ErrorIs(t, err, config.ErrValidation)
}

// The startup entry is written at any level, with the service, its version,
// the effective level and the settings.
func TestLogStart_WritesTheStartupEntryPastTheLevel(t *testing.T) {
	settings, err := loadSettings(t.Context(), []string{"--request-timeout=7s"})
	require.NoError(t, err)
	var logs bytes.Buffer
	level := new(slog.LevelVar)
	level.Set(slog.LevelError)
	logStart(t.Context(), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level})), level.Level(), settings)

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
	assert.Equal(t, "ERROR", entry.LogLevel)
	assert.Equal(t, "7s", entry.Config["request_timeout"])
	assert.Equal(t, "127.0.0.1:8080", entry.Config["addr"])
}
