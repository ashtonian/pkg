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
	"strings"
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

// ExampleWithAllowUnknownKeys shows a typo in a config file failing the load
// instead of leaving the service on the default it meant to override, and
// the option that lets such a key through.
func ExampleWithAllowUnknownKeys() {
	type AppConfig struct {
		Server struct {
			Port int `koanf:"port" default:"8080"`
		} `koanf:"server"`
	}
	file, cleanup := tempFile("config.yaml", "server:\n  prot: 9000\n")
	defer cleanup()

	_, err := config.Load[AppConfig](context.Background(), config.WithFile(file))
	fmt.Println(errors.Is(err, config.ErrValidation), err)

	cfg, err := config.Load[AppConfig](context.Background(), config.WithFile(file), config.WithAllowUnknownKeys())
	fmt.Println(cfg.Server.Port, err)

	// Output:
	// true unmarshaling config: configuration validation failed: keys no field of config_test.AppConfig decodes: server.prot
	// 8080 <nil>
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
		config.WithOnReloadSuccess(func(_ context.Context, ev config.ReloadEvent[AppConfig]) {
			old, new := ev.Old, ev.New
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

// Example_reloadEndpoint mounts a manual reload on an HTTP mux. A refused
// reload answers with its error, a reload already running answers 409, and a
// published one answers with its generation and what changed, a credential's
// values redacted. Put authentication in front of it.
func Example_reloadEndpoint() {
	type AppConfig struct {
		Port   int    `koanf:"port" default:"8080"`
		APIKey string `koanf:"api_key"`
	}
	ctx := context.Background()
	settings := map[string]any{"api_key": "k-1"} // stands in for a settings table
	source, err := config.NewMapProvider(func(context.Context) (map[string]any, error) {
		return settings, nil
	}, ".")
	if err != nil {
		fmt.Println(err)
		return
	}
	reloader, err := config.NewReloader[AppConfig](ctx, []config.Option{config.WithBaseProvider(source, nil)})
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	defer func() { _ = reloader.Stop(ctx) }()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/config/reload", func(w http.ResponseWriter, r *http.Request) {
		result, err := reloader.Reload(r.Context())
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
		_ = json.NewEncoder(w).Encode(struct {
			Generation uint64          `json:"generation"`
			Changes    []config.Change `json:"changes"`
		}{result.Snapshot.Generation, result.Changes})
	})

	settings = map[string]any{"port": 9090, "api_key": "k-2"}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())

	// Output:
	// 200 {"generation":2,"changes":[{"key":"port","old":{"present":true,"type":"int","text":"8080"},"new":{"present":true,"type":"int","text":"9090"}},{"key":"api_key","old":{"present":true,"type":"string","text":"[REDACTED]","redacted":true},"new":{"present":true,"type":"string","text":"[REDACTED]","redacted":true}}]}
}

// ExampleDiff lists what changed between two configs, keyed in the package's
// grammar; a credential's change is reported with its values redacted.
func ExampleDiff() {
	type Limits struct {
		Rate int `koanf:"rate"`
	}
	type AppConfig struct {
		Port     int               `koanf:"port"`
		Password string            `koanf:"password"`
		Tags     []string          `koanf:"tags"`
		Features map[string]Limits `koanf:"features"`
	}
	old := &AppConfig{Port: 8080, Password: "a", Tags: []string{"web"}, Features: map[string]Limits{"audit": {Rate: 10}}}
	next := &AppConfig{Port: 9090, Password: "b", Tags: []string{"web", "api"}, Features: map[string]Limits{"audit": {Rate: 20}, "export": {Rate: 1}}}

	changes, err := config.Diff(old, next)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, c := range changes {
		switch {
		case c.Structural:
			fmt.Printf("%s: added (%s)\n", c.Key, c.Field.Path)
		case !c.Old.Present:
			fmt.Printf("%s: + %s\n", c.Key, c.New.Text)
		default:
			fmt.Printf("%s: %s -> %s\n", c.Key, c.Old.Text, c.New.Text)
		}
	}

	// Output:
	// port: 8080 -> 9090
	// password: [REDACTED] -> [REDACTED]
	// tags[1]: + api
	// features["audit"].rate: 10 -> 20
	// features["export"]: added (features[*])
	// features["export"].rate: + 1
}

// ExamplePrintTemplate writes the config file a struct describes: every key
// with its default, its description and the variable that sets it; a key with
// no default, and a secret, are commented out with no value.
func ExamplePrintTemplate() {
	type DBConfig struct {
		Host     string `koanf:"host" default:"localhost" desc:"Database host"`
		Password string `koanf:"password" validate:"required"`
	}
	type AppConfig struct {
		Port    int           `koanf:"port" default:"8080" desc:"HTTP listen port"`
		Timeout time.Duration `koanf:"timeout" default:"30s"`
		Tags    []string      `koanf:"tags" default:"web,api"`
		DB      DBConfig      `koanf:"db"`
	}
	if err := config.PrintTemplate[AppConfig]("APP", config.WithWriter(os.Stdout)); err != nil {
		fmt.Println(err)
	}

	// Output:
	// # Configuration for config_test.AppConfig: every key with its default. A commented key has
	// # no default, or one that is not shown; uncomment it to set it.
	// # HTTP listen port
	// # int; env APP_PORT
	// port: 8080
	// # time.Duration; env APP_TIMEOUT
	// timeout: 30s
	// # []string; env APP_TAGS
	// tags:
	//   - web
	//   - api
	// db:
	//   # Database host
	//   # string; env APP_DB_HOST
	//   host: localhost
	//   # string, required, sensitive; env APP_DB_PASSWORD
	//   # password:
}

// ExamplePrintExplain shows where each value came from: a tag default, an
// environment variable by name, and a post-load hook that changed a value
// after decoding. The password's value stays redacted; its source does not.
func ExamplePrintExplain() {
	type DB struct {
		Host     string `koanf:"host" default:"localhost"`
		Password string `koanf:"password"`
	}
	type AppConfig struct {
		Port     int    `koanf:"port" default:"8080"`
		LogLevel string `koanf:"log_level" default:"info"`
		DB       DB     `koanf:"db"`
	}
	snap, err := config.LoadSnapshot[AppConfig](context.Background(),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_PORT=9000", "APP_DB_PASSWORD=s3cret"} }),
		config.WithPostLoad(func(_ context.Context, c *AppConfig) error {
			c.LogLevel = strings.ToUpper(c.LogLevel)
			return nil
		}),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := config.PrintExplain(snap, config.WithWriter(os.Stdout)); err != nil {
		fmt.Println(err)
	}

	// Output:
	// KEY          VALUE       SOURCE
	// ---------------------------------------------------
	// port         9000        environment APP_PORT
	// log_level    INFO        tag_default → post_load 0
	// db.host      localhost   tag_default
	// db.password  [REDACTED]  environment APP_DB_PASSWORD
}
