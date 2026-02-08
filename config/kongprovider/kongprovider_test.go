package kongprovider

import (
	"reflect"
	"testing"

	"github.com/alecthomas/kong"
)

// --- buildFlagMapping Tests ---

func TestBuildFlagMapping_Basic(t *testing.T) {
	type Cfg struct {
		Host string `koanf:"host" flag:"host"`
		Port int    `koanf:"port" flag:"port"`
	}
	mapping := buildFlagMapping[Cfg]()

	want := map[string]string{
		"host": "host",
		"port": "port",
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Errorf("mapping = %v, want %v", mapping, want)
	}
}

func TestBuildFlagMapping_Nested(t *testing.T) {
	type DB struct {
		Host string `koanf:"host" flag:"db-host"`
		Port int    `koanf:"port" flag:"db-port"`
	}
	type Cfg struct {
		Host string `koanf:"host" flag:"host"`
		DB   DB     `koanf:"db"`
	}
	mapping := buildFlagMapping[Cfg]()

	want := map[string]string{
		"host":    "host",
		"db-host": "db.host",
		"db-port": "db.port",
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Errorf("mapping = %v, want %v", mapping, want)
	}
}

func TestBuildFlagMapping_NoFlagTags(t *testing.T) {
	type Cfg struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	mapping := buildFlagMapping[Cfg]()
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for struct with no flag tags, got %v", mapping)
	}
}

func TestBuildFlagMapping_IgnoredField(t *testing.T) {
	type Cfg struct {
		Host    string `koanf:"host" flag:"host"`
		Ignored string `koanf:"-" flag:"ignored"`
	}
	mapping := buildFlagMapping[Cfg]()
	if _, ok := mapping["ignored"]; ok {
		t.Error("ignored field should not appear in mapping")
	}
	if mapping["host"] != "host" {
		t.Errorf("host mapping = %q, want %q", mapping["host"], "host")
	}
}

func TestBuildFlagMapping_NonStruct(t *testing.T) {
	mapping := buildFlagMapping[string]()
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for non-struct, got %v", mapping)
	}
}

func TestBuildFlagMapping_MissingKoanfTag(t *testing.T) {
	type Cfg struct {
		Name string `flag:"name"` // no koanf tag, should use lowercase field name
	}
	mapping := buildFlagMapping[Cfg]()
	if mapping["name"] != "name" {
		t.Errorf("name mapping = %q, want %q (should use lowercase field name)", mapping["name"], "name")
	}
}

// --- Provider Read Tests ---

func TestProvider_Read(t *testing.T) {
	p := &provider{values: map[string]any{"host": "localhost", "port": 8080}}
	m, err := p.Read()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", m["host"])
	}
	if m["port"] != 8080 {
		t.Errorf("port = %v, want 8080", m["port"])
	}
}

func TestProvider_ReadBytes(t *testing.T) {
	p := &provider{values: map[string]any{}}
	_, err := p.ReadBytes()
	if err == nil {
		t.Fatal("ReadBytes should return error")
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
