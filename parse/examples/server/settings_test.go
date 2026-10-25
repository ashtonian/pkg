//go:build unit

package main

import (
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
