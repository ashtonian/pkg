// Command awssecrets resolves its credentials from AWS Secrets Manager:
// every field tagged secret:"<ref>" is fetched through SecretsFrom on each
// load, and a poll picks up rotations. The refs name secrets relative to a
// per-environment prefix, so one config type serves every environment.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/ashtonian/pkg/config"
)

type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	User     string `koanf:"user" secret:"db#username"`
	Password string `koanf:"password" secret:"db#password" validate:"required"`
}

type AppConfig struct {
	DB         DBConfig `koanf:"db"`
	WebhookKey string   `koanf:"webhook_key" secret:"webhook-key,optional"` // a dev file may set it instead
}

// BootConfig is what the process needs before it can reach the store.
type BootConfig struct {
	SecretPrefix  string        `koanf:"secret_prefix" validate:"required"` // e.g. prod/app/
	FetchTimeout  time.Duration `koanf:"fetch_timeout" default:"10s" validate:"min=1ms"`
	RotationCheck time.Duration `koanf:"rotation_check" default:"15m" validate:"min=1m"`
}

// secretValues is the part of the Secrets Manager client a fetch uses.
type secretValues interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// fetchSecret reads the secret prefix+ref. One that does not exist is
// config.ErrSecretNotFound, so an ,optional tag leaves its field as the
// sources set it.
func fetchSecret(sm secretValues, prefix string) func(ctx context.Context, ref string) (string, error) {
	return func(ctx context.Context, ref string) (string, error) {
		out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(prefix + ref)})
		var missing *types.ResourceNotFoundException
		switch {
		case errors.As(err, &missing):
			return "", fmt.Errorf("%w: %s%s", config.ErrSecretNotFound, prefix, ref)
		case err != nil:
			return "", err
		case out.SecretString == nil:
			return "", fmt.Errorf("secret %s%s is binary; store it as a string", prefix, ref)
		}
		return *out.SecretString, nil
	}
}

func newReloader(ctx context.Context, sm secretValues, boot *BootConfig) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithSecretPopulator(config.SecretsFrom(fetchSecret(sm, boot.SecretPrefix))),
		},
		config.WithPollInterval(boot.RotationCheck),
	)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger); err != nil {
		logger.Error("aws secrets", "error", err) // names the key and ref, never a value
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	boot, err := config.Load[BootConfig](ctx, config.WithEnvPrefix("BOOT")) // BOOT_SECRET_PREFIX, ...
	if err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(boot.FetchTimeout)))
	if err != nil {
		return err
	}
	reloader, err := newReloader(ctx, secretsmanager.NewFromConfig(awsCfg), boot)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- reloader.Run(ctx) }()
	logger.Info("running", "db_user", reloader.Config().DB.User)

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
