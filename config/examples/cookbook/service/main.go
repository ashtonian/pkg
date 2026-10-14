// Command service is a whole HTTP service around one config struct: Kong
// makes its flags and help from the struct, the config file, environment and
// flags load through one validated pipeline, and a reloader applies what can
// change live — the log level, the greeting — on a file change or SIGHUP and
// reports the rest as waiting for a restart.
//
//	service --config service.yaml --log-level debug
//	service print       # the effective config, secrets redacted
//	service template    # a starter config file
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

// Validate holds the rule no tag states.
func (c *AppConfig) Validate() error {
	if strings.TrimSpace(c.Greeting) == "" {
		return errors.New("greeting must not be blank")
	}
	return nil
}

type CLI struct {
	Config    string `type:"path" default:"service.yaml" help:"Config file."`
	AppConfig `embed:""`

	Serve    struct{} `cmd:"" default:"1" help:"Run the service."`
	Print    struct{} `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Template struct{} `cmd:"" help:"Write a starter config file."`
}

// newReloader loads the config from the file, APP_* and the flags kctx
// parsed, and registers an applier for each part that changes live: the log
// level, set into level, and the greeting and admin token, which handlers
// read from Config on every request. The http section is the listener's,
// fixed at start: a change to it waits for a restart.
func newReloader(ctx context.Context, cli *CLI, kctx *kong.Context, logger *slog.Logger, level *slog.LevelVar) (*config.Reloader[AppConfig], error) {
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
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile(cli.Config),
			config.WithEnvPrefix("APP"),
			kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
			config.WithLogger(logger),
		},
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
	)
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
	var cli CLI
	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
	if err := run(kctx, &cli); err != nil {
		fmt.Fprintln(os.Stderr, err) // names the failing key, never a secret's value
		os.Exit(1)
	}
}

func run(kctx *kong.Context, cli *CLI) error {
	if kctx.Command() == "template" {
		return config.PrintTemplate[AppConfig]("APP")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	level := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	reloader, err := newReloader(ctx, cli, kctx, logger, level)
	if err != nil {
		return err
	}
	if kctx.Command() == "print" {
		return config.PrintRedacted(reloader.Config(), config.WithFormat(config.FormatYAML), config.WithWriter(os.Stdout))
	}
	if err := reloader.Arm(ctx); err != nil {
		return err
	}
	go func() {
		if err := reloader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	httpCfg := reloader.Config().HTTP
	server := &http.Server{Addr: httpCfg.Addr, Handler: routes(reloader), ReadHeaderTimeout: httpCfg.ReadHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "serving", "addr", httpCfg.Addr)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpCfg.ShutdownTimeout)
	defer cancel()
	return errors.Join(server.Shutdown(shutdownCtx), reloader.Stop(shutdownCtx))
}
