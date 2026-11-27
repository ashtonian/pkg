// Command server is a runnable API built on github.com/ashtonian/pkg/parse. It speaks the
// list contract HTTP handlers and the TypeScript SDK share
// (filter.Param*): keyset-paginated lists with JSON or flat-parameter
// filters, read from a GET's query string or a POST's JSON body, facets
// over OPTIONS, JSON Merge Patch updates and schema discovery, on the
// database its db settings name, which parseprovider opens among the
// providers the binary links (parsesql, parsepgx): an in-memory SQLite
// database by default, or PostgreSQL. It is a module of its own, outside
// the repository's Go workspace; from its directory:
//
//	GOWORK=off go run . --addr 127.0.0.1:8080
//	curl '127.0.0.1:8080/api/products?category=books&sort=-price&page_size=2'
//	curl -H 'Content-Type: application/json' -d '{"filter":{"category":"books"},"sort":"-price","page_size":2}' 127.0.0.1:8080/api/products
//
// On PostgreSQL:
//
//	PARSE_SERVER_DB_PROVIDER=postgres PARSE_SERVER_DB_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/app' GOWORK=off go run .
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parseprovider"
	"github.com/ashtonian/pkg/parse/parsesql"
)

const (
	// serviceName names the process in its startup entry and its CLI.
	serviceName = "parse-example"
	// envPrefix prefixes every setting's environment variable.
	envPrefix = "PARSE_SERVER"
)

type serverSettings struct {
	LogLevel          string        `koanf:"log_level" default:"warn" validate:"oneof=trace debug info warn error" desc:"Minimum log level: trace, debug, info, warn or error; applied live on a reload"`
	RequestTimeout    time.Duration `koanf:"request_timeout" default:"5s" validate:"gt=0" desc:"Deadline per request; its database work runs under it"`
	Addr              string        `koanf:"addr" default:"127.0.0.1:8080" validate:"required" desc:"HTTP listen address"`
	ShutdownGrace     time.Duration `koanf:"shutdown_grace" default:"10s" validate:"gt=0" desc:"Time to drain in-flight requests on shutdown"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"gt=0" desc:"HTTP header read deadline"`
	ReadTimeout       time.Duration `koanf:"read_timeout" default:"10s" validate:"gt=0" desc:"HTTP request read deadline"`
	WriteTimeout      time.Duration `koanf:"write_timeout" default:"10s" validate:"gt=0" desc:"HTTP response write deadline"`
	IdleTimeout       time.Duration `koanf:"idle_timeout" default:"60s" validate:"gt=0" desc:"HTTP keep-alive idle deadline"`
	// DB has no flags: the environment or the settings file sets it, so a
	// DSN's password stays out of the process list and shell history.
	DB dbSettings `koanf:"db" kong:"-"`
}

// dbSettings name the provider the server runs on, and set up each provider
// it links; Open uses the named provider's settings alone.
type dbSettings struct {
	Provider string          `koanf:"provider" default:"sqlite" validate:"oneof=sqlite postgres" desc:"Database parse runs on: sqlite or postgres"`
	SQLite   parsesql.Config `koanf:"sqlite"`
	Postgres parsepgx.Config `koanf:"postgres"`
}

func main() {
	// The level is warn until the settings load, and a reload changes it
	// live.
	level := new(slog.LevelVar)
	level.Set(slog.LevelWarn)
	log := newLogger(os.Stderr, level)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, stop, os.Args[1:], level, log)
	if err != nil {
		log.ErrorContext(ctx, "server exited", slog.Any("error", err))
		os.Exit(1)
	}
	log.InfoContext(ctx, "stopped")
}

// loadSettings reads the command line and loads the settings from the
// environment, the --config file when one is named, and the flags given,
// each above the one before. The reloader re-reads the same sources on every
// reload: a change to the file, or one of opts' triggers.
func loadSettings(ctx context.Context, args []string, opts ...config.ReloadOption) (*config.Reloader[serverSettings], error) {
	var cli struct {
		ConfigFile string         `name:"config" type:"path" help:"Settings file, YAML or JSON, read beneath the environment and flags; a change to it reloads the settings"`
		Settings   serverSettings `embed:""`
	}
	parser, err := kong.New(&cli, kong.Name(serviceName), kongprovider.HelpFromConfig(&cli.Settings, envPrefix))
	if err != nil {
		return nil, fmt.Errorf("configuring CLI: %w", err)
	}
	parsed, err := parser.Parse(args)
	if err != nil {
		return nil, fmt.Errorf("parsing CLI: %w", err)
	}
	sources := []config.Option{config.WithEnvPrefix(envPrefix), kongprovider.WithOverridesFromStruct(parsed, &cli.Settings)}
	if cli.ConfigFile != "" {
		sources = append(sources, config.WithRequiredFile(cli.ConfigFile))
		opts = append(opts, config.WithFileWatch(0))
	}
	return config.NewReloader[serverSettings](ctx, sources, opts...)
}

// levelApplier applies a reloaded log_level to level. The other settings
// take a restart, and the reloader logs each change to them as waiting for
// one.
func levelApplier(level *slog.LevelVar, log *slog.Logger) config.ReloadOption {
	applier := config.Applier[serverSettings]{
		Name:     "log_level",
		Prefixes: []string{"log_level"},
		Apply: func(ctx context.Context, ev config.ApplyEvent[serverSettings]) (config.ApplyResult, error) {
			l, err := parseLevel(ev.Desired.LogLevel)
			if err != nil {
				return config.ApplyResult{}, err
			}
			level.Set(l)
			logAlways(ctx, log, "log level changed", slog.String("log_level", levelName(l)))
			return config.ApplyResult{}, nil
		},
	}
	return config.WithApplier(applier)
}

// logStart writes the startup entry at any level: what started and how it
// is set up, with the settings redacted as pkg/config judges secrets.
func logStart(ctx context.Context, log *slog.Logger, level slog.Level, settings *serverSettings) {
	logAlways(ctx, log, "starting",
		slog.String("service", serviceName),
		slog.String("version", version()),
		slog.String("log_level", levelName(level)),
		slog.Any("config", config.RedactedTree(settings)),
	)
}

// version is the main module's version as the build recorded it: a module
// version when installed with go install, "(devel)" from a checkout.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if ok {
		return info.Main.Version
	}
	return "unknown"
}

// run loads the settings, serves until ctx ends, then drains the requests in
// flight. It calls stop once ctx has ended, so a second SIGINT or SIGTERM
// ends the process at once rather than waiting out the drain.
func run(ctx context.Context, stop context.CancelFunc, args []string, level *slog.LevelVar, log *slog.Logger) (err error) {
	settings, err := loadSettings(ctx, args,
		config.WithSignalReload(syscall.SIGHUP), config.WithReloadLogger(log), levelApplier(level, log))
	if err != nil {
		return err
	}
	startup := settings.Config()
	initial, err := parseLevel(startup.LogLevel)
	if err != nil {
		return err
	}
	level.Set(initial)
	logStart(ctx, log, initial, startup)
	err = settings.Arm(ctx)
	if err != nil {
		return fmt.Errorf("arming settings reload: %w", err)
	}
	reloading := make(chan struct{})
	go func() {
		defer close(reloading)
		runErr := settings.Run(ctx)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			log.ErrorContext(ctx, "reloading settings", slog.Any("error", runErr))
		}
	}()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startup.ShutdownGrace)
		defer cancel()
		err = errors.Join(err, settings.Stop(stopCtx))
		<-reloading
	}()

	db, err := openDB(ctx, startup.DB)
	if err != nil {
		return err
	}
	// Deferred after the reloader's stop, so it runs before it, once serve
	// has drained the requests that use the database.
	defer func() { err = errors.Join(err, db.Close()) }()
	log.InfoContext(ctx, "database opened", slog.String("provider", db.Provider().Name()))
	handler, err := newServer(db, log, startup.RequestTimeout, rand.Text)
	if err != nil {
		return err
	}
	return serve(ctx, stop, startup, handler, log)
}

// serve listens on settings.Addr and serves handler until ctx ends, then
// drains for the shutdown grace and closes what is left.
func serve(ctx context.Context, stop context.CancelFunc, settings *serverSettings, handler http.Handler, log *slog.Logger) (err error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", settings.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", settings.Addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		ReadTimeout:       settings.ReadTimeout,
		WriteTimeout:      settings.WriteTimeout,
		IdleTimeout:       settings.IdleTimeout,
	}
	// Close also terminates active connections if graceful shutdown expires.
	defer func() { err = errors.Join(err, srv.Close()) }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.InfoContext(ctx, "listening", slog.String("addr", ln.Addr().String()))

	select {
	case err = <-serveErr:
		return err
	case <-ctx.Done():
	}
	stop()
	log.InfoContext(ctx, "shutting down", slog.String("grace", settings.ShutdownGrace.String()))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settings.ShutdownGrace)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	err = <-serveErr
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openDB opens the database cfg names and seeds its products.
func openDB(ctx context.Context, cfg dbSettings) (parseprovider.Database, error) {
	db, err := parseprovider.Open(ctx, cfg.Provider, parsesql.Choice(cfg.SQLite), parsepgx.Choice(cfg.Postgres))
	if err != nil {
		return nil, err
	}
	err = seed(ctx, db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// seedProducts are the demonstration products, p01 onwards, each created
// an hour after the one before it from seedEpoch.
var seedProducts = []struct {
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
}

var seedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// seedSchema is one provider's statements for the products table, run in
// order, and the insert of one product, which takes the id, name,
// category, price, labels and creation time.
type seedSchema struct {
	create []string
	insert string
	// createdAt is a creation time as the provider's column holds it.
	createdAt func(time.Time) any
}

// The tables hold what Product's types read: on SQLite the labels as JSON
// text and the creation time as parse's timestamp text (core.FormatTimestamp),
// on PostgreSQL jsonb and timestamptz. The labels' index is the one their
// indexed tag declares, which PostgreSQL's containment filter uses.
var (
	sqliteSeed = seedSchema{
		create: []string{`CREATE TABLE IF NOT EXISTS products (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			category   TEXT NOT NULL,
			price      REAL NOT NULL,
			labels     TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL
		)`},
		insert:    `INSERT INTO products VALUES (?1, ?2, ?3, ?4, ?5, ?6) ON CONFLICT (id) DO NOTHING`,
		createdAt: func(t time.Time) any { return core.FormatTimestamp(t) },
	}
	postgresSeed = seedSchema{
		create: []string{`CREATE TABLE IF NOT EXISTS products (
			id         text PRIMARY KEY,
			name       text NOT NULL,
			category   text NOT NULL,
			price      double precision NOT NULL,
			labels     jsonb NOT NULL DEFAULT '{}',
			created_at timestamptz NOT NULL
		)`, `CREATE INDEX IF NOT EXISTS products_labels ON products USING gin (labels)`},
		insert:    `INSERT INTO products VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		createdAt: func(t time.Time) any { return t },
	}
)

// seed creates the products table when it is missing and adds the
// demonstration products it does not hold, so a database that persists
// keeps the changes made to them across restarts.
func seed(ctx context.Context, db parseprovider.Database) error {
	switch opened := db.(type) {
	case *parsesql.Database:
		return sqliteSeed.run(ctx, sqliteExec(opened.DB()))
	case *parsepgx.Database:
		return postgresSeed.run(ctx, poolExec(opened.Pool()))
	}
	return fmt.Errorf("seeding products: provider %s: no products schema for it", db.Provider().Name())
}

// execFunc runs one statement.
type execFunc func(ctx context.Context, stmt string, args ...any) error

func sqliteExec(db *sql.DB) execFunc {
	return func(ctx context.Context, stmt string, args ...any) error {
		_, err := db.ExecContext(ctx, stmt, args...)
		return err
	}
}

func poolExec(pool *pgxpool.Pool) execFunc {
	return func(ctx context.Context, stmt string, args ...any) error {
		_, err := pool.Exec(ctx, stmt, args...)
		return err
	}
}

// run creates the table and inserts the products through exec.
func (s seedSchema) run(ctx context.Context, exec execFunc) error {
	for _, stmt := range s.create {
		err := exec(ctx, stmt)
		if err != nil {
			return fmt.Errorf("creating products: %w", err)
		}
	}
	for i, p := range seedProducts {
		id := fmt.Sprintf("p%02d", i+1)
		err := exec(ctx, s.insert, id, p.name, p.category, p.price, p.labels, s.createdAt(seedEpoch.Add(time.Duration(i)*time.Hour)))
		if err != nil {
			return fmt.Errorf("seeding product %s: %w", id, err)
		}
	}
	return nil
}
