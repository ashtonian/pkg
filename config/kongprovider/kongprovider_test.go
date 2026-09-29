//go:build unit

package kongprovider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
)

// --- flagMapping Tests ---

func TestFlagMapping_Basic(t *testing.T) {
	type Cfg struct {
		Host string `koanf:"host" flag:"host"`
		Port int    `koanf:"port" flag:"port"`
	}
	mapping := flagMapping[Cfg]()

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
	mapping := flagMapping[Cfg]()

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
	mapping := flagMapping[Cfg]()
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for struct with no flag tags, got %v", mapping)
	}
}

func TestFlagMapping_IgnoredField(t *testing.T) {
	type Cfg struct {
		Host    string `koanf:"host" flag:"host"`
		Ignored string `koanf:"-" flag:"ignored"`
	}
	mapping := flagMapping[Cfg]()
	if _, ok := mapping["ignored"]; ok {
		t.Error("ignored field should not appear in mapping")
	}
	if mapping["host"] != "host" {
		t.Errorf("host mapping = %q, want %q", mapping["host"], "host")
	}
}

func TestFlagMapping_NonStruct(t *testing.T) {
	mapping := flagMapping[string]()
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for non-struct, got %v", mapping)
	}
}

func TestFlagMapping_MissingKoanfTag(t *testing.T) {
	type Cfg struct {
		Name string `flag:"name"` // no koanf tag, should use lowercase field name
	}
	mapping := flagMapping[Cfg]()
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

	opt := WithOverridesFromTags[testCLI](ctx)
	if opt == nil {
		t.Fatal("WithOverridesFromTags returned nil option")
	}
}

// TestGetConfig_NestedCLIOverrideBeatsTagDefault is the end-to-end
// regression: a CLI flag mapped to a nested koanf path must override the
// struct-tag default through config.GetConfig. Before the unflatten fix
// the flat dotted key never merged into the nested tree, so the :1883
// struct-tag default silently won over the --host :2000 override.
func TestGetConfig_NestedCLIOverrideBeatsTagDefault(t *testing.T) {
	type listenersCfg struct {
		TCP string `koanf:"tcp" default:":1883"`
	}
	type appCfg struct {
		Listeners listenersCfg `koanf:"listeners"`
	}
	ctx := parseTestCLI(t, []string{"--host", ":2000"})
	cfg, err := config.GetConfig[appCfg](
		context.Background(),
		config.WithProvider(New(ctx, map[string]string{"host": "listeners.tcp"})),
	)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
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
func TestGetConfig_NegatedFlagBeatsFile(t *testing.T) {
	type appCfg struct {
		Feature bool `koanf:"feature" flag:"feature"`
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("feature: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.GetConfig[appCfg](context.Background(),
		config.WithMustConfigFile(path),
		WithOverridesFromTags[appCfg](parseSpellingCLI(t, "--no-feature")),
	)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Feature {
		t.Error("feature = true, want false: --no-feature must override the file")
	}
}
