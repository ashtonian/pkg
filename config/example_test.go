package config_test

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/config"
)

func ExampleGetConfig() {
	type AppConfig struct {
		Host string `config:"HOST,localhost"`
		Port int    `config:"PORT,8080"`
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
		Host    string `config:"HOST"`
		Port    int    `config:"PORT"`
		MaxConn int    `config:"MAX_CONN"`
	}

	cfg, err := config.GetConfig[DBConfig](
		config.WithEnvPrefix("DB"),
		config.WithDefaults(map[string]any{
			"DB_HOST":     "127.0.0.1",
			"DB_PORT":     "5432",
			"DB_MAX_CONN": "10",
		}),
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%s:%d (max %d)\n", cfg.Host, cfg.Port, cfg.MaxConn)
	// Output: 127.0.0.1:5432 (max 10)
}

func ExampleLoader_Print() {
	type AppConfig struct {
		Host     string `config:"HOST,localhost"`
		Port     int    `config:"PORT,8080"`
		Password string `config:"PASSWORD,secret123" sensitive:"true"`
	}

	loader, err := config.NewLoader[AppConfig](
		config.WithSkipValidation(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	_, err = loader.Load()
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	var buf bytes.Buffer
	loader.Print(config.WithWriter(&buf), config.WithFormat("env"))
	output := buf.String()
	fmt.Println(output)
	// Output:
	// export HOST=localhost
	// export PORT=8080
	// export PASSWORD=[REDACTED]
}

func ExampleGetConfig_requiredFieldMissing() {
	type AppConfig struct {
		APIKey string `config:"API_KEY" required:"true"`
	}

	_, err := config.GetConfig[AppConfig](
		config.WithSkipValidation(),
	)
	fmt.Println(errors.Is(err, config.ErrRequired))
	// Output: true
}
