// Command kubernetes takes its settings from a ConfigMap and its credentials
// from a Secret, both mounted as volumes, and reloads when either changes.
// The kubelet updates a volume by swapping its ..data symlink: the file watch
// follows it for config.yaml, and the Secret provider's own watch sees it.
//
//	volumes:
//	  - name: config
//	    configMap: {name: app}        # key config.yaml
//	  - name: secrets
//	    secret: {secretName: app}     # key db.password
//	containers:
//	  - volumeMounts:                 # no subPath: a subPath mount never updates
//	      - {name: config, mountPath: /etc/app/config}
//	      - {name: secrets, mountPath: /etc/app/secrets}
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/knadh/koanf/providers/k8smount"

	"github.com/ashtonian/pkg/config"
)

type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1"`
	Password string `koanf:"password" validate:"required"`
}

type AppConfig struct {
	LogLevel string   `koanf:"log_level" default:"info" validate:"oneof=debug info warn error"`
	DB       DBConfig `koanf:"db"`
}

// secretVolume reads every key of a mounted Secret as a key of the config:
// the Secret key db.password sets db.password.
func secretVolume(dir string) *k8smount.K8SMount {
	return k8smount.Provider(dir, ".", k8smount.Opt{
		TransformFunc: func(key, value string) (string, any) { return key, strings.TrimSpace(value) },
	})
}

// newReloader layers the Secret over config.yaml from the ConfigMap volume
// and the environment, and watches config.yaml.
func newReloader(ctx context.Context, configDir string, secrets *k8smount.K8SMount) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithRequiredFile(filepath.Join(configDir, "config.yaml")),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(secrets, nil),
		},
		config.WithFileWatch(0),
	)
}

// watchSecrets reloads on any change to the Secret volume — a rotated value,
// or a key added later — through the provider's own watch. Any koanf
// provider with a Watch method (Consul, etcd, NATS, AppConfig) bridges the
// same way; SourceChanged coalesces a burst into one reload.
func watchSecrets(secrets *k8smount.K8SMount, reloader *config.Reloader[AppConfig], logger *slog.Logger) error {
	return secrets.Watch(func(_ any, err error) {
		if err != nil {
			logger.Error("secret volume watch stopped; rotations need a restart", "error", err)
			return
		}
		reloader.SourceChanged()
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger); err != nil {
		logger.Error("kubernetes config", "error", err) // names the key, never the secret
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	secrets := secretVolume("/etc/app/secrets")
	reloader, err := newReloader(ctx, "/etc/app/config", secrets)
	if err != nil {
		return err
	}
	// Arm before reporting ready: a volume that cannot be watched fails here.
	if err := reloader.Arm(ctx); err != nil {
		return err
	}
	if err := watchSecrets(secrets, reloader, logger); err != nil {
		return err
	}
	defer func() {
		if err := secrets.Unwatch(); err != nil {
			logger.Warn("closing the secret volume watch", "error", err)
		}
	}()
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
