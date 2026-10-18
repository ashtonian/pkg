package kongprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// --- New: an explicit flag name → key mapping ---

type testCLI struct {
	Host  string `kong:"name='host',default='default-host'"`
	Port  int    `kong:"name='port',default='8080'"`
	Debug bool   `kong:"name='debug',default='false'"`
}

func parseTestCLI(t *testing.T, args []string) *kong.Context {
	t.Helper()
	var cli testCLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	ctx, err := parser.Parse(args)
	require.NoError(t, err)
	return ctx
}

func TestNew_ForwardsOnlyTypedMappedFlags(t *testing.T) {
	m, err := New(parseTestCLI(t, []string{"--host", "custom-host", "--port", "9090"}), map[string]string{"host": "host", "port": "port"}).Read()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"host": "custom-host", "port": 9090}, m)

	m, err = New(parseTestCLI(t, nil), map[string]string{"host": "host", "port": "port", "debug": "debug"}).Read()
	require.NoError(t, err)
	assert.Empty(t, m, "Kong defaults are not typed flags")

	m, err = New(parseTestCLI(t, []string{"--host", "h", "--port", "1"}), map[string]string{"host": "host"}).Read()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"host": "h"}, m, "an unmapped flag is not forwarded")
}

// A flag mapped to a nested key unflattens into the key tree, so it merges
// into the config instead of adding a literal dotted top-level key.
func TestNew_NestedKeyUnflattens(t *testing.T) {
	m, err := New(parseTestCLI(t, []string{"--port", "2000"}), map[string]string{"port": "listeners.tcp"}).Read()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"listeners": map[string]any{"tcp": 2000}}, m)

	type cfg struct {
		Listeners struct {
			TCP int `koanf:"tcp" default:"1883"`
		} `koanf:"listeners"`
	}
	got, err := config.Load[cfg](context.Background(), WithOverrides(parseTestCLI(t, []string{"--port", "2000"}), map[string]string{"port": "listeners.tcp"}))
	require.NoError(t, err)
	assert.Equal(t, 2000, got.Listeners.TCP)
}

func TestNew_UnknownFlagFailsTheLoad(t *testing.T) {
	_, err := config.Load[testCLI](context.Background(),
		WithOverrides(parseTestCLI(t, []string{"--host", "h"}), map[string]string{"hots": "host"}), config.WithSkipValidation())
	require.ErrorIs(t, err, config.ErrSource)
	assert.Contains(t, err.Error(), "--hots")
}

// A flag name means one key per mapping; a CLI whose subcommands give one
// flag different keys chooses the mapping by the selected command.
func TestWithOverrides_PerCommandMapping(t *testing.T) {
	type cli struct {
		Serve struct {
			Port int `help:"Listen port."`
		} `cmd:""`
		Admin struct {
			Port int `help:"Admin port."`
		} `cmd:""`
	}
	type appCfg struct {
		Server struct {
			Port int `koanf:"port" default:"8080"`
		} `koanf:"server"`
		Admin struct {
			Port int `koanf:"port" default:"9090"`
		} `koanf:"admin"`
	}
	for _, tt := range []struct {
		args              []string
		server, adminPort int
	}{
		{[]string{"serve", "--port", "1"}, 1, 9090},
		{[]string{"admin", "--port", "2"}, 8080, 2},
	} {
		var c cli
		parser, err := kong.New(&c)
		require.NoError(t, err)
		kctx, err := parser.Parse(tt.args)
		require.NoError(t, err)

		mapping := map[string]string{"port": "server.port"}
		if kctx.Command() == "admin" {
			mapping = map[string]string{"port": "admin.port"}
		}
		got, err := config.Load[appCfg](context.Background(), WithOverrides(kctx, mapping))
		require.NoError(t, err)
		assert.Equal(t, tt.server, got.Server.Port, tt.args)
		assert.Equal(t, tt.adminPort, got.Admin.Port, tt.args)
	}
}

// --- Spellings Kong accepts ---

type spellingCLI struct {
	Feature bool     `kong:"name='feature',default='true',negatable"`
	Port    int      `kong:"name='port',default='8080',short='p',aliases='listen-port'"`
	Host    string   `kong:"name='host',default='default-host',env='KONGPROVIDER_TEST_HOST'"`
	Rest    []string `kong:"arg,optional"`
}

func parseSpellingCLI(t *testing.T, args ...string) *kong.Context {
	t.Helper()
	var cli spellingCLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	ctx, err := parser.Parse(args)
	require.NoError(t, err)
	return ctx
}

var spellingMapping = map[string]string{"feature": "feature", "port": "port", "host": "host"}

func TestNew_ForwardsEverySpellingOfAFlag(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		key  string
		want any
	}{
		{"negated boolean", []string{"--no-feature"}, "feature", false},
		{"positive boolean", []string{"--feature"}, "feature", true},
		{"long with separate value", []string{"--port", "9001"}, "port", 9001},
		{"long with equals", []string{"--port=9002"}, "port", 9002},
		{"short", []string{"-p", "9003"}, "port", 9003},
		{"alias", []string{"--listen-port", "9004"}, "port", 9004},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values, err := New(parseSpellingCLI(t, tt.args...), spellingMapping).Read()
			require.NoError(t, err)
			assert.Equal(t, tt.want, values[tt.key])
		})
	}
}

// Everything after "--" is a positional argument, even when it looks like a
// flag; Kong's defaults and its env resolution are not user input.
func TestNew_DoesNotForwardUntypedValues(t *testing.T) {
	t.Setenv("KONGPROVIDER_TEST_HOST", "from-env")
	values, err := New(parseSpellingCLI(t, "--", "--port", "1"), spellingMapping).Read()
	require.NoError(t, err)
	assert.Empty(t, values)
}

// --- WithOverridesFromStruct and HelpFromConfig: flags of the config struct ---

type embeddedDB struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	Password string `koanf:"password" default:"dev-pass" kong:"-"`
}

type embeddedCfg struct {
	Port     int               `koanf:"port" default:"8080" short:"p" desc:"HTTP listen port"`
	APIKey   string            `koanf:"api_key" default:"k-123"`
	Metrics  bool              `koanf:"metrics" default:"true" negatable:""`
	Tags     []string          `koanf:"tags" default:"web"`
	DB       embeddedDB        `koanf:"db" embed:"" prefix:"db-"`
	Features map[string]string `koanf:"features" kong:"-"`
}

type embeddingCLI struct {
	Config      string `default:"config.yaml" help:"Config file."`
	embeddedCfg `embed:""`
}

func parseEmbedding(t *testing.T, args ...string) (*embeddingCLI, *kong.Kong, *kong.Context) {
	t.Helper()
	var cli embeddingCLI
	parser, err := kong.New(&cli, HelpFromConfig(&cli.embeddedCfg, "APP"), kong.Exit(func(int) { t.Fatal("kong exited") }))
	require.NoError(t, err)
	kctx, err := parser.Parse(args)
	require.NoError(t, err)
	return &cli, parser, kctx
}

// Flags typed on the command line override the environment, nested ones
// included; Kong's applied defaults and the CLI's own flags are not
// forwarded.
func TestWithOverridesFromStruct_ForwardsTypedFieldFlags(t *testing.T) {
	cli, _, kctx := parseEmbedding(t, "-p", "9000", "--db-host", "db.internal", "--config", "other.yaml")
	cfg, err := config.Load[embeddedCfg](context.Background(),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_PORT=7000", "APP_METRICS=false", "APP_DB_HOST=env-host"} }),
		WithOverridesFromStruct(kctx, &cli.embeddedCfg),
	)
	require.NoError(t, err)
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, "db.internal", cfg.DB.Host)
	assert.False(t, cfg.Metrics, "Kong's default for --metrics must not override APP_METRICS")
	assert.Equal(t, "dev-pass", cfg.DB.Password)
}

// A snapshot with provenance names the flag behind each value it set —
// its canonical name, however it was typed — and the environment variable
// behind the rest.
func TestWithOverridesFromStruct_ProvenanceNamesTheFlag(t *testing.T) {
	cli, _, kctx := parseEmbedding(t, "-p", "9000", "--db-host", "db.internal", "--no-metrics")
	snap, err := config.LoadSnapshot[embeddedCfg](context.Background(),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_API_KEY=k"} }),
		WithOverridesFromStruct(kctx, &cli.embeddedCfg),
	)
	require.NoError(t, err)
	flag := func(name string) config.Origin {
		return config.Origin{Input: config.Source{Kind: config.SourceFlag, Name: name}, Exact: true}
	}
	assert.Equal(t, flag("--port"), snap.Provenance["port"])
	assert.Equal(t, flag("--db-host"), snap.Provenance["db.host"])
	assert.Equal(t, flag("--metrics"), snap.Provenance["metrics"])
	assert.Equal(t, config.Origin{Input: config.Source{Kind: config.SourceEnv, Name: "APP_API_KEY"}, Exact: true}, snap.Provenance["api_key"])
	assert.Equal(t, config.Origin{Input: config.Source{Kind: config.SourceTag}, Exact: true}, snap.Provenance["db.password"])

	mapped := parseTestCLI(t, []string{"--host", "h"})
	snap2, err := config.LoadSnapshot[struct {
		Server struct {
			Host string `koanf:"host"`
		} `koanf:"server"`
	}](context.Background(), WithOverrides(mapped, map[string]string{"host": "server.host"}), config.WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, flag("--host"), snap2.Provenance["server.host"])
}

// A negated flag beats a true value from a lower source.
func TestWithOverridesFromStruct_NegatedFlagBeatsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("metrics: true\n"), 0o600))
	cli, _, kctx := parseEmbedding(t, "--no-metrics")
	cfg, err := config.Load[embeddedCfg](context.Background(), config.WithRequiredFile(path), WithOverridesFromStruct(kctx, &cli.embeddedCfg))
	require.NoError(t, err)
	assert.False(t, cfg.Metrics)
}

// The config struct may be the CLI struct itself.
func TestWithOverridesFromStruct_ConfigIsTheCLI(t *testing.T) {
	var cli embeddedCfg
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"--port", "9100", "--db-host", "h"})
	require.NoError(t, err)
	cfg, err := config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct(kctx, &cli))
	require.NoError(t, err)
	assert.Equal(t, 9100, cfg.Port)
	assert.Equal(t, "h", cfg.DB.Host)
}

// Flags map by the field they write, not by name: a sibling command's flag
// with the name of a config flag does not set the config key.
func TestWithOverridesFromStruct_SiblingFlagWithTheSameName(t *testing.T) {
	type cli struct {
		Serve struct {
			embeddedCfg `embed:""`
		} `cmd:""`
		Admin struct {
			Port int `help:"Admin console port."`
		} `cmd:""`
	}
	var c cli
	parser, err := kong.New(&c)
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"admin", "--port", "1"})
	require.NoError(t, err)
	cfg, err := config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct(kctx, &c.Serve.embeddedCfg))
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port)
}

// The typed values are copied when the option is built: a reloader built
// from it keeps the command line it started with.
func TestWithOverridesFromStruct_CopiesTypedValues(t *testing.T) {
	cli, _, kctx := parseEmbedding(t, "--port", "9000", "--tags", "a,b")
	opt := WithOverridesFromStruct(kctx, &cli.embeddedCfg)
	cli.Port = 1
	cli.Tags[0] = "changed"
	cfg, err := config.Load[embeddedCfg](context.Background(), opt)
	require.NoError(t, err)
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, []string{"a", "b"}, cfg.Tags)
}

// A flag whose target is a pointer is copied through the pointer: a change
// to what it points at after the option is built does not reach the load.
func TestWithOverridesFromStruct_CopiesThroughPointers(t *testing.T) {
	type cfg struct {
		Port *int `koanf:"port" name:"port"`
	}
	var cli struct {
		Config cfg `embed:""`
	}
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"--port", "8080"})
	require.NoError(t, err)
	opt := WithOverridesFromStruct(kctx, &cli.Config)
	*cli.Config.Port = 9090
	snap, err := config.LoadSnapshot[cfg](context.Background(), opt, config.WithSkipValidation())
	require.NoError(t, err)
	require.NotNil(t, snap.Config.Port)
	assert.Equal(t, 8080, *snap.Config.Port)
	assert.Equal(t, config.Source{Kind: config.SourceFlag, Name: "--port"}, snap.Provenance["port"].Input)
}

func TestWithOverridesFromStruct_RootOutsideTheCLIFailsTheLoad(t *testing.T) {
	_, _, kctx := parseEmbedding(t)
	var detached embeddedCfg
	_, err := config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct(kctx, &detached))
	require.ErrorIs(t, err, config.ErrSource)
	assert.Contains(t, err.Error(), "writes a field of")
}

// Config fields are described from the struct; a sensitive default is shown
// neither in the help nor as Kong's placeholder; the CLI's own flags keep
// their help.
func TestHelpFromConfig_DescribesConfigFlags(t *testing.T) {
	_, parser, _ := parseEmbedding(t)
	flags := map[string]*kong.Flag{}
	for _, fl := range parser.Model.Flags {
		flags[fl.Name] = fl
	}
	assert.Equal(t, "HTTP listen port (config: port; env: APP_PORT)", flags["port"].Help)
	assert.Equal(t, "Database host (config: db.host; env: APP_DB_HOST)", flags["db-host"].Help)
	assert.Equal(t, "Config file.", flags["config"].Help)

	apiKey := flags["api-key"]
	assert.Equal(t, "(config: api_key; env: APP_API_KEY)", apiKey.Help)
	assert.Equal(t, "VALUE", apiKey.FormatPlaceHolder())
	assert.Equal(t, "8080", flags["port"].FormatPlaceHolder())
}

// A default that could hold a secret — a map's — reaches neither the help
// nor the placeholder.
func TestHelpFromConfig_WithholdsACompositeDefault(t *testing.T) {
	type cfg struct {
		Level    string            `koanf:"level" desc:"Log level"`
		Settings map[string]string `koanf:"settings" default:"password=FAKE_SECRET_VALUE"`
	}
	var cli cfg
	parser, err := kong.New(&cli, HelpFromConfig(&cli, "APP"))
	require.NoError(t, err)
	for _, fl := range parser.Model.Flags {
		assert.NotContains(t, fl.Help, "FAKE_SECRET_VALUE")
		assert.NotContains(t, fl.FormatPlaceHolder(), "FAKE_SECRET_VALUE")
	}
}

func TestConfigAdaptersRejectInvalidSchemaWithoutMatchingFlags(t *testing.T) {
	type cfg struct {
		Port  int `koanf:"port"`
		Other int `koanf:"PORT"`
	}
	var c cfg
	var cli struct{ File string }
	_, err := kong.New(&cli, HelpFromConfig(&c, "APP"))
	require.ErrorIs(t, err, config.ErrSchema)
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	ctx, err := parser.Parse(nil)
	require.NoError(t, err)
	_, err = config.Load[struct{ File string }](context.Background(), WithOverridesFromStruct(ctx, &c))
	require.ErrorIs(t, err, config.ErrSchema)
	require.ErrorIs(t, err, config.ErrSource)
}
