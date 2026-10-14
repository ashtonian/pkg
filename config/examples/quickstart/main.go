package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ashtonian/pkg/config"
)

type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" desc:"Database password"`
}

type AppConfig struct {
	Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535" desc:"HTTP listen port"`
	Timeout time.Duration `koanf:"timeout" default:"30s" desc:"Request timeout"`
	DB      DBConfig      `koanf:"db"`
}

func main() {
	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile("config.yaml"), // optional: skipped when absent
		config.WithEnvPrefix("APP"),    // APP_PORT, APP_DB_MAX_CONNS, APP_DB_PASSWORD, ...
	)
	if err != nil {
		log.Fatal(err) // names the failing key, never a secret's value
	}
	if err := config.PrintRedacted(cfg, config.WithWriter(os.Stderr)); err != nil {
		log.Fatal(err)
	}
}
