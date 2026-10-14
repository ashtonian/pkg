// Command database keeps its runtime settings in a database table and
// reloads them on a poll, validated like any other source: a row naming no
// key, or a value out of range, keeps the last valid settings live.
//
//	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
//	INSERT INTO settings VALUES ('rate_limit', '200'), ('features.export', 'true');
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver

	"github.com/ashtonian/pkg/config"
)

// BootConfig is what the process needs before it can read the table.
type BootConfig struct {
	DatabaseURL  string        `koanf:"database_url" validate:"required" sensitive:"true"`
	QueryTimeout time.Duration `koanf:"query_timeout" default:"5s" validate:"min=1ms"`
	PollInterval time.Duration `koanf:"poll_interval" default:"30s" validate:"min=1s"`
}

type AppConfig struct {
	RateLimit   int             `koanf:"rate_limit" default:"100" validate:"min=1"`
	Maintenance bool            `koanf:"maintenance"`
	Features    map[string]bool `koanf:"features"`
}

// settings reads the settings table on every load: one row per key in the
// package's dotted grammar, each value text that decodes as an environment
// variable's would.
func settings(db *sql.DB, timeout time.Duration) (config.ContextProvider, error) {
	return config.NewMapProvider(func(ctx context.Context) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		rows, err := db.QueryContext(ctx, "SELECT key, value FROM settings")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]any{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, rows.Err()
	}, ".")
}

// newReloader layers the table over config.yaml and the environment and
// reads it again every poll interval. Code that writes the table calls
// SourceChanged to apply its change without waiting for the poll.
func newReloader(ctx context.Context, db *sql.DB, boot *BootConfig) (*config.Reloader[AppConfig], error) {
	table, err := settings(db, boot.QueryTimeout)
	if err != nil {
		return nil, err
	}
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(table, nil),
		},
		config.WithPollInterval(boot.PollInterval),
	)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger); err != nil {
		logger.Error("database settings", "error", err)
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
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)

	reloader, err := newReloader(ctx, db, boot)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- reloader.Run(ctx) }()
	logger.Info("running", "rate_limit", reloader.Config().RateLimit)

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := reloader.Stop(stopCtx); err != nil {
		return err
	}
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
