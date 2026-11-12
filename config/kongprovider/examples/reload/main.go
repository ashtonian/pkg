package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig: the listener's settings take effect at start, the greeting on
// every request.
type AppConfig struct {
	Addr            string        `koanf:"addr" default:":8080" desc:"Listen address"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout" default:"15s" validate:"min=1ms" desc:"Drain deadline on SIGTERM"`
	Greeting        string        `koanf:"greeting" default:"hello" validate:"required" desc:"What GET / answers"`
}

type ServeCmd struct {
	AppConfig `embed:""`
}

// Run serves until SIGINT or SIGTERM. The receiver holds the startup config,
// which the listener uses; each request reads r.Config(), so an edit to the
// file, or SIGHUP, changes the greeting without a restart.
func (c *ServeCmd) Run(r *config.Reloader[AppConfig]) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := r.Arm(ctx); err != nil {
		return err // a config directory that cannot be watched
	}
	go func() {
		if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	server := &http.Server{
		Addr:              c.Addr,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, r.Config().Greeting)
		}),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), r.Stop(shutdownCtx))
}

type CLI struct {
	Config string   `type:"path" default:"config.yaml" help:"Config file."`
	Serve  ServeCmd `cmd:"" default:"withargs" help:"Run the server."`
}

func main() {
	var cli CLI
	files := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config)}, nil
	}
	kctx := kong.Parse(&cli,
		kongprovider.LoadReloader(context.Background(), &cli.Serve.AppConfig, []string{"APP"}, files,
			config.WithFileWatch(0),
			config.WithSignalReload(syscall.SIGHUP),
		),
	)
	if err := kctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err) // names the failing key, never a secret's value
		os.Exit(1)
	}
}
