//go:build unit

package kongprovider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// --- flagMapping Tests ---

func TestFlagMapping_Basic(t *testing.T) {
	type Cfg struct {
		Host string `koanf:"host" flag:"host"`
		Port int    `koanf:"port" flag:"port"`
	}
	mapping := mustFlagMapping[Cfg](t)

	want := map[string]string{
		"host": "host",
		"port": "port",
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Errorf("mapping = %v, want %v", mapping, want)
	}
}

func TestFlagMapping_Nested(t *testing.T) {
	type DB struct {
		Host string `koanf:"host" flag:"db-host"`
		Port int    `koanf:"port" flag:"db-port"`
	}
	type Cfg struct {
		Host string `koanf:"host" flag:"host"`
		DB   DB     `koanf:"db"`
	}
	mapping := mustFlagMapping[Cfg](t)

	want := map[string]string{
		"host":    "host",
		"db-host": "db.host",
		"db-port": "db.port",
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Errorf("mapping = %v, want %v", mapping, want)
	}
}

func TestFlagMapping_NoFlagTags(t *testing.T) {
	type Cfg struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	mapping := mustFlagMapping[Cfg](t)
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for struct with no flag tags, got %v", mapping)
	}
}

func TestFlagMapping_IgnoredField(t *testing.T) {
	type Cfg struct {
		Host    string `koanf:"host" flag:"host"`
		Ignored string `koanf:"-" flag:"ignored"`
	}
	mapping := mustFlagMapping[Cfg](t)
	if _, ok := mapping["ignored"]; ok {
		t.Error("ignored field should not appear in mapping")
	}
	if mapping["host"] != "host" {
		t.Errorf("host mapping = %q, want %q", mapping["host"], "host")
	}
}

func TestFlagMapping_NonStruct(t *testing.T) {
	mapping := mustFlagMapping[string](t)
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for non-struct, got %v", mapping)
	}
}

func TestFlagMapping_MissingKoanfTag(t *testing.T) {
	type Cfg struct {
		Name string `flag:"name"` // no koanf tag, should use lowercase field name
	}
	mapping := mustFlagMapping[Cfg](t)
	if mapping["name"] != "name" {
		t.Errorf("name mapping = %q, want %q (should use lowercase field name)", mapping["name"], "name")
	}
}

// --- Read / unflatten regression tests ---

// TestNew_FlatKeyRead confirms a flat top-level mapping round-trips
// unchanged — the unflatten is a no-op when keys carry no delimiter.
func TestNew_FlatKeyRead(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--host", "localhost", "--port", "8080"})
	p := New(ctx, map[string]string{"host": "host", "port": "port"})
	m, err := p.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", m["host"])
	}
	if m["port"] != 8080 {
		t.Errorf("port = %v, want 8080", m["port"])
	}
}

// TestNew_NestedKeyUnflattens is the regression for a
// bug: a flag mapped to a NESTED koanf path must produce a nested map,
// not a flat dotted key — otherwise koanf never merges it into the
// config tree and the override is silently lost.
func TestNew_NestedKeyUnflattens(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--port", "2000"})
	p := New(ctx, map[string]string{"port": "listeners.tcp"})
	m, err := p.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	listeners, ok := m["listeners"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested listeners map, got %#v", m)
	}
	if listeners["tcp"] != 2000 {
		t.Errorf("listeners.tcp = %v, want 2000", listeners["tcp"])
	}
	if _, flat := m["listeners.tcp"]; flat {
		t.Error("flat dotted key listeners.tcp present — would not merge into the nested koanf tree")
	}
}

// --- Integration Tests with kong ---

type testCLI struct {
	Host  string `kong:"name='host',default='default-host'" koanf:"host" flag:"host"`
	Port  int    `kong:"name='port',default='8080'" koanf:"port" flag:"port"`
	Debug bool   `kong:"name='debug',default='false'" koanf:"debug" flag:"debug"`
}

func parseTestCLI(t *testing.T, args []string) *kong.Context {
	t.Helper()
	var cli testCLI
	parser, err := kong.New(&cli)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("parser.Parse(%v): %v", args, err)
	}
	return ctx
}

func TestNew_ExplicitlySetFlags(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--host", "custom-host", "--port", "9090"})

	mapping := map[string]string{
		"host":  "host",
		"port":  "port",
		"debug": "debug",
	}

	p := New(ctx, mapping)
	values, err := p.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if values["host"] != "custom-host" {
		t.Errorf("host = %v, want custom-host", values["host"])
	}
	if values["port"] != 9090 {
		t.Errorf("port = %v, want 9090", values["port"])
	}
	// debug was not set on command line, so should NOT be in the map
	if _, ok := values["debug"]; ok {
		t.Error("debug should not be in map (not explicitly set)")
	}
}

func TestNew_NoFlagsSet(t *testing.T) {
	ctx := parseTestCLI(t, []string{})

	mapping := map[string]string{
		"host": "host",
		"port": "port",
	}

	p := New(ctx, mapping)
	values, err := p.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(values) != 0 {
		t.Errorf("expected empty map when no flags set, got %v", values)
	}
}

func TestNew_UnmappedFlags(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--host", "test"})

	// Only port is mapped, not host
	mapping := map[string]string{
		"port": "port",
	}

	p := New(ctx, mapping)
	values, err := p.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if _, ok := values["host"]; ok {
		t.Error("host should not be in map (not in mapping)")
	}
}

func TestWithOverridesFromTags(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--host", "tagged-host"})

	cfg, err := config.Load[testCLI](context.Background(), WithOverridesFromTags[testCLI](ctx), config.WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, "tagged-host", cfg.Host)
	assert.Zero(t, cfg.Port, "an untyped flag's Kong default is not forwarded")
}

func mustFlagMapping[T any](t *testing.T) map[string]string {
	t.Helper()
	mapping, err := flagMapping[T]()
	require.NoError(t, err)
	return mapping
}

// A mapping that names a flag the CLI does not have fails the load instead of
// silently never applying.
func TestNew_UnknownFlagFailsTheLoad(t *testing.T) {
	ctx := parseTestCLI(t, []string{"--host", "h"})
	_, err := config.Load[testCLI](context.Background(),
		WithOverrides(ctx, map[string]string{"hots": "host"}), config.WithSkipValidation())
	require.ErrorIs(t, err, config.ErrSource)
	assert.Contains(t, err.Error(), "--hots")
}

func TestWithOverridesFromTags_DuplicateFlagFailsTheLoad(t *testing.T) {
	type cfg struct {
		A string `koanf:"a" flag:"host"`
		B string `koanf:"b" flag:"host"`
	}
	_, err := config.Load[cfg](context.Background(),
		WithOverridesFromTags[cfg](parseTestCLI(t, nil)), config.WithSkipValidation())
	require.ErrorIs(t, err, config.ErrSource)
	assert.Contains(t, err.Error(), `flag "host"`)
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

// HelpFromConfig describes each tagged flag from the config struct: its
// description, key, environment variable and default, a sensitive default
// withheld; a flag the user wrote help for keeps it.
func TestHelpFromConfig_DescribesTaggedFlags(t *testing.T) {
	type cli struct {
		Port     int
		Token    string
		LogLevel string `help:"How much to log."`
		Other    bool
	}
	type appCfg struct {
		Port     int    `koanf:"port" default:"8080" flag:"port" desc:"HTTP listen port"`
		Token    string `koanf:"token" default:"dev-token" flag:"token"`
		LogLevel string `koanf:"log_level" default:"info" flag:"log-level"`
	}
	var c cli
	parser, err := kong.New(&c, HelpFromConfig[appCfg]("APP"))
	require.NoError(t, err)

	help := map[string]string{}
	for _, fl := range parser.Model.Flags {
		help[fl.Name] = fl.Help
	}
	assert.Equal(t, "HTTP listen port (config: port; env: APP_PORT; default: 8080)", help["port"])
	assert.Equal(t, "(config: token; env: APP_TOKEN)", help["token"])
	assert.Equal(t, "How much to log. (config: log_level; env: APP_LOG_LEVEL; default: info)", help["log-level"])
	assert.Empty(t, help["other"])
}

func TestHelpFromConfig_RefusesTagsTheCLILacks(t *testing.T) {
	type cli struct {
		Port int
	}
	type typo struct {
		Port int `koanf:"port" flag:"prot"`
	}
	type dup struct {
		A int `koanf:"a" flag:"port"`
		B int `koanf:"b" flag:"port"`
	}
	var c cli
	_, err := kong.New(&c, HelpFromConfig[typo](""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `flag "prot"`)
	_, err = kong.New(&c, HelpFromConfig[dup](""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `flag "port"`)
}

// TestLoad_NestedCLIOverrideBeatsTagDefault is the end-to-end
// regression: a CLI flag mapped to a nested koanf path must override the
// struct-tag default through config.Load. Before the unflatten fix
// the flat dotted key never merged into the nested tree, so the :1883
// struct-tag default silently won over the --host :2000 override.
func TestLoad_NestedCLIOverrideBeatsTagDefault(t *testing.T) {
	type listenersCfg struct {
		TCP string `koanf:"tcp" default:":1883"`
	}
	type appCfg struct {
		Listeners listenersCfg `koanf:"listeners"`
	}
	ctx := parseTestCLI(t, []string{"--host", ":2000"})
	cfg, err := config.Load[appCfg](
		context.Background(),
		config.WithOverrideProvider(New(ctx, map[string]string{"host": "listeners.tcp"}), nil),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listeners.TCP != ":2000" {
		t.Errorf("listeners.tcp = %q, want %q (CLI override must beat the :1883 tag default)", cfg.Listeners.TCP, ":2000")
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
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("parser.Parse(%v): %v", args, err)
	}
	return ctx
}

var spellingMapping = map[string]string{"feature": "feature", "port": "port", "host": "host"}

func readSpelling(t *testing.T, args ...string) map[string]any {
	t.Helper()
	values, err := New(parseSpellingCLI(t, args...), spellingMapping).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return values
}

func TestNew_ForwardsEverySpellingOfAFlag(t *testing.T) {
	tests := []struct {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := readSpelling(t, tt.args...)
			if got, ok := values[tt.key]; !ok || got != tt.want {
				t.Errorf("values[%q] = %v (present %v), want %v", tt.key, got, ok, tt.want)
			}
		})
	}
}

func TestNew_DoesNotForwardUntypedValues(t *testing.T) {
	t.Setenv("KONGPROVIDER_TEST_HOST", "from-env")

	// Everything after "--" is a positional argument, even when it looks like
	// a flag; Kong's defaults and its env resolution are not user input.
	values := readSpelling(t, "--", "--port", "1")
	if len(values) != 0 {
		t.Errorf("values = %v, want none", values)
	}
}

// End to end: a negated flag must beat a true value from a lower source.
func TestLoad_NegatedFlagBeatsFile(t *testing.T) {
	type appCfg struct {
		Feature bool `koanf:"feature" flag:"feature"`
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("feature: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load[appCfg](context.Background(),
		config.WithRequiredFile(path),
		WithOverridesFromTags[appCfg](parseSpellingCLI(t, "--no-feature")),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Feature {
		t.Error("feature = true, want false: --no-feature must override the file")
	}
}

// A default whose type can hold sensitive entries stays out of the help.
func TestHelpFromConfig_WithholdsCompositeDefault(t *testing.T) {
	type cfg struct {
		Settings map[string]string `koanf:"settings" flag:"settings" default:"{\"password\":\"FAKE_SECRET_VALUE\"}"`
	}
	var cli struct{ Settings map[string]string }
	parser, err := kong.New(&cli, HelpFromConfig[cfg]("APP"))
	require.NoError(t, err)
	for _, fl := range parser.Model.Flags {
		if fl.Name == "settings" {
			assert.Equal(t, "(config: settings; env: APP_SETTINGS)", fl.Help)
		}
	}
}

// A `,remain` element type can hold any key, so a slice default built from
// it stays out of the help.
func TestHelpFromConfig_WithholdsRemainDefault(t *testing.T) {
	type remaining struct {
		Extra map[string]string `koanf:",remain"`
	}
	type cfg struct {
		Rows []remaining `koanf:"rows" flag:"rows" default:"[{\"password\":\"FAKE_SECRET_VALUE\"}]"`
	}
	var cli struct{ Rows string }
	parser, err := kong.New(&cli, HelpFromConfig[cfg]("APP"))
	require.NoError(t, err)
	for _, fl := range parser.Model.Flags {
		if fl.Name == "rows" {
			assert.NotContains(t, fl.Help, "FAKE_SECRET_VALUE")
		}
	}
}

// embeddedCfg is a config struct a CLI embeds: Kong makes a flag of each
// field, and WithOverridesFromStruct maps each by the field it writes.
type embeddedDB struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	Password string `koanf:"password" default:"dev-pass" kong:"-"`
}

type embeddedCfg struct {
	Port     int               `koanf:"port" default:"8080" short:"p" desc:"HTTP listen port"`
	APIKey   string            `koanf:"api_key" default:"k-123"`
	Metrics  bool              `koanf:"metrics" default:"true" negatable:""`
	DB       embeddedDB        `koanf:"db" embed:"" prefix:"db-"`
	Features map[string]string `koanf:"features" kong:"-"`
}

type embeddingCLI struct {
	Config      string `default:"config.yaml" help:"Config file."`
	embeddedCfg `embed:""`
}

func parseEmbedding(t *testing.T, args ...string) (*kong.Kong, *kong.Context) {
	t.Helper()
	var cli embeddingCLI
	parser, err := kong.New(&cli, HelpFromConfig[embeddedCfg]("APP"), kong.Exit(func(int) { t.Fatal("kong exited") }))
	require.NoError(t, err)
	kctx, err := parser.Parse(args)
	require.NoError(t, err)
	return parser, kctx
}

// Flags typed on the command line override the environment, nested ones
// included; Kong's applied defaults and the CLI's own flags are not
// forwarded.
func TestWithOverridesFromStruct_ForwardsTypedFieldFlags(t *testing.T) {
	_, kctx := parseEmbedding(t, "-p", "9000", "--db-host", "db.internal", "--config", "other.yaml")
	cfg, err := config.Load[embeddedCfg](context.Background(),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"APP_PORT=7000", "APP_METRICS=false", "APP_DB_HOST=env-host"} }),
		WithOverridesFromStruct[embeddedCfg](kctx),
	)
	require.NoError(t, err)
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, "db.internal", cfg.DB.Host)
	assert.False(t, cfg.Metrics, "Kong's default for --metrics must not override APP_METRICS")
	assert.Equal(t, "dev-pass", cfg.DB.Password)
}

func TestWithOverridesFromStruct_NegatedFlag(t *testing.T) {
	_, kctx := parseEmbedding(t, "--no-metrics")
	cfg, err := config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct[embeddedCfg](kctx))
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
	cfg, err := config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct[embeddedCfg](kctx))
	require.NoError(t, err)
	assert.Equal(t, 9100, cfg.Port)
	assert.Equal(t, "h", cfg.DB.Host)
}

func TestWithOverridesFromStruct_CLIWithoutTheConfigFailsTheLoad(t *testing.T) {
	var cli struct{ Port int }
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	kctx, err := parser.Parse(nil)
	require.NoError(t, err)
	_, err = config.Load[embeddedCfg](context.Background(), WithOverridesFromStruct[embeddedCfg](kctx))
	require.ErrorIs(t, err, config.ErrSource)
}

// Embedded fields are described like tagged ones; a sensitive default is
// shown neither in the help nor as Kong's placeholder.
func TestHelpFromConfig_DescribesEmbeddedFields(t *testing.T) {
	parser, _ := parseEmbedding(t)
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
	assert.NotContains(t, apiKey.FormatPlaceHolder(), "k-123")
	assert.Equal(t, "8080", flags["port"].FormatPlaceHolder())
}
