//go:build unit

package config_test

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/ashtonian/pkg/config"
)

// ExampleGetConfig_comprehensive demonstrates loading a realistic
// multi-layered configuration that exercises every supported type:
// nested structs, slices, maps, pointers, durations, booleans,
// struct-tag defaults, programmatic defaults, env var overrides
// (including fields whose koanf tags contain underscores), and
// sensitive-field redaction via PrintRedacted.
func ExampleGetConfig_comprehensive() {
	// --- Define a realistic config struct hierarchy ---

	type JWTConfig struct {
		SignKey        string   `koanf:"sign_key" sensitive:"true"`
		AllowedIssuers []string `koanf:"allowed_issuers" default:"auth.local"`
	}

	type AuthConfig struct {
		JWT JWTConfig `koanf:"jwt"`
	}

	type DBConfig struct {
		Host     string `koanf:"host" default:"localhost"`
		Port     int    `koanf:"port" default:"5432"`
		Name     string `koanf:"name" default:"mydb"`
		MaxConns int    `koanf:"max_conns" default:"25"`
		Password string `koanf:"password" sensitive:"true"`
	}

	type PoolEntry struct {
		Name string `koanf:"name"`
		Size int    `koanf:"size"`
	}

	type AppConfig struct {
		Host    string            `koanf:"host" default:"0.0.0.0"`
		Port    int               `koanf:"port" default:"8080"`
		Debug   bool              `koanf:"debug" default:"false"`
		Tags    []string          `koanf:"tags" default:"web,api"`
		Labels  map[string]string `koanf:"labels"`
		MaxBody *int              `koanf:"max_body"`
		DB      DBConfig          `koanf:"db"`
		Auth    AuthConfig        `koanf:"auth"`
		Pools   []PoolEntry       `koanf:"pools"`
	}

	// --- Set env vars to override selected fields ---
	// Env vars take precedence over defaults and config files.
	// Underscored koanf tags (max_conns, sign_key, allowed_issuers)
	// resolve correctly thanks to struct-aware env mapping.

	os.Setenv("APP_PORT", "9090")
	os.Setenv("APP_TAGS", "web,api,grpc")
	os.Setenv("APP_DB_MAX_CONNS", "50")
	os.Setenv("APP_DB_PASSWORD", "s3cret")
	os.Setenv("APP_AUTH_JWT_SIGN_KEY", "hmac-key-256")
	os.Setenv("APP_AUTH_JWT_ALLOWED_ISSUERS", "auth.prod,auth.staging")
	defer func() {
		os.Unsetenv("APP_PORT")
		os.Unsetenv("APP_TAGS")
		os.Unsetenv("APP_DB_MAX_CONNS")
		os.Unsetenv("APP_DB_PASSWORD")
		os.Unsetenv("APP_AUTH_JWT_SIGN_KEY")
		os.Unsetenv("APP_AUTH_JWT_ALLOWED_ISSUERS")
	}()

	// --- Load config ---
	// Source precedence: env > programmatic defaults > struct-tag defaults.

	cfg, err := config.GetConfig[AppConfig](
		config.WithEnvPrefix("APP"),
		config.WithDefaults(map[string]any{
			"debug":      true,                        // overrides default:"false"
			"labels":     map[string]any{"env": "dev"}, // map via programmatic default
			"db.name":    "proddb",                     // dot-path overrides default:"mydb"
		}),
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// --- Inspect values ---

	fmt.Printf("host=%s port=%d debug=%v\n", cfg.Host, cfg.Port, cfg.Debug)
	fmt.Printf("tags=%v\n", cfg.Tags)
	fmt.Printf("labels=%v\n", cfg.Labels)
	fmt.Printf("max_body set=%v\n", cfg.MaxBody != nil) // nil pointer = unset
	fmt.Printf("db=%s:%d/%s max_conns=%d\n", cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.MaxConns)
	fmt.Printf("jwt_issuers=%v\n", cfg.Auth.JWT.AllowedIssuers)

	// --- PrintRedacted: sensitive fields are masked ---

	var buf bytes.Buffer
	config.PrintRedacted(cfg, config.WithWriter(&buf), config.WithFormat("env"))
	// Scan output for the redacted fields.
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		s := string(line)
		if len(s) == 0 {
			continue
		}
		// Print only a few interesting lines to keep output stable.
		switch {
		case contains(s, "DB_PASSWORD"):
			fmt.Println(s)
		case contains(s, "JWT_SIGN_KEY"):
			fmt.Println(s)
		}
	}

	// Output:
	// host=0.0.0.0 port=9090 debug=true
	// tags=[web api grpc]
	// labels=map[env:dev]
	// max_body set=false
	// db=localhost:5432/proddb max_conns=50
	// jwt_issuers=[auth.prod auth.staging]
	// export DB_PASSWORD=[REDACTED]
	// export AUTH_JWT_SIGN_KEY=[REDACTED]
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}

// ExampleNewReloader demonstrates creating a reloader with initial config
// from struct tag defaults, then performing a manual reload.
func ExampleNewReloader() {
	type AppConfig struct {
		Host string `koanf:"host" default:"localhost"`
		Port int    `koanf:"port" default:"8080"`
	}

	reloader, err := config.NewReloader[AppConfig](
		nil, // no reload options
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	cfg := reloader.Config()
	fmt.Printf("host=%s port=%d\n", cfg.Host, cfg.Port)

	newCfg, err := reloader.Reload(context.Background())
	if err != nil {
		fmt.Println("reload error:", err)
		return
	}
	fmt.Printf("reloaded: host=%s port=%d\n", newCfg.Host, newCfg.Port)

	// Output:
	// host=localhost port=8080
	// reloaded: host=localhost port=8080
}

// ExampleNewReloader_withCallback demonstrates using an onChange callback
// that fires after each successful reload.
func ExampleNewReloader_withCallback() {
	type Cfg struct {
		Level string `koanf:"level" default:"info"`
	}

	var lastLevel string
	reloader, err := config.NewReloader[Cfg](
		[]config.ReloadOption[Cfg]{
			config.WithOnChange(func(old, new *Cfg) {
				lastLevel = new.Level
			}),
		},
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	cfg := reloader.Config()
	fmt.Printf("level=%s\n", cfg.Level)

	reloader.Reload(context.Background())
	fmt.Printf("callback_level=%s\n", lastLevel)

	// Output:
	// level=info
	// callback_level=info
}
