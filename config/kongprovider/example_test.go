package kongprovider_test

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig is the whole schema: keys, defaults, descriptions and
// validation. Kong makes a flag of each field once a command embeds it.
type AppConfig struct {
	Port    int  `koanf:"port" default:"8080" validate:"min=1,max=65535" short:"p" desc:"HTTP listen port"`
	Metrics bool `koanf:"metrics" default:"true" negatable:"" desc:"Serve /metrics"`
}

type ServeCmd struct {
	AppConfig `embed:""`
}

func (c *ServeCmd) Run() error {
	fmt.Printf("port=%d metrics=%v\n", c.Port, c.Metrics)
	return nil
}

// ExampleLoad loads the config while Kong parses: --no-metrics turns metrics
// off, the untyped --port keeps the value from APP_PORT, and Run's receiver
// holds both.
func ExampleLoad() {
	var cli struct {
		Serve ServeCmd `cmd:""`
	}
	parser, err := kong.New(&cli,
		kongprovider.Load(context.Background(), &cli.Serve.AppConfig, []string{"APP"}, func(*kong.Context) ([]config.Option, error) {
			return []config.Option{config.WithEnviron(func() []string { return []string{"APP_PORT=9000"} })}, nil
		}),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	kctx, err := parser.Parse([]string{"serve", "--no-metrics"})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := kctx.Run(); err != nil {
		fmt.Println(err)
	}

	// Output:
	// port=9000 metrics=false
}

type CLI struct {
	Config    string `type:"path" default:"config.yaml" help:"Config file."`
	AppConfig `embed:""`
}

// ExampleWithOverridesFromStruct wires the same by hand, loading after
// Parse: cli.AppConfig keeps Kong's view, and cfg is the loaded config.
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
	fmt.Printf("loaded: port=%d metrics=%v\n", cfg.Port, cfg.Metrics)
	fmt.Printf("Kong's view: port=%d\n", cli.Port)

	for _, f := range parser.Model.Flags {
		if f.Name == "port" {
			fmt.Println(f.Help)
		}
	}

	// Output:
	// loaded: port=9000 metrics=false
	// Kong's view: port=8080
	// HTTP listen port (config: port; env: APP_PORT)
}
