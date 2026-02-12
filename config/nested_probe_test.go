//go:build unit

package config

import (
	"testing"
)

func TestProbe_NestedEnvUnderscore(t *testing.T) {
	// koanf tag has underscore: "max_conns"
	// env var APP_DB_MAX_CONNS -> transform strips prefix, lowercases, replaces _ with .
	// Result: "db.max.conns" but we NEED "db.max_conns"
	t.Setenv("APP_DB_MAX_CONNS", "50")
	t.Setenv("APP_DB_HOST", "localhost")

	type dbCfg struct {
		Host     string `koanf:"host"`
		MaxConns int    `koanf:"max_conns"`
	}
	type cfg struct {
		DB dbCfg `koanf:"db"`
	}

	c, err := GetConfig[cfg](WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("DB.Host=%q DB.MaxConns=%d", c.DB.Host, c.DB.MaxConns)
	if c.DB.Host != "localhost" {
		t.Errorf("DB.Host = %q, want localhost", c.DB.Host)
	}
	if c.DB.MaxConns != 50 {
		t.Errorf("DB.MaxConns = %d, want 50 (underscore in koanf tag breaks env mapping)", c.DB.MaxConns)
	}
}

func TestProbe_NestedEnvDeep(t *testing.T) {
	// 3-level nesting: app.server.tls.enabled
	t.Setenv("APP_SERVER_TLS_ENABLED", "true")
	t.Setenv("APP_SERVER_TLS_CERT", "/path/cert")

	type tlsCfg struct {
		Enabled bool   `koanf:"enabled"`
		Cert    string `koanf:"cert"`
	}
	type serverCfg struct {
		TLS tlsCfg `koanf:"tls"`
	}
	type cfg struct {
		Server serverCfg `koanf:"server"`
	}

	c, err := GetConfig[cfg](WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Server.TLS.Enabled=%v Server.TLS.Cert=%q", c.Server.TLS.Enabled, c.Server.TLS.Cert)
	if !c.Server.TLS.Enabled {
		t.Error("Server.TLS.Enabled = false, want true")
	}
	if c.Server.TLS.Cert != "/path/cert" {
		t.Errorf("Server.TLS.Cert = %q, want /path/cert", c.Server.TLS.Cert)
	}
}
