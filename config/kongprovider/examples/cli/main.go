package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

const envPrefix = "APP"

// DBConfig becomes the --db-* flags through the prefix on its field below.
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" kong:"-"` // file or APP_DB_PASSWORD only
}

// AppConfig is the whole schema: keys, defaults, descriptions, validation and
// the flags Kong makes of it.
type AppConfig struct {
	Port     int               `koanf:"port" default:"8080" validate:"min=1,max=65535" short:"p" desc:"HTTP listen port"`
	LogLevel string            `koanf:"log_level" default:"info" validate:"oneof=debug info warn error" desc:"Log level"`
	Timeout  time.Duration     `koanf:"timeout" default:"30s" desc:"Request timeout"`
	Metrics  bool              `koanf:"metrics" default:"true" negatable:"" desc:"Serve /metrics"`
	DB       DBConfig          `koanf:"db" embed:"" prefix:"db-"`
	Limits   map[string]int    `koanf:"limits" kong:"-"` // per-route limits, from the file
	Labels   map[string]string `koanf:"labels" kong:"-"`
}

// Validate holds the rule no tag states. It sees the loaded config: the
// file, APP_* and the typed flags, never Kong's defaults alone.
func (c *AppConfig) Validate() error {
	if c.DB.MaxConns > 100 && c.Timeout < time.Second {
		return errors.New("a pool of more than 100 connections needs a timeout of at least 1s")
	}
	return nil
}

// ServeCmd and PrintCmd each hold the config; Load fills it for the one that
// runs.
type ServeCmd struct {
	AppConfig `embed:""`
}

func (c *ServeCmd) Run() error {
	fmt.Printf("serving on :%d (log level %s, db %s, metrics %v)\n", c.Port, c.LogLevel, c.DB.Host, c.Metrics)
	return nil
}

type PrintCmd struct {
	AppConfig `embed:""`
}

func (c *PrintCmd) Run() error {
	return config.PrintRedacted(&c.AppConfig, config.WithFormat(config.FormatYAML), config.WithWriter(os.Stdout))
}

// UsageCmd and TemplateCmd hold no config, so they run without one.
type UsageCmd struct{}

func (UsageCmd) Run() error { return config.PrintUsage[AppConfig](envPrefix) }

type TemplateCmd struct{}

func (TemplateCmd) Run() error { return config.PrintTemplate[AppConfig](envPrefix) }

// CLI adds what is the CLI's own: the config file's path and the commands.
type CLI struct {
	Config string `type:"path" default:"config.yaml" help:"Config file."`

	Serve    ServeCmd    `cmd:"" default:"withargs" help:"Run the server."`
	Print    PrintCmd    `cmd:"" help:"Print the effective configuration as YAML, secrets redacted."`
	Usage    UsageCmd    `cmd:"" help:"List every setting with its environment variable."`
	Template TemplateCmd `cmd:"" help:"Write a starter config file: every key with its default."`
}

func main() {
	ctx := context.Background()
	var cli CLI
	// Called during Parse, once the command line is parsed: cli.Config holds
	// --config.
	files := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config)}, nil
	}
	kctx := kong.Parse(&cli,
		kongprovider.Load(ctx, &cli.Serve.AppConfig, []string{envPrefix}, files),
		kongprovider.Load(ctx, &cli.Print.AppConfig, []string{envPrefix}, files),
	)
	kctx.FatalIfErrorf(kctx.Run())
}
