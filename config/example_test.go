package config_test

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/config"
)

func ExampleGetConfig() {
	type AppConfig struct {
		Host string `koanf:"host" default:"localhost"`
		Port int    `koanf:"port" default:"8080"`
	}

	cfg, err := config.GetConfig[AppConfig](
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%s:%d\n", cfg.Host, cfg.Port)
	// Output: localhost:8080
}

func ExampleGetConfig_withDefaults() {
	type DBConfig struct {
		Host    string `koanf:"host" default:"127.0.0.1"`
		Port    int    `koanf:"port" default:"5432"`
		MaxConn int    `koanf:"maxconn" default:"10"`
	}

	cfg, err := config.GetConfig[DBConfig](
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%s:%d (max %d)\n", cfg.Host, cfg.Port, cfg.MaxConn)
	// Output: 127.0.0.1:5432 (max 10)
}

func ExamplePrintRedacted() {
	type AppConfig struct {
		Host     string `koanf:"host" default:"localhost"`
		Port     int    `koanf:"port" default:"8080"`
		Password string `koanf:"password" sensitive:"true"`
	}

	cfg := &AppConfig{
		Host:     "localhost",
		Port:     8080,
		Password: "secret123",
	}

	var buf bytes.Buffer
	config.PrintRedacted(cfg, config.WithWriter(&buf), config.WithFormat("env"))
	fmt.Print(buf.String())
	// Output:
	// export HOST=localhost
	// export PORT=8080
	// export PASSWORD=[REDACTED]
}

func ExampleGetConfig_requiredFieldMissing() {
	type AppConfig struct {
		APIKey string `koanf:"apikey" validate:"required"`
	}

	_, err := config.GetConfig[AppConfig]()
	fmt.Println(errors.Is(err, config.ErrValidation))
	// Output: true
}
