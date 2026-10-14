package kongprovider_test

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// CLI declares the flags; their defaults, descriptions and validation live
// on AppConfig, so the CLI carries no defaults of its own.
type CLI struct {
	Port    int  `short:"p"`
	Metrics bool `negatable:""`
}

type AppConfig struct {
	Port    int  `koanf:"port" default:"8080" validate:"min=1,max=65535" flag:"port" desc:"HTTP listen port"`
	Metrics bool `koanf:"metrics" default:"true" flag:"metrics" desc:"Serve /metrics"`
}

// ExampleWithOverridesFromTags loads flags typed on the command line above
// the environment: --no-metrics turns metrics off, the untyped --port keeps
// the value from APP_PORT.
func ExampleWithOverridesFromTags() {
	var cli CLI
	parser, err := kong.New(&cli, kongprovider.HelpFromTags[AppConfig]("APP"))
	if err != nil {
		fmt.Println(err)
		return
	}
	kctx, err := parser.Parse([]string{"--no-metrics"})
	if err != nil {
		fmt.Println(err)
		return
	}

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_PORT=9000"} }),
		kongprovider.WithOverridesFromTags[AppConfig](kctx),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("port=%d metrics=%v\n", cfg.Port, cfg.Metrics)

	for _, f := range parser.Model.Flags {
		if f.Name == "port" {
			fmt.Println(f.Help)
		}
	}

	// Output:
	// port=9000 metrics=false
	// HTTP listen port (config: port; env: APP_PORT; default: 8080)
}
