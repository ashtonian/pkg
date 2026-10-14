package kongprovider_test

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig is the whole schema: keys, defaults, descriptions and
// validation. Kong makes a flag of each field once the CLI embeds it.
type AppConfig struct {
	Port    int  `koanf:"port" default:"8080" validate:"min=1,max=65535" short:"p" desc:"HTTP listen port"`
	Metrics bool `koanf:"metrics" default:"true" negatable:"" desc:"Serve /metrics"`
}

type CLI struct {
	Config    string `type:"path" default:"config.yaml" help:"Config file."`
	AppConfig `embed:""`
}

// ExampleWithOverridesFromStruct loads flags typed on the command line above
// the environment: --no-metrics turns metrics off, the untyped --port keeps
// the value from APP_PORT.
func ExampleWithOverridesFromStruct() {
	var cli CLI
	parser, err := kong.New(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
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
		kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
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
	// HTTP listen port (config: port; env: APP_PORT)
}
