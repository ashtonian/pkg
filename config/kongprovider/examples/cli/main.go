package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

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

// CLI embeds the config and adds what is the CLI's own: the config file's
// path and the commands.
type CLI struct {
	Config    string `type:"path" default:"config.yaml" help:"Config file."`
	AppConfig `embed:""`

	Serve struct{} `cmd:"" default:"1" help:"Run the server."`
	Print struct{} `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Usage struct{} `cmd:"" help:"List every setting with its environment variable."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig[AppConfig]("APP"))
	if kctx.Command() == "usage" {
		if err := config.PrintUsage[AppConfig]("APP"); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile(cli.Config),
		config.WithEnvPrefix("APP"),
		kongprovider.WithOverridesFromStruct[AppConfig](kctx),
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
		fmt.Printf("serving on :%d (log level %s, db %s, metrics %v)\n", cfg.Port, cfg.LogLevel, cfg.DB.Host, cfg.Metrics)
	}
}
