package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig owns every default, description and validation rule; a flag tag
// ties a field to the CLI flag that overrides it.
type AppConfig struct {
	Port     int    `koanf:"port" default:"8080" validate:"min=1,max=65535" flag:"port" desc:"HTTP listen port"`
	LogLevel string `koanf:"log_level" default:"info" validate:"oneof=debug info warn error" flag:"log-level" desc:"Log level"`
	Metrics  bool   `koanf:"metrics" default:"true" flag:"metrics" desc:"Serve /metrics"`
}

// CLI declares the flags' names and shapes only.
type CLI struct {
	Config   string `type:"path" default:"config.yaml" help:"Config file."`
	Port     int    `short:"p"`
	LogLevel string
	Metrics  bool `negatable:""`

	Serve struct{} `cmd:"" default:"1" help:"Run the server."`
	Print struct{} `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Usage struct{} `cmd:"" help:"List every setting with its environment variable."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli, kongprovider.HelpFromTags[AppConfig]("APP"))
	if kctx.Command() == "usage" {
		if err := config.PrintUsage[AppConfig]("APP"); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile(cli.Config),
		config.WithEnvPrefix("APP"),
		kongprovider.WithOverridesFromTags[AppConfig](kctx),
	)
	if err != nil {
		log.Fatal(err)
	}

	switch kctx.Command() {
	case "print":
		if err := config.PrintRedacted(cfg, config.WithWriter(os.Stdout)); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Printf("serving on :%d (log level %s, metrics %v)\n", cfg.Port, cfg.LogLevel, cfg.Metrics)
	}
}
