// Command server is a runnable API built on github.com/ashtonian/pkg/parse. It speaks the
// list contract HTTP handlers and the TypeScript SDK share
// (filter.Param*): keyset-paginated lists with JSON or flat-parameter
// filters, read from a GET's query string or a POST's JSON body, facets
// over OPTIONS, JSON Merge Patch updates and schema discovery, over an
// in-memory SQLite database.
//
//	go run ./examples/server --addr 127.0.0.1:8080
//	curl '127.0.0.1:8080/api/products?category=books&sort=-price&page_size=2'
//	curl -H 'Content-Type: application/json' -d '{"filter":{"category":"books"},"sort":"-price","page_size":2}' 127.0.0.1:8080/api/products
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/parsesqlite"
)

// serviceName names the process in its startup entry and its CLI.
const serviceName = "parse-example"

type serverSettings struct {
	LogLevel          string        `koanf:"log_level" default:"warn" validate:"oneof=debug info warn error" desc:"Minimum log level: debug, info, warn or error"`
	RequestTimeout    time.Duration `koanf:"request_timeout" default:"5s" validate:"gt=0" desc:"Deadline per request; its database work runs under it"`
	Addr              string        `koanf:"addr" default:"127.0.0.1:8080" validate:"required" desc:"HTTP listen address"`
	ShutdownGrace     time.Duration `koanf:"shutdown_grace" default:"10s" validate:"gt=0" desc:"Time to drain in-flight requests on shutdown"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"gt=0" desc:"HTTP header read deadline"`
	ReadTimeout       time.Duration `koanf:"read_timeout" default:"10s" validate:"gt=0" desc:"HTTP request read deadline"`
	WriteTimeout      time.Duration `koanf:"write_timeout" default:"10s" validate:"gt=0" desc:"HTTP response write deadline"`
	IdleTimeout       time.Duration `koanf:"idle_timeout" default:"60s" validate:"gt=0" desc:"HTTP keep-alive idle deadline"`
}

func main() {
	// The level is a LevelVar so it can change without a restart; it is warn
	// until the settings say otherwise.
	level := new(slog.LevelVar)
	level.Set(slog.LevelWarn)
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	settings, err := loadSettings(ctx, os.Args[1:])
	if err == nil {
		err = level.UnmarshalText([]byte(settings.LogLevel))
	}
	if err == nil {
		logStart(ctx, log, level.Level(), settings)
		err = run(ctx, settings, log)
	}
	if err != nil {
		log.Error("server exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func loadSettings(ctx context.Context, args []string) (*serverSettings, error) {
	var cli struct {
		Settings serverSettings `embed:""`
	}
	parser, err := kong.New(&cli, kong.Name(serviceName), kongprovider.HelpFromConfig(&cli.Settings, "PARSE_SERVER"))
	if err != nil {
		return nil, fmt.Errorf("configure CLI: %w", err)
	}
	parsed, err := parser.Parse(args)
	if err != nil {
		return nil, fmt.Errorf("parse CLI: %w", err)
	}
	return config.Load[serverSettings](ctx, config.WithEnvPrefix("PARSE_SERVER"),
		kongprovider.WithOverridesFromStruct(parsed, &cli.Settings))
}

// logStart writes the startup entry straight to the handler, past the level
// gate, so that at any level the log says what started and how it is set up.
// The settings are redacted as pkg/config judges secrets.
func logStart(ctx context.Context, log *slog.Logger, level slog.Level, settings *serverSettings) {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "starting", 0)
	r.AddAttrs(
		slog.String("service", serviceName),
		slog.String("version", version()),
		slog.String("log_level", level.String()),
		slog.Any("config", config.RedactedTree(settings)),
	)
	// A startup entry that cannot reach stderr has nowhere else to go.
	_ = log.Handler().Handle(ctx, r)
}

// version is the main module's version as the build recorded it: a module
// version when installed with go install, "(devel)" from a checkout.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}

func run(ctx context.Context, settings *serverSettings, log *slog.Logger) (err error) {
	db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	handler, err := newServer(db, log, settings.RequestTimeout)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              settings.Addr,
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		ReadTimeout:       settings.ReadTimeout,
		WriteTimeout:      settings.WriteTimeout,
		IdleTimeout:       settings.IdleTimeout,
	}
	// Close also terminates active connections if graceful shutdown expires.
	defer func() { err = errors.Join(err, srv.Close()) }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("listening", slog.String("addr", settings.Addr))

	select {
	case err = <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settings.ShutdownGrace)
	defer cancel()
	if err = srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err = <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openDB creates and seeds an in-memory database. One connection: every
// statement sees the same in-memory database. parse's SQLite functions are
// registered before the database opens a connection. Transactions begin
// IMMEDIATE, as parsesql needs for an update that reads before it writes.
func openDB(ctx context.Context) (*sql.DB, error) {
	if err := parsesqlite.Register(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", ":memory:?_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := seed(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func seed(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE products (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		category   TEXT NOT NULL,
		price      REAL NOT NULL,
		labels     TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create products: %w", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, p := range []struct {
		name, category, labels string
		price                  float64
	}{
		{"Gaming Laptop", "electronics", `{"brand":"acme","refurb":false}`, 1299.99},
		{"Wireless Mouse", "electronics", `{"brand":"acme"}`, 29.99},
		{"USB-C Hub", "electronics", `{"brand":"volt"}`, 45},
		{"Cotton T-Shirt", "clothing", `{}`, 19.99},
		{"Running Shoes", "clothing", `{"sizes":[40,41,42]}`, 120},
		{"Go Programming", "books", `{"format":"paperback"}`, 39.99},
		{"Clean Code", "books", `{"format":"hardcover"}`, 32},
		{"Design Patterns", "books", `{"format":"paperback"}`, 15},
		{"Desk Lamp", "home", `{}`, 55},
		{"Coffee Maker", "home", `{"brand":"volt"}`, 79.99},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO products VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
			fmt.Sprintf("p%02d", i+1), p.name, p.category, p.price, p.labels, core.FormatTimestamp(base.Add(time.Duration(i)*time.Hour)))
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.name, err)
		}
	}
	return nil
}
