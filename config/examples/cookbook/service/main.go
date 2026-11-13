// Command service is a whole HTTP service around one config struct: Kong
// makes its flags and help from the struct, the config file, environment and
// flags load through one validated pipeline while Kong parses, and a reloader
// applies what can change live — the log level, the greeting — on a file
// change or SIGHUP and reports the rest as waiting for a restart.
//
//	service --config service.yaml --log-level debug
//	service print       # the effective config, secrets redacted
//	service template    # a starter config file
//	service schema      # the config file's JSON Schema, for an editor
//	kill -HUP $(pidof service)
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

type HTTPConfig struct {
	Addr              string        `koanf:"addr" default:":8080" desc:"Listen address"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"min=1ms" desc:"Time to read request headers"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout" default:"15s" validate:"min=1ms" desc:"Drain deadline on SIGTERM"`
}

type LogConfig struct {
	Level string `koanf:"level" default:"info" validate:"oneof=debug info warn error" desc:"Log level"`
}

// AppConfig is the whole schema: keys, defaults, validation, help and flags.
type AppConfig struct {
	HTTP       HTTPConfig `koanf:"http" embed:"" prefix:"http-"`
	Log        LogConfig  `koanf:"log" embed:"" prefix:"log-"`
	Greeting   string     `koanf:"greeting" default:"hello" desc:"What GET / answers"`
	AdminToken string     `koanf:"admin_token" validate:"required" kong:"-"` // file or APP_ADMIN_TOKEN only
}

// Validate holds the rule no tag states. It runs on the loaded config, on
// every load and reload.
func (c *AppConfig) Validate() error {
	if strings.TrimSpace(c.Greeting) == "" {
		return errors.New("greeting must not be blank")
	}
	return nil
}

// ServeCmd and PrintCmd hold the config; TemplateCmd and SchemaCmd need none,
// so they run without a valid one.
type CLI struct {
	Config string `type:"path" default:"service.yaml" help:"Config file."`

	Serve    ServeCmd    `cmd:"" default:"withargs" help:"Run the service."`
	Print    PrintCmd    `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Template TemplateCmd `cmd:"" help:"Write a starter config file."`
	Schema   SchemaCmd   `cmd:"" help:"Write the config file's JSON Schema."`
}

// loading returns the Kong options that load the config, while Kong parses,
// for the command that runs: for serve a reloader over the file, APP_* and
// the flags, with an applier for each part that changes live — the log
// level, set into level, and the greeting and admin token, which handlers
// read from Config on every request; for print the same sources, once. The
// http section is the listener's, fixed at start: a change to it waits for a
// restart.
func loading(ctx context.Context, cli *CLI, logger *slog.Logger, level *slog.LevelVar) ([]kong.Option, error) {
	var zero AppConfig
	levelKey, err := config.KeyOf(&zero, &zero.Log.Level)
	if err != nil {
		return nil, err
	}
	greetingKey, err := config.KeyOf(&zero, &zero.Greeting)
	if err != nil {
		return nil, err
	}
	tokenKey, err := config.KeyOf(&zero, &zero.AdminToken)
	if err != nil {
		return nil, err
	}
	sources := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config), config.WithLogger(logger)}, nil
	}
	return []kong.Option{
		kongprovider.LoadReloader(ctx, &cli.Serve.AppConfig, []string{"APP"}, sources,
			config.WithFileWatch(0),
			config.WithSignalReload(syscall.SIGHUP),
			config.WithApplier(config.Applier[AppConfig]{
				Name:         "log",
				Prefixes:     []string{levelKey},
				ApplyInitial: true,
				Apply: func(_ context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
					return config.ApplyResult{}, level.UnmarshalText([]byte(ev.Desired.Log.Level))
				},
			}),
			config.WithApplier(config.Applier[AppConfig]{
				Name:     "handlers",
				Prefixes: []string{greetingKey, tokenKey},
				Apply: func(context.Context, config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
					return config.ApplyResult{}, nil // read from Config on every request
				},
			}),
		),
		kongprovider.Load(ctx, &cli.Print.AppConfig, []string{"APP"}, sources),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(logger),
	}, nil
}

// ServeCmd's fields are the startup config: the listener's settings for the
// life of the process.
type ServeCmd struct {
	AppConfig `embed:""`
}

// Run serves until ctx ends, reloading the config as its sources change.
func (c *ServeCmd) Run(ctx context.Context, reloader *config.Reloader[AppConfig], logger *slog.Logger) error {
	if err := reloader.Arm(ctx); err != nil {
		return err
	}
	go func() {
		if err := reloader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	server := &http.Server{Addr: c.HTTP.Addr, Handler: routes(reloader), ReadHeaderTimeout: c.HTTP.ReadHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "serving", "addr", c.HTTP.Addr)

	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.HTTP.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), reloader.Stop(shutdownCtx))
}

type PrintCmd struct {
	AppConfig `embed:""`
}

func (c *PrintCmd) Run() error {
	return config.PrintRedacted(&c.AppConfig, config.WithFormat(config.FormatYAML), config.WithWriter(os.Stdout))
}

type TemplateCmd struct{}

func (TemplateCmd) Run() error { return config.PrintTemplate[AppConfig]("APP") }

type SchemaCmd struct{}

func (SchemaCmd) Run() error {
	schema, err := config.JSONSchema[AppConfig]()
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(schema, '\n'))
	return err
}

// routes serves the greeting, and the config's status and a reload behind
// the admin token.
func routes(reloader *config.Reloader[AppConfig]) http.Handler {
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(reloader.Config().AdminToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, reloader.Config().Greeting)
	})
	mux.HandleFunc("GET /admin/config", admin(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, reloader.Snapshot()) // generation, stats, appliers, pending restarts
	}))
	mux.HandleFunc("POST /admin/config/reload", admin(func(w http.ResponseWriter, r *http.Request) {
		result, err := reloader.Reload(r.Context())
		switch {
		case result != nil: // published; an ApplyError's outcome is in result.Applied
			writeJSON(w, http.StatusOK, result)
		case errors.Is(err, config.ErrReloadInProgress):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, config.ErrReloaderStopped):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		default:
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		}
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client went away; nothing to do
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err) // names the failing key, never a secret's value
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	level := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	var cli CLI
	opts, err := loading(ctx, &cli, logger, level)
	if err != nil {
		return err
	}
	parser, err := kong.New(&cli, opts...)
	if err != nil {
		return err
	}
	kctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err) // Kong's message and exit status: 80 for a usage error, 1 for a failed load
	return kctx.Run()
}
