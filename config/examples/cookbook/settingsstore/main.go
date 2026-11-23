// Command settingsstore keeps the settings an admin API writes in a table:
// one row per key, spelled as CheckKey and DecodeEdits take it, with a JSON
// value. A write is validated as a batch over the live config before it is
// stored, so the table never holds a batch the service would refuse, and
// the table loads back through NewEntryProvider with no parsing of its keys.
//
//	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
//	PUT /admin/settings {"limits.max": 200, "upstreams[\"api.internal\"].timeout": "2s"}
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver

	"github.com/ashtonian/pkg/config"
)

// BootConfig is what the process needs before it can read the table.
type BootConfig struct {
	DatabaseURL       string        `koanf:"database_url" validate:"required" sensitive:"true"`
	Addr              string        `koanf:"addr" default:":8080"`
	QueryTimeout      time.Duration `koanf:"query_timeout" default:"5s" validate:"min=1ms"`
	PollInterval      time.Duration `koanf:"poll_interval" default:"30s" validate:"min=1s"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"min=1ms"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout" default:"10s" validate:"min=1s"`
}

type Limits struct {
	Min int `koanf:"min" default:"1" validate:"min=1"`
	Max int `koanf:"max" default:"100"`
}

type Upstream struct {
	URL     string        `koanf:"url" validate:"required,url"`
	Timeout time.Duration `koanf:"timeout" default:"1s" validate:"min=1ms"`
}

type Route struct {
	Path     string `koanf:"path" validate:"required,startswith=/"`
	Upstream string `koanf:"upstream" validate:"required"`
}

type AppConfig struct {
	Limits    Limits              `koanf:"limits"`
	Upstreams map[string]Upstream `koanf:"upstreams" validate:"dive"`
	Routes    []Route             `koanf:"routes" validate:"dive"`
}

// Validate relates settings no single key can: a batch that moves both
// limits is judged with both moved.
func (c *AppConfig) Validate() error {
	if c.Limits.Min > c.Limits.Max {
		return errors.New("limits.min exceeds limits.max")
	}
	for i, r := range c.Routes {
		if _, ok := c.Upstreams[r.Upstream]; !ok {
			return fmt.Errorf("routes[%d] names no upstream", i)
		}
	}
	return nil
}

// Settings is the settings table.
type Settings struct {
	db      *sql.DB
	timeout time.Duration
	mu      sync.Mutex // one write at a time here; replicas sharing the table need the database to serialize theirs
}

// Provider reads the table on every load. Its keys are config's own, so a
// row needs nothing decoded but its JSON value.
func (s *Settings) Provider() (config.ContextProvider, error) {
	return config.NewEntryProvider(func(ctx context.Context) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		return readRows(ctx, s.db)
	})
}

// Write stores edits, verbatim, once the table they make validates over
// the live config, and tells the reloader the table changed. The rows
// already stored are checked with the edits, so a key spelled two ways
// across writes is refused too. A refused batch changes nothing; its error
// names keys, never a value.
func (s *Settings) Write(ctx context.Context, reloader *config.Reloader[AppConfig], edits map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op
	table, err := readRows(ctx, tx)
	if err != nil {
		return err
	}
	maps.Copy(table, edits)
	if _, err := config.DecodeEdits(ctx, reloader.Config(), table); err != nil {
		return err
	}
	for key, value := range edits {
		text, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encoding the value of %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, string(text)); err != nil {
			return fmt.Errorf("storing %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	reloader.SourceChanged()
	return nil
}

func readRows(ctx context.Context, db interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) (map[string]any, error) {
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var key, text string
		if err := rows.Scan(&key, &text); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return nil, fmt.Errorf("reading the value of %s: %w", key, err)
		}
		out[key] = value
	}
	return out, rows.Err()
}

// settingsHandler is PUT /admin/settings: a JSON object of keys and values,
// stored all or nothing. Mount it behind the service's admin authentication.
func settingsHandler(s *Settings, reloader *config.Reloader[AppConfig]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var edits map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&edits); err != nil {
			http.Error(w, "the body is not a JSON object", http.StatusBadRequest)
			return
		}
		switch err := s.Write(r.Context(), reloader, edits); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, config.ErrKey), errors.Is(err, config.ErrValidation):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			http.Error(w, "the settings store is unavailable", http.StatusServiceUnavailable)
		}
	}
}

// newReloader layers the table over config.yaml and the environment and
// reads it again every poll interval.
func newReloader(ctx context.Context, settings *Settings, poll time.Duration) (*config.Reloader[AppConfig], error) {
	table, err := settings.Provider()
	if err != nil {
		return nil, err
	}
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(table, nil),
		},
		config.WithPollInterval(poll),
	)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger); err != nil {
		logger.Error("settings store", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	boot, err := config.Load[BootConfig](ctx, config.WithEnvPrefix("SETTINGS")) // SETTINGS_DATABASE_URL, ...
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", boot.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	settings := &Settings{db: db, timeout: boot.QueryTimeout}
	reloader, err := newReloader(ctx, settings, boot.PollInterval)
	if err != nil {
		return err
	}
	go func() {
		if err := reloader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("PUT /admin/settings", settingsHandler(settings, reloader))
	server := &http.Server{Addr: boot.Addr, Handler: mux, ReadHeaderTimeout: boot.ReadHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "serving", "addr", boot.Addr)

	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boot.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), reloader.Stop(shutdownCtx))
}
