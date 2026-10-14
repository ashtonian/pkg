package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/ashtonian/pkg/config"
)

// tempFile writes content to a file in a new temporary directory and
// returns its path and a cleanup function.
func tempFile(name, content string) (string, func()) {
	dir, err := os.MkdirTemp("", "config-example")
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		log.Fatal(err)
	}
	return path, func() { _ = os.RemoveAll(dir) }
}

// Example mirrors the README Quick Start: struct-tag defaults, a file and
// APP_* environment variables, then the result printed with the password
// redacted. The example supplies its own file and environment, so its output
// does not depend on where it runs.
func Example() {
	type DBConfig struct {
		Host     string `koanf:"host" default:"localhost"`
		MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1"`
		Password string `koanf:"password" default:"hunter2"`
	}
	type AppConfig struct {
		Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535"`
		Timeout time.Duration `koanf:"timeout" default:"30s"`
		DB      DBConfig      `koanf:"db"`
	}

	file, cleanup := tempFile("config.yaml", "db:\n  host: db.internal\n")
	defer cleanup()

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile(file),       // optional: skipped when absent
		config.WithEnvPrefix("APP"), // APP_PORT, APP_DB_MAX_CONNS, ...
		config.WithEnviron(func() []string { return []string{"APP_DB_MAX_CONNS=50"} }),
	)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	if err := config.PrintRedacted(cfg, config.WithWriter(os.Stdout)); err != nil {
		fmt.Println("print:", err)
	}

	// Output:
	// FIELD         VALUE        TYPE
	// -------------------------------------------
	// port          8080         int
	// timeout       30s          time.Duration
	// db.host       db.internal  string
	// db.max_conns  50           int
	// db.password   [REDACTED]   string
}

// ExampleLoad_layers shows every source at its layer: a struct-tag default,
// a programmatic default, a file, an environment variable and an override,
// each winning over the ones below it.
func ExampleLoad_layers() {
	type AppConfig struct {
		Region   string   `koanf:"region" default:"us-east-1"`
		LogLevel string   `koanf:"log_level" default:"info"`
		Replicas int      `koanf:"replicas" default:"1"`
		Tags     []string `koanf:"tags"`
		Debug    bool     `koanf:"debug"`
	}

	file, cleanup := tempFile("config.yaml", "replicas: 3\ntags: [web, api]\n")
	defer cleanup()

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithDefaults(map[string]any{"log_level": "warn"}),
		config.WithFile(file),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_REPLICAS=5", "APP_TAGS=web,api,grpc"} }),
		config.WithOverrideProvider(overrides{"debug": true}, nil),
	)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	fmt.Printf("region=%s log_level=%s replicas=%d tags=%v debug=%v\n",
		cfg.Region, cfg.LogLevel, cfg.Replicas, cfg.Tags, cfg.Debug)

	// Output:
	// region=us-east-1 log_level=warn replicas=5 tags=[web api grpc] debug=true
}

// overrides is a koanf.Provider over a fixed map — the shape kongprovider
// gives CLI flags.
type overrides map[string]any

func (o overrides) Read() (map[string]any, error) { return o, nil }
func (overrides) ReadBytes() ([]byte, error)      { return nil, errors.New("overrides: use Read") }

// ExampleLoad_errors shows the categories a failed load reports, and that a
// value meant for a sensitive field is never quoted.
func ExampleLoad_errors() {
	type AppConfig struct {
		Port     int       `koanf:"port" validate:"min=1"`
		Rotation time.Time `koanf:"rotation" sensitive:"true"`
	}
	ctx := context.Background()

	_, err := config.Load[AppConfig](ctx, config.WithDefaults(map[string]any{"port": 0}))
	fmt.Println(errors.Is(err, config.ErrValidation), err)

	_, err = config.Load[AppConfig](ctx, config.WithRequiredFile("/nonexistent/config.yaml"))
	fmt.Println(errors.Is(err, config.ErrFileRead), errors.Is(err, os.ErrNotExist))

	_, err = config.Load[AppConfig](ctx, config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_ROTATION=s3cr3t-not-a-time"} }))
	fmt.Println(errors.Is(err, config.ErrValidation), err)

	file, cleanup := tempFile("config.yaml", "port: 8080\nrotation: [s3cr3t\n")
	defer cleanup()
	_, err = config.Load[AppConfig](ctx, config.WithFile(file))
	var pe *config.ParseError
	fmt.Println(errors.Is(err, config.ErrFileParse), errors.As(err, &pe), pe.Line > 0)

	// Output:
	// true validating config: configuration validation failed: port(min=1)
	// true true
	// true unmarshaling config: configuration validation failed: decoding failed due to the following error(s):
	//
	// 'rotation' cannot be decoded into time.Time (the value is sensitive and not shown)
	// true true true
}

// ExampleWithStrictKeys shows a typo in a config file failing the load
// instead of being ignored.
func ExampleWithStrictKeys() {
	type AppConfig struct {
		Server struct {
			Port int `koanf:"port" default:"8080"`
		} `koanf:"server"`
	}
	file, cleanup := tempFile("config.yaml", "server:\n  prot: 9000\n")
	defer cleanup()

	_, err := config.Load[AppConfig](context.Background(), config.WithFile(file), config.WithStrictKeys())
	fmt.Println(errors.Is(err, config.ErrValidation))

	// Output:
	// true
}

// ExamplePrintUsage prints what an operator can set, with the environment
// variable for each key; a sensitive default stays hidden.
func ExamplePrintUsage() {
	type AppConfig struct {
		Port     int           `koanf:"port" default:"8080" desc:"HTTP listen port"`
		Timeout  time.Duration `koanf:"timeout" default:"30s" desc:"Request timeout"`
		Name     string        `koanf:"name" validate:"required" desc:"Service name"`
		Password string        `koanf:"password" default:"hunter2"`
	}
	if err := config.PrintUsage[AppConfig]("APP", config.WithWriter(os.Stdout)); err != nil {
		fmt.Println(err)
	}

	// Output:
	// KEY       ENV           TYPE           DEFAULT     DESCRIPTION
	// port      APP_PORT      int            8080        HTTP listen port
	// timeout   APP_TIMEOUT   time.Duration  30s         Request timeout
	// name      APP_NAME      string         (required)  Service name
	// password  APP_PASSWORD  string         [REDACTED]
}

// ExampleNewReloader reloads a file: a valid edit is swapped in, an invalid
// one is refused and the previous config stays live.
func ExampleNewReloader() {
	type AppConfig struct {
		Rate int `koanf:"rate" validate:"min=1"`
	}
	file, cleanup := tempFile("config.yaml", "rate: 10\n")
	defer cleanup()
	ctx := context.Background()

	reloader, err := config.NewReloader[AppConfig](ctx,
		[]config.Option{config.WithFile(file)},
		config.WithOnReloadSuccess(func(old, new *AppConfig) {
			fmt.Printf("rate %d -> %d\n", old.Rate, new.Rate)
		}),
	)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	defer func() { _ = reloader.Stop(ctx) }()

	_ = os.WriteFile(file, []byte("rate: 20\n"), 0o600)
	if _, err := reloader.Reload(ctx); err != nil {
		fmt.Println("reload:", err)
	}

	_ = os.WriteFile(file, []byte("rate: 0\n"), 0o600)
	_, err = reloader.Reload(ctx)
	fmt.Println(errors.Is(err, config.ErrReload), errors.Is(err, config.ErrValidation))
	fmt.Println("live rate:", reloader.Config().Rate)

	// Output:
	// rate 10 -> 20
	// true true
	// live rate: 20
}

// ExampleNewReloaderFunc gives an existing loader the reload lifecycle.
func ExampleNewReloaderFunc() {
	type AppConfig struct{ Version int }
	version := 0
	load := func(context.Context) (*AppConfig, error) {
		version++
		return &AppConfig{Version: version}, nil
	}
	ctx := context.Background()
	reloader, err := config.NewReloaderFunc(ctx, load)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	defer func() { _ = reloader.Stop(ctx) }()

	fmt.Println(reloader.Config().Version)
	if _, err := reloader.Reload(ctx); err != nil {
		fmt.Println("reload:", err)
	}
	fmt.Println(reloader.Config().Version, reloader.Stats().Successes)

	// Output:
	// 1
	// 2 1
}

// Example_reloadEndpoint mounts a manual reload on an HTTP mux: a refused
// reload answers with its error, which never quotes a sensitive value, and a
// reload already running answers 409. Put authentication in front of it.
func Example_reloadEndpoint() {
	type AppConfig struct {
		Port   int    `koanf:"port" default:"8080"`
		APIKey string `koanf:"api_key" default:"k-123"`
	}
	ctx := context.Background()
	reloader, err := config.NewReloader[AppConfig](ctx, nil)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	defer func() { _ = reloader.Stop(ctx) }()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/config/reload", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := reloader.Reload(r.Context())
		switch {
		case errors.Is(err, config.ErrReloadInProgress):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, config.ErrReloaderStopped):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(config.RedactedMap(cfg))
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())

	// Output:
	// 200 {"api_key":"[REDACTED]","port":"8080"}
}
