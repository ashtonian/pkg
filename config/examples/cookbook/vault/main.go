// Command vault layers a HashiCorp Vault KV v2 secret over its config file
// and environment through koanf's Vault provider, and reads it again on a
// poll so a rotated value reaches the process. Each key of the secret is a
// key of the config — db.password sets db.password — and one naming no key
// fails the load, so a typo in Vault never goes unnoticed.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/knadh/koanf/providers/vault/v2"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config"
)

type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	Password string `koanf:"password" validate:"required"`
}

type AppConfig struct {
	DB         DBConfig `koanf:"db"`
	SigningKey string   `koanf:"signing_key" validate:"required"`
}

// BootConfig is what the process needs before it can reach Vault. The token
// is one a Vault Agent sidecar keeps renewed.
type BootConfig struct {
	Vault struct {
		Address string        `koanf:"address" default:"https://vault.internal:8200" validate:"url"`
		Token   string        `koanf:"token" validate:"required"`
		Path    string        `koanf:"path" default:"secret/data/app"`
		Timeout time.Duration `koanf:"timeout" default:"5s" validate:"min=1ms"`
		Poll    time.Duration `koanf:"poll" default:"5m" validate:"min=1s"`
	} `koanf:"vault"`
}

// vaultSource reads the KV v2 secret at boot.Vault.Path on every load: its
// data, not its metadata, each key split on "." into the config's tree.
func vaultSource(boot *BootConfig) (*vault.Vault, error) {
	return vault.Provider(vault.Config{
		Address:     boot.Vault.Address,
		Token:       boot.Vault.Token,
		Path:        boot.Vault.Path,
		Delim:       ".",
		Timeout:     boot.Vault.Timeout,
		ExcludeMeta: true,
	})
}

// newReloader layers the secret over config.yaml and the environment, and
// reads it again every poll.
func newReloader(ctx context.Context, secrets koanf.Provider, poll time.Duration) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(secrets, nil),
		},
		config.WithPollInterval(poll),
	)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger); err != nil {
		logger.Error("vault config", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	boot, err := config.Load[BootConfig](ctx, config.WithEnvPrefix("BOOT")) // BOOT_VAULT_TOKEN, ...
	if err != nil {
		return err
	}
	secrets, err := vaultSource(boot)
	if err != nil {
		return err
	}
	reloader, err := newReloader(ctx, secrets, boot.Vault.Poll)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- reloader.Run(ctx) }()
	logger.Info("running", "db_host", reloader.Config().DB.Host)

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
