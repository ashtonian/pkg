//go:build unit

package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/confmap"
)

// mapPopulator returns a SecretPopulator that walks `secret:"key"` tags on
// target's exported fields and assigns string values from the supplied map.
// Mirrors a secret store's populate call for the subset of types the test
// suite exercises (string fields only).
func mapPopulator(secrets map[string]string) SecretPopulator {
	return func(_ context.Context, target any) error {
		v := reflect.ValueOf(target)
		if v.Kind() != reflect.Ptr || v.IsNil() {
			return fmt.Errorf("mapPopulator: target must be non-nil pointer to struct")
		}
		v = v.Elem()
		if v.Kind() != reflect.Struct {
			return fmt.Errorf("mapPopulator: target must point to struct")
		}
		t := v.Type()
		for i := range t.NumField() {
			tag := t.Field(i).Tag.Get("secret")
			if tag == "" || tag == "-" {
				continue
			}
			val, ok := secrets[tag]
			if !ok {
				return fmt.Errorf("secret %q not found", tag)
			}
			f := v.Field(i)
			if f.Kind() != reflect.String || !f.CanSet() {
				return fmt.Errorf("mapPopulator: unsupported or unsettable field %s", t.Field(i).Name)
			}
			f.SetString(val)
		}
		return nil
	}
}

// --- Test config structs (koanf tags) ---

type BasicConfig struct {
	Host    string        `koanf:"host" default:"localhost"`
	Port    int           `koanf:"port" default:"8080"`
	Debug   bool          `koanf:"debug" default:"false"`
	Timeout time.Duration `koanf:"timeout" default:"30s"`
}

type SensitiveConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	Password string `koanf:"password" sensitive:"true"`
	APIKey   string `koanf:"apikey"` // auto-detected sensitive (contains "key")
	Token    string `koanf:"token"`  // auto-detected sensitive
	Normal   string `koanf:"normal" default:"hello"`
}

type NestedConfig struct {
	Server   ServerSubConfig   `koanf:"server"`
	Database DatabaseSubConfig `koanf:"database"`
}

type ServerSubConfig struct {
	Host string `koanf:"host" default:"0.0.0.0"`
	Port int    `koanf:"port" default:"8080"`
}

type DatabaseSubConfig struct {
	DSN      string `koanf:"dsn" validate:"required"`
	MaxConns int    `koanf:"maxconns" default:"25"`
}

type SecretConfig struct {
	DBPassword string `koanf:"dbpassword" secret:"db/password" sensitive:"true"`
	APIKey     string `koanf:"apikey" secret:"api/key"`
}

type ValidationConfig struct {
	Port    int    `koanf:"port" default:"8080" validate:"min=1,max=65535"`
	Workers int    `koanf:"workers" default:"4" validate:"min=1,max=100"`
	Email   string `koanf:"email" validate:"omitempty,email"`
}

type AllTypesConfig struct {
	Str     string        `koanf:"str" default:"hello"`
	Bool    bool          `koanf:"bool" default:"true"`
	Int     int           `koanf:"int" default:"42"`
	Float64 float64       `koanf:"float64" default:"2.5"`
	Dur     time.Duration `koanf:"dur" default:"5s"`
}

type IgnoredFieldConfig struct {
	Host    string `koanf:"host" default:"localhost"`
	Ignored string `koanf:"-"`
}

type RequiredFieldConfig struct {
	Name string `koanf:"name" validate:"required"`
	Port int    `koanf:"port" default:"8080"`
}

type FlagConfig struct {
	Host string `koanf:"host" default:"localhost" flag:"host"`
	Port int    `koanf:"port" default:"8080" flag:"port"`
	DB   FlagDB `koanf:"db"`
}

type FlagDB struct {
	Host string `koanf:"host" default:"localhost" flag:"db-host"`
	Port int    `koanf:"port" default:"5432" flag:"db-port"`
}

// --- DefaultsFromTags Tests ---

func TestDefaultsFromTags_Basic(t *testing.T) {
	defaults := DefaultsFromTags[BasicConfig]()

	if defaults["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", defaults["host"])
	}
	if defaults["port"] != "8080" {
		t.Errorf("port = %v, want 8080", defaults["port"])
	}
	if defaults["debug"] != "false" {
		t.Errorf("debug = %v, want false", defaults["debug"])
	}
	if defaults["timeout"] != "30s" {
		t.Errorf("timeout = %v, want 30s", defaults["timeout"])
	}
}

func TestDefaultsFromTags_Nested(t *testing.T) {
	type Inner struct {
		Val string `koanf:"val" default:"inner-default"`
	}
	type Outer struct {
		Name  string `koanf:"name" default:"outer-name"`
		Inner Inner  `koanf:"inner"`
	}

	defaults := DefaultsFromTags[Outer]()

	if defaults["name"] != "outer-name" {
		t.Errorf("name = %v, want outer-name", defaults["name"])
	}
	if defaults["inner.val"] != "inner-default" {
		t.Errorf("inner.val = %v, want inner-default", defaults["inner.val"])
	}
}

func TestDefaultsFromTags_NoDefaults(t *testing.T) {
	type NoDefaults struct {
		Name string `koanf:"name"`
	}
	defaults := DefaultsFromTags[NoDefaults]()
	if len(defaults) != 0 {
		t.Errorf("expected empty map, got %v", defaults)
	}
}

func TestDefaultsFromTags_NonStruct(t *testing.T) {
	defaults := DefaultsFromTags[string]()
	if len(defaults) != 0 {
		t.Errorf("expected empty map for non-struct, got %v", defaults)
	}
}

func TestDefaultsFromTags_IgnoredField(t *testing.T) {
	defaults := DefaultsFromTags[IgnoredFieldConfig]()
	if _, ok := defaults["-"]; ok {
		t.Error("ignored field should not appear in defaults")
	}
	if defaults["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", defaults["host"])
	}
}

// --- GetConfig Tests ---

func TestGetConfig_Defaults(t *testing.T) {
	cfg, err := Load[BasicConfig](t.Context(), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want %d", cfg.Port, 8080)
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want false", cfg.Debug)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
}

func TestGetConfig_AllTypes(t *testing.T) {
	cfg, err := Load[AllTypesConfig](t.Context(), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Str != "hello" {
		t.Errorf("Str = %q, want %q", cfg.Str, "hello")
	}
	if cfg.Bool != true {
		t.Errorf("Bool = %v, want true", cfg.Bool)
	}
	if cfg.Int != 42 {
		t.Errorf("Int = %d, want 42", cfg.Int)
	}
	if cfg.Float64 != 2.5 {
		t.Errorf("Float64 = %f, want 2.5", cfg.Float64)
	}
	if cfg.Dur != 5*time.Second {
		t.Errorf("Dur = %v, want 5s", cfg.Dur)
	}
}

func TestGetConfig_EnvVars(t *testing.T) {
	t.Setenv("APP_HOST", "api.example.com")
	t.Setenv("APP_PORT", "9000")
	t.Setenv("APP_DEBUG", "true")
	t.Setenv("APP_TIMEOUT", "10s")

	cfg, err := Load[BasicConfig](t.Context(),
		WithEnvPrefix("APP"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "api.example.com" {
		t.Errorf("Host = %q, want %q", cfg.Host, "api.example.com")
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %d, want %d", cfg.Port, 9000)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", cfg.Timeout)
	}
}

func TestGetConfig_EnvOverridesFile(t *testing.T) {
	yamlContent := "host: from-file\nport: 3000\n"
	path := writeYAML(t, yamlContent)

	t.Setenv("TEST_HOST", "from-env")

	cfg, err := Load[BasicConfig](t.Context(),
		WithFile(path),
		WithEnvPrefix("TEST"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "from-env" {
		t.Errorf("Host = %q, want %q (env should override file)", cfg.Host, "from-env")
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want %d (from file)", cfg.Port, 3000)
	}
}

func TestGetConfig_FileOverridesDefault(t *testing.T) {
	yamlContent := "host: from-file\nport: 3000\n"
	path := writeYAML(t, yamlContent)

	cfg, err := Load[BasicConfig](t.Context(),
		WithFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "from-file" {
		t.Errorf("Host = %q, want %q", cfg.Host, "from-file")
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want %d", cfg.Port, 3000)
	}
}

// --- Nested Config Tests ---

func TestGetConfig_NestedStruct(t *testing.T) {
	yamlContent := `server:
  host: 10.0.0.1
  port: 9090
database:
  dsn: postgres://localhost/test
  maxconns: 50
`
	path := writeYAML(t, yamlContent)

	cfg, err := Load[NestedConfig](t.Context(),
		WithFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Host != "10.0.0.1" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "10.0.0.1")
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 9090)
	}
	if cfg.Database.DSN != "postgres://localhost/test" {
		t.Errorf("Database.DSN = %q, want %q", cfg.Database.DSN, "postgres://localhost/test")
	}
	if cfg.Database.MaxConns != 50 {
		t.Errorf("Database.MaxConns = %d, want %d", cfg.Database.MaxConns, 50)
	}
}

func TestGetConfig_NestedWithEnvPrefix(t *testing.T) {
	t.Setenv("APP_SERVER_HOST", "10.0.0.1")
	t.Setenv("APP_SERVER_PORT", "9090")
	t.Setenv("APP_DATABASE_DSN", "postgres://localhost/test")

	cfg, err := Load[NestedConfig](t.Context(),
		WithEnvPrefix("APP"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Host != "10.0.0.1" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "10.0.0.1")
	}
	if cfg.Database.DSN != "postgres://localhost/test" {
		t.Errorf("Database.DSN = %q, want %q", cfg.Database.DSN, "postgres://localhost/test")
	}
}

// --- Secret Provider Tests ---

func TestGetConfig_SecretPopulator(t *testing.T) {
	populator := mapPopulator(map[string]string{
		"db/password": "supersecret",
		"api/key":     "key-123",
	})

	cfg, err := Load[SecretConfig](t.Context(),
		WithSecretPopulator(populator),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBPassword != "supersecret" {
		t.Errorf("DBPassword = %q, want %q", cfg.DBPassword, "supersecret")
	}
	if cfg.APIKey != "key-123" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "key-123")
	}
}

func TestGetConfig_SecretPopulator_Error(t *testing.T) {
	populator := mapPopulator(map[string]string{})

	_, err := Load[SecretConfig](t.Context(),
		WithSecretPopulator(populator),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
	if !errors.Is(err, ErrSecretProvider) {
		t.Errorf("expected ErrSecretProvider, got: %v", err)
	}
}

// --- Validation Tests ---

func TestGetConfig_Validation_Pass(t *testing.T) {
	cfg, err := Load[ValidationConfig](t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestGetConfig_Validation_Fail(t *testing.T) {
	t.Setenv("APP_PORT", "0")

	_, err := Load[ValidationConfig](t.Context(), WithEnvPrefix("APP"))
	if err == nil {
		t.Fatal("expected validation error for Port=0")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("expected ErrValidation, got: %v", err)
	}
}

func TestGetConfig_Validation_Required(t *testing.T) {
	_, err := Load[RequiredFieldConfig](t.Context())
	if err == nil {
		t.Fatal("expected validation error for missing required field")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("expected ErrValidation, got: %v", err)
	}
}

func TestGetConfig_Validation_Required_Set(t *testing.T) {
	t.Setenv("APP_NAME", "test-app")

	cfg, err := Load[RequiredFieldConfig](t.Context(), WithEnvPrefix("APP"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "test-app" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-app")
	}
}

func TestGetConfig_Validation_Skip(t *testing.T) {
	t.Setenv("APP_PORT", "0")

	cfg, err := Load[ValidationConfig](t.Context(),
		WithEnvPrefix("APP"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0", cfg.Port)
	}
}

// --- File Source Tests ---

func TestGetConfig_YAMLFile(t *testing.T) {
	yamlContent := "host: yaml-host\nport: 7777\ndebug: true\ntimeout: 15s\n"
	path := writeYAML(t, yamlContent)

	cfg, err := Load[BasicConfig](t.Context(),
		WithFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "yaml-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "yaml-host")
	}
	if cfg.Port != 7777 {
		t.Errorf("Port = %d, want %d", cfg.Port, 7777)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want 15s", cfg.Timeout)
	}
}

func TestGetConfig_YAMLFile_NestedStruct(t *testing.T) {
	yamlContent := `server:
  host: yaml-server
  port: 3000
database:
  dsn: postgres://yaml/db
  maxconns: 10
`
	path := writeYAML(t, yamlContent)

	cfg, err := Load[NestedConfig](t.Context(),
		WithFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Host != "yaml-server" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "yaml-server")
	}
	if cfg.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 3000)
	}
	if cfg.Database.DSN != "postgres://yaml/db" {
		t.Errorf("Database.DSN = %q, want %q", cfg.Database.DSN, "postgres://yaml/db")
	}
	if cfg.Database.MaxConns != 10 {
		t.Errorf("Database.MaxConns = %d, want %d", cfg.Database.MaxConns, 10)
	}
}

func TestGetConfig_MissingOptionalFile(t *testing.T) {
	cfg, err := Load[BasicConfig](t.Context(),
		WithFile("/nonexistent/config.yaml"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("optional missing file should not error: %v", err)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want default %q", cfg.Host, "localhost")
	}
}

func TestGetConfig_MissingRequiredFile(t *testing.T) {
	_, err := Load[BasicConfig](t.Context(),
		WithRequiredFile("/nonexistent/config.yaml"),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for missing required file")
	}
	if !errors.Is(err, ErrFileRead) {
		t.Errorf("expected ErrFileRead, got: %v", err)
	}
}

func TestGetConfig_MultipleFiles_MergedLeftToRight(t *testing.T) {
	base := writeYAML(t, "host: base-host\nport: 1111\n")
	override := writeYAML(t, "port: 2222\n")

	cfg, err := Load[BasicConfig](t.Context(),
		WithFiles(base, override),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "base-host" {
		t.Errorf("Host = %q, want %q (from base)", cfg.Host, "base-host")
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want %d (from override)", cfg.Port, 2222)
	}
}

func TestGetConfig_MustConfigFiles(t *testing.T) {
	f := writeYAML(t, "host: ok\n")
	_, err := Load[BasicConfig](t.Context(),
		WithRequiredFiles(f, "/nonexistent/config.yaml"),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for missing required file")
	}
	if !errors.Is(err, ErrFileRead) {
		t.Errorf("expected ErrFileRead, got: %v", err)
	}
}

// --- Ignored Field Tests ---

func TestGetConfig_IgnoredField(t *testing.T) {
	cfg, err := Load[IgnoredFieldConfig](t.Context(), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Ignored != "" {
		t.Errorf("Ignored = %q, want empty", cfg.Ignored)
	}
}

// --- Programmatic Defaults Tests ---

func TestGetConfig_ProgrammaticDefaults(t *testing.T) {
	cfg, err := Load[BasicConfig](t.Context(),
		WithDefaults(map[string]any{
			"host": "prog-host",
			"port": 5555,
		}),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "prog-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "prog-host")
	}
	if cfg.Port != 5555 {
		t.Errorf("Port = %d, want %d", cfg.Port, 5555)
	}
}

func TestGetConfig_ProgrammaticDefaults_FileOverrides(t *testing.T) {
	path := writeYAML(t, "host: from-file\n")

	cfg, err := Load[BasicConfig](t.Context(),
		WithDefaults(map[string]any{
			"host": "prog-host",
			"port": 5555,
		}),
		WithFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "from-file" {
		t.Errorf("Host = %q, want %q (file should override programmatic default)", cfg.Host, "from-file")
	}
	if cfg.Port != 5555 {
		t.Errorf("Port = %d, want %d (from programmatic default)", cfg.Port, 5555)
	}
}

// --- Precedence Integration Tests ---

func TestGetConfig_FullPrecedence(t *testing.T) {
	path := writeYAML(t, "host: file-host\n")
	t.Setenv("TEST_HOST", "env-host")

	cfg, err := Load[BasicConfig](t.Context(),
		WithFile(path),
		WithEnvPrefix("TEST"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "env-host" {
		t.Errorf("Host = %q, want %q (env should win over file and default)", cfg.Host, "env-host")
	}
}

func TestGetConfig_Precedence_ProgrammaticOverStructDefault(t *testing.T) {
	cfg, err := Load[BasicConfig](t.Context(),
		WithDefaults(map[string]any{
			"host": "prog-host",
		}),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "prog-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "prog-host")
	}
}

func TestGetConfig_Precedence_ProviderOverEnv(t *testing.T) {
	t.Setenv("TEST_HOST", "env-host")

	extraProvider := confmap.Provider(map[string]any{
		"host": "provider-host",
	}, ".")

	cfg, err := Load[BasicConfig](t.Context(),
		WithEnvPrefix("TEST"),
		WithOverrideProvider(extraProvider, nil),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "provider-host" {
		t.Errorf("Host = %q, want %q (extra provider should win over env)", cfg.Host, "provider-host")
	}
}

// --- WithProvider Tests ---

func TestGetConfig_WithProvider(t *testing.T) {
	p := confmap.Provider(map[string]any{
		"host": "from-provider",
	}, ".")

	cfg, err := Load[BasicConfig](t.Context(),
		WithOverrideProvider(p, nil),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "from-provider" {
		t.Errorf("Host = %q, want %q", cfg.Host, "from-provider")
	}
}

func TestGetConfig_MultipleProviders(t *testing.T) {
	p1 := confmap.Provider(map[string]any{
		"host": "from-p1",
		"port": 1111,
	}, ".")
	p2 := confmap.Provider(map[string]any{
		"port": 2222,
	}, ".")

	cfg, err := Load[BasicConfig](t.Context(),
		WithOverrideProvider(p1, nil),
		WithOverrideProvider(p2, nil),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "from-p1" {
		t.Errorf("Host = %q, want %q", cfg.Host, "from-p1")
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want %d (p2 should override p1)", cfg.Port, 2222)
	}
}

// --- PrintRedacted Tests ---

func TestPrintRedacted_Table(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:     "localhost",
		Password: "secret123",
		APIKey:   "key-abc",
		Token:    "tok-xyz",
		Normal:   "hello",
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "FIELD") {
		t.Error("table should contain header")
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Error("sensitive fields should be redacted")
	}
	if !strings.Contains(output, "localhost") {
		t.Error("normal values should appear")
	}
	if strings.Contains(output, "secret123") {
		t.Error("password should be redacted")
	}
}

func TestPrintRedacted_JSON(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:     "localhost",
		Password: "secret123",
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithFormat("json"), WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "[REDACTED]") {
		t.Error("JSON should redact sensitive fields")
	}
	if strings.Contains(output, "secret123") {
		t.Error("password should be redacted in JSON")
	}
}

func TestPrintRedacted_Env(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:   "localhost",
		Normal: "hello",
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithFormat("env"), WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "export HOST='localhost'") {
		t.Errorf("env format should contain a quoted export, got:\n%s", output)
	}
}

// Env output is shell-safe: values are single-quoted (quotes inside escaped)
// and names hold only [A-Z0-9_], map keys and indexes included.
func TestPrintRedacted_EnvIsShellSafe(t *testing.T) {
	var buf bytes.Buffer
	cfg := struct {
		Name   string
		Quote  string
		Labels map[string]string
	}{"two words", "it's", map[string]string{"team-a.b": "x"}}
	if err := PrintRedacted(cfg, WithFormat(FormatEnv), WithWriter(&buf), WithSortByName()); err != nil {
		t.Fatal(err)
	}
	want := "export LABELS_TEAM_A_B_='x'\nexport NAME='two words'\nexport QUOTE='it'\\''s'\n"
	if buf.String() != want {
		t.Fatalf("env output:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestPrintRedacted_ShowSensitive(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:     "localhost",
		Password: "mypass",
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf), WithShowSensitive(true)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "mypass") {
		t.Error("ShowSensitive should reveal password")
	}
}

func TestPrintRedacted_SortByName(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:   "localhost",
		APIKey: "key",
		Normal: "hello",
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf), WithSortByName()); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	apikeyIdx := strings.Index(output, "apikey")
	hostIdx := strings.Index(output, "host")
	if apikeyIdx < 0 || hostIdx < 0 {
		t.Fatalf("expected both apikey and host in output:\n%s", output)
	}
	if apikeyIdx > hostIdx {
		t.Error("apikey should appear before host when sorted")
	}
}

// --- RedactedMap Tests ---

func TestRedactedMap(t *testing.T) {
	cfg := &SensitiveConfig{
		Host:     "localhost",
		Password: "secret123",
		Normal:   "hello",
	}

	m := RedactedMap(cfg)
	if m["password"] != "[REDACTED]" {
		t.Errorf("password = %v, want [REDACTED]", m["password"])
	}
	if m["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", m["host"])
	}
}

// --- Sensitive Auto-Detection Tests ---

func TestSensitiveAutoDetection(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		expected bool
	}{
		{"password field", "Password", true},
		{"api key field", "APIKey", true},
		{"token field", "AccessToken", true},
		{"secret field", "ClientSecret", true},
		{"normal field", "Host", false},
		{"normal field 2", "Port", false},
		{"credential field", "Credential", true},
		{"auth field", "AuthHeader", true},
		{"private field", "PrivateKey", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSensitiveName(tt.field)
			if got != tt.expected {
				t.Errorf("isSensitiveName(%q) = %v, want %v", tt.field, got, tt.expected)
			}
		})
	}
}

// --- Custom Validator Tests ---

func TestGetConfig_CustomValidator(t *testing.T) {
	cfg, err := Load[ValidationConfig](t.Context(),
		WithValidator(nil), // nil falls back to default
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

// --- Error Sentinel Tests ---

// The categories are distinct sentinels, and the two reload refusals are not
// reload failures.
func TestErrors_AreDistinctSentinels(t *testing.T) {
	all := []error{ErrValidation, ErrFileRead, ErrFileParse, ErrSecretProvider, ErrPostLoad,
		ErrSource, ErrReload, ErrReloadInProgress, ErrReloaderStopped}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}

// --- Complex Type Tests: Slices ---

func TestGetConfig_YAMLArray(t *testing.T) {
	yamlContent := "tags:\n  - a\n  - b\n  - c\n"
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Tags []string `koanf:"tags"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Tags) != 3 {
		t.Fatalf("Tags length = %d, want 3", len(c.Tags))
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if c.Tags[i] != w {
			t.Errorf("Tags[%d] = %q, want %q", i, c.Tags[i], w)
		}
	}
}

func TestGetConfig_SliceFromEnvVar(t *testing.T) {
	t.Setenv("APP_TAGS", "x,y,z")

	type cfg struct {
		Tags []string `koanf:"tags"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Tags) != 3 {
		t.Fatalf("Tags length = %d, want 3", len(c.Tags))
	}
	for i, want := range []string{"x", "y", "z"} {
		if c.Tags[i] != want {
			t.Errorf("Tags[%d] = %q, want %q", i, c.Tags[i], want)
		}
	}
}

func TestGetConfig_SliceFromDefault(t *testing.T) {
	type cfg struct {
		Tags []string `koanf:"tags" default:"a,b,c"`
	}

	c, err := Load[cfg](t.Context(), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Tags) != 3 {
		t.Fatalf("Tags length = %d, want 3", len(c.Tags))
	}
	for i, want := range []string{"a", "b", "c"} {
		if c.Tags[i] != want {
			t.Errorf("Tags[%d] = %q, want %q", i, c.Tags[i], want)
		}
	}
}

func TestGetConfig_SliceEnvOverridesDefault(t *testing.T) {
	t.Setenv("APP_TAGS", "x,y")

	type cfg struct {
		Tags []string `koanf:"tags" default:"a,b,c"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Tags) != 2 {
		t.Fatalf("Tags length = %d, want 2", len(c.Tags))
	}
	for i, want := range []string{"x", "y"} {
		if c.Tags[i] != want {
			t.Errorf("Tags[%d] = %q, want %q", i, c.Tags[i], want)
		}
	}
}

func TestGetConfig_IntSlice(t *testing.T) {
	yamlContent := "ports:\n  - 8080\n  - 9090\n"
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Ports []int `koanf:"ports"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Ports) != 2 {
		t.Fatalf("Ports length = %d, want 2", len(c.Ports))
	}
	if c.Ports[0] != 8080 || c.Ports[1] != 9090 {
		t.Errorf("Ports = %v, want [8080 9090]", c.Ports)
	}
}

func TestGetConfig_SliceOfStructs(t *testing.T) {
	yamlContent := "servers:\n  - host: a.com\n    port: 1\n  - host: b.com\n    port: 2\n"
	path := writeYAML(t, yamlContent)

	type server struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	type cfg struct {
		Servers []server `koanf:"servers"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Servers) != 2 {
		t.Fatalf("Servers length = %d, want 2", len(c.Servers))
	}
	if c.Servers[0].Host != "a.com" || c.Servers[0].Port != 1 {
		t.Errorf("Servers[0] = %+v, want {a.com 1}", c.Servers[0])
	}
	if c.Servers[1].Host != "b.com" || c.Servers[1].Port != 2 {
		t.Errorf("Servers[1] = %+v, want {b.com 2}", c.Servers[1])
	}
}

// --- Complex Type Tests: Maps ---

func TestGetConfig_YAMLMap(t *testing.T) {
	yamlContent := "labels:\n  env: prod\n  region: us\n"
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Labels map[string]string `koanf:"labels"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Labels) != 2 {
		t.Fatalf("Labels length = %d, want 2", len(c.Labels))
	}
	if c.Labels["env"] != "prod" {
		t.Errorf("Labels[env] = %q, want prod", c.Labels["env"])
	}
	if c.Labels["region"] != "us" {
		t.Errorf("Labels[region] = %q, want us", c.Labels["region"])
	}
}

func TestGetConfig_MapFromEnvVar(t *testing.T) {
	// koanf env provider maps APP_LABELS_ENV to koanf key "labels.env"
	// which koanf stores as a nested map, then unmarshals to map[string]string.
	t.Setenv("APP_LABELS_ENV", "prod")
	t.Setenv("APP_LABELS_REGION", "eu")

	type cfg struct {
		Labels map[string]string `koanf:"labels"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Labels) != 2 {
		t.Fatalf("Labels length = %d, want 2", len(c.Labels))
	}
	if c.Labels["env"] != "prod" {
		t.Errorf("Labels[env] = %q, want prod", c.Labels["env"])
	}
	if c.Labels["region"] != "eu" {
		t.Errorf("Labels[region] = %q, want eu", c.Labels["region"])
	}
}

func TestGetConfig_MapOfSlices(t *testing.T) {
	yamlContent := "routes:\n  api:\n    - /v1\n    - /v2\n  admin:\n    - /dashboard\n"
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Routes map[string][]string `koanf:"routes"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Routes) != 2 {
		t.Fatalf("Routes length = %d, want 2", len(c.Routes))
	}
	if len(c.Routes["api"]) != 2 {
		t.Errorf("Routes[api] length = %d, want 2", len(c.Routes["api"]))
	}
	if len(c.Routes["admin"]) != 1 {
		t.Errorf("Routes[admin] length = %d, want 1", len(c.Routes["admin"]))
	}
}

func TestGetConfig_MapFromProgrammaticDefaults(t *testing.T) {
	type cfg struct {
		Labels map[string]string `koanf:"labels"`
	}

	c, err := Load[cfg](t.Context(),
		WithDefaults(map[string]any{
			"labels": map[string]any{
				"env":    "dev",
				"region": "local",
			},
		}),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Labels["env"] != "dev" {
		t.Errorf("Labels[env] = %q, want dev", c.Labels["env"])
	}
	if c.Labels["region"] != "local" {
		t.Errorf("Labels[region] = %q, want local", c.Labels["region"])
	}
}

// --- Complex Type Tests: Pointers ---

func TestGetConfig_PointerFields(t *testing.T) {
	yamlContent := "name: hello\ncount: 42\n"
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Name  *string `koanf:"name"`
		Count *int    `koanf:"count"`
		Opt   *string `koanf:"opt"` // not set
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name == nil || *c.Name != "hello" {
		t.Errorf("Name = %v, want *hello", c.Name)
	}
	if c.Count == nil || *c.Count != 42 {
		t.Errorf("Count = %v, want *42", c.Count)
	}
	if c.Opt != nil {
		t.Errorf("Opt = %v, want nil", c.Opt)
	}
}

func TestGetConfig_PointerFieldsFromEnv(t *testing.T) {
	t.Setenv("APP_NAME", "from-env")
	t.Setenv("APP_COUNT", "99")

	type cfg struct {
		Name  *string `koanf:"name"`
		Count *int    `koanf:"count"`
		Opt   *string `koanf:"opt"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name == nil || *c.Name != "from-env" {
		t.Errorf("Name = %v, want *from-env", c.Name)
	}
	if c.Count == nil || *c.Count != 99 {
		t.Errorf("Count = %v, want *99", c.Count)
	}
	if c.Opt != nil {
		t.Errorf("Opt = %v, want nil", c.Opt)
	}
}

func TestGetConfig_PointerToStruct(t *testing.T) {
	yamlContent := "db:\n  host: dbhost\n  port: 5432\n"
	path := writeYAML(t, yamlContent)

	type dbCfg struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	type cfg struct {
		DB    *dbCfg `koanf:"db"`
		Cache *dbCfg `koanf:"cache"` // not set
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DB == nil {
		t.Fatal("DB should not be nil")
	}
	if c.DB.Host != "dbhost" || c.DB.Port != 5432 {
		t.Errorf("DB = %+v, want {Host:dbhost Port:5432}", c.DB)
	}
}

func TestGetConfig_PointerToStructWithDefaults(t *testing.T) {
	type inner struct {
		Host string `koanf:"host" default:"defaulthost"`
		Port int    `koanf:"port" default:"3000"`
	}
	type cfg struct {
		Server *inner `koanf:"server"`
	}

	c, err := Load[cfg](t.Context(), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Pointer struct should be populated from defaults.
	if c.Server == nil {
		t.Fatal("Server should not be nil when defaults exist")
	}
	if c.Server.Host != "defaulthost" {
		t.Errorf("Server.Host = %q, want defaulthost", c.Server.Host)
	}
	if c.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want 3000", c.Server.Port)
	}
}

// --- Complex Type Tests: Mixed ---

func TestGetConfig_ComplexNestedConfig(t *testing.T) {
	yamlContent := `
app:
  name: myapp
  debug: true
  tags:
    - web
    - api
  labels:
    env: staging
    team: platform
  servers:
    - host: primary.com
      port: 443
    - host: backup.com
      port: 8443
`
	path := writeYAML(t, yamlContent)

	type server struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	type appCfg struct {
		Name    string            `koanf:"name"`
		Debug   bool              `koanf:"debug"`
		Tags    []string          `koanf:"tags"`
		Labels  map[string]string `koanf:"labels"`
		Servers []server          `koanf:"servers"`
	}
	type cfg struct {
		App appCfg `koanf:"app"`
	}

	c, err := Load[cfg](t.Context(), WithFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.App.Name != "myapp" {
		t.Errorf("App.Name = %q, want myapp", c.App.Name)
	}
	if !c.App.Debug {
		t.Error("App.Debug = false, want true")
	}
	if len(c.App.Tags) != 2 {
		t.Fatalf("App.Tags length = %d, want 2", len(c.App.Tags))
	}
	if c.App.Tags[0] != "web" || c.App.Tags[1] != "api" {
		t.Errorf("App.Tags = %v, want [web api]", c.App.Tags)
	}
	if c.App.Labels["env"] != "staging" {
		t.Errorf("App.Labels[env] = %q, want staging", c.App.Labels["env"])
	}
	if len(c.App.Servers) != 2 {
		t.Fatalf("App.Servers length = %d, want 2", len(c.App.Servers))
	}
	if c.App.Servers[0].Host != "primary.com" {
		t.Errorf("App.Servers[0].Host = %q, want primary.com", c.App.Servers[0].Host)
	}
}

// --- Nested Struct Env Var Tests ---

func TestGetConfig_NestedEnvWithUnderscoreInTag(t *testing.T) {
	t.Setenv("APP_DB_HOST", "localhost")
	t.Setenv("APP_DB_MAX_CONNS", "50")
	t.Setenv("APP_DB_IDLE_TIMEOUT", "30s")

	type dbCfg struct {
		Host        string        `koanf:"host"`
		MaxConns    int           `koanf:"max_conns"`
		IdleTimeout time.Duration `koanf:"idle_timeout"`
	}
	type cfg struct {
		DB dbCfg `koanf:"db"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DB.Host != "localhost" {
		t.Errorf("DB.Host = %q, want localhost", c.DB.Host)
	}
	if c.DB.MaxConns != 50 {
		t.Errorf("DB.MaxConns = %d, want 50", c.DB.MaxConns)
	}
	if c.DB.IdleTimeout != 30*time.Second {
		t.Errorf("DB.IdleTimeout = %v, want 30s", c.DB.IdleTimeout)
	}
}

func TestGetConfig_DeepNestedEnvWithUnderscores(t *testing.T) {
	t.Setenv("APP_SERVER_TLS_CERT_FILE", "/path/cert.pem")
	t.Setenv("APP_SERVER_TLS_KEY_FILE", "/path/key.pem")
	t.Setenv("APP_SERVER_TLS_MIN_VERSION", "1.3")

	type tlsCfg struct {
		CertFile   string `koanf:"cert_file"`
		KeyFile    string `koanf:"key_file"`
		MinVersion string `koanf:"min_version"`
	}
	type serverCfg struct {
		TLS tlsCfg `koanf:"tls"`
	}
	type cfg struct {
		Server serverCfg `koanf:"server"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Server.TLS.CertFile != "/path/cert.pem" {
		t.Errorf("CertFile = %q, want /path/cert.pem", c.Server.TLS.CertFile)
	}
	if c.Server.TLS.KeyFile != "/path/key.pem" {
		t.Errorf("KeyFile = %q, want /path/key.pem", c.Server.TLS.KeyFile)
	}
	if c.Server.TLS.MinVersion != "1.3" {
		t.Errorf("MinVersion = %q, want 1.3", c.Server.TLS.MinVersion)
	}
}

func TestGetConfig_MapFieldsStillWorkFromEnv(t *testing.T) {
	// Map fields use the fallback transform since their keys are dynamic.
	t.Setenv("APP_LABELS_ENV", "prod")
	t.Setenv("APP_LABELS_REGION", "us")
	t.Setenv("APP_NAME", "myapp")

	type cfg struct {
		Name   string            `koanf:"name"`
		Labels map[string]string `koanf:"labels"`
	}

	c, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name != "myapp" {
		t.Errorf("Name = %q, want myapp", c.Name)
	}
	if c.Labels["env"] != "prod" {
		t.Errorf("Labels[env] = %q, want prod", c.Labels["env"])
	}
	if c.Labels["region"] != "us" {
		t.Errorf("Labels[region] = %q, want us", c.Labels["region"])
	}
}

// --- PrintRedacted/RedactedMap with Complex Types ---

func TestRedactedMap_Slice(t *testing.T) {
	type cfg struct {
		Tags []string `koanf:"tags"`
		Name string   `koanf:"name"`
	}
	c := &cfg{Tags: []string{"a", "b"}, Name: "test"}
	m := RedactedMap(c)

	if m["name"] != "test" {
		t.Errorf("name = %v, want test", m["name"])
	}
	// Slice should be preserved as a value in the map.
	tags, ok := m["tags"]
	if !ok {
		t.Fatal("tags key missing from map")
	}
	tagsStr := fmt.Sprintf("%v", tags)
	if !strings.Contains(tagsStr, "a") || !strings.Contains(tagsStr, "b") {
		t.Errorf("tags = %v, want to contain a and b", tags)
	}
}

func TestRedactedMap_Map(t *testing.T) {
	type cfg struct {
		Labels map[string]string `koanf:"labels"`
	}
	c := &cfg{Labels: map[string]string{"env": "prod"}}
	m := RedactedMap(c)

	// Maps expand one entry per key, so a key can be judged sensitive.
	if m["labels[env]"] != "prod" {
		t.Errorf("labels[env] = %v, want prod (map = %v)", m["labels[env]"], m)
	}
}

func TestPrintRedacted_SliceAndMap(t *testing.T) {
	type cfg struct {
		Tags   []string          `koanf:"tags"`
		Labels map[string]string `koanf:"labels"`
		Secret []string          `koanf:"secret_tokens" sensitive:"true"`
	}
	c := &cfg{
		Tags:   []string{"a", "b"},
		Labels: map[string]string{"k": "v"},
		Secret: []string{"tok1", "tok2"},
	}
	var buf bytes.Buffer
	if err := PrintRedacted(c, WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "a") {
		t.Error("table should contain slice value 'a'")
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Error("sensitive slice should be redacted")
	}
	if strings.Contains(output, "tok1") {
		t.Error("sensitive slice values should be redacted")
	}
}

// --- ConfigError / i18n Integration Tests ---

type DescConfig struct {
	Host   string `koanf:"host" default:"localhost" desc:"Server hostname"`
	Port   int    `koanf:"port" default:"8080" desc:"Server port" validate:"min=1,max=65535"`
	Debug  bool   `koanf:"debug" default:"false" desc:"Enable debug mode"`
	Secret string `koanf:"secret" sensitive:"true" desc:"API secret key"`
	NoDesc string `koanf:"nodesc" default:"val"`
}

func TestWithIncludeDescriptions_Table(t *testing.T) {
	cfg := &DescConfig{
		Host:  "localhost",
		Port:  8080,
		Debug: false,
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf), WithIncludeDescriptions()); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "DESCRIPTION") {
		t.Error("table with descriptions should contain DESCRIPTION header")
	}
	if !strings.Contains(output, "Server hostname") {
		t.Errorf("table should contain desc value 'Server hostname', got:\n%s", output)
	}
	if !strings.Contains(output, "Server port") {
		t.Errorf("table should contain desc value 'Server port', got:\n%s", output)
	}
	if !strings.Contains(output, "Enable debug mode") {
		t.Errorf("table should contain desc value 'Enable debug mode', got:\n%s", output)
	}
}

func TestWithIncludeDescriptions_JSON(t *testing.T) {
	cfg := &DescConfig{
		Host: "localhost",
		Port: 8080,
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithFormat("json"), WithWriter(&buf), WithIncludeDescriptions()); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "Server hostname") {
		t.Errorf("JSON with descriptions should contain desc value, got:\n%s", output)
	}
	if !strings.Contains(output, "description") {
		t.Errorf("JSON with descriptions should contain 'description' key, got:\n%s", output)
	}
}

func TestWithIncludeDescriptions_Env(t *testing.T) {
	cfg := &DescConfig{
		Host: "localhost",
		Port: 8080,
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithFormat("env"), WithWriter(&buf), WithIncludeDescriptions()); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "# Server hostname") {
		t.Errorf("env with descriptions should contain desc comment, got:\n%s", output)
	}
	if !strings.Contains(output, "# Server port") {
		t.Errorf("env with descriptions should contain desc comment, got:\n%s", output)
	}
}

func TestWithIncludeDescriptions_NoDescFields(t *testing.T) {
	// Fields without desc:"" tag should show empty description.
	type NoDCfg struct {
		Name string `koanf:"name" default:"test"`
	}
	cfg := &NoDCfg{Name: "test"}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf), WithIncludeDescriptions()); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "DESCRIPTION") {
		t.Error("table with descriptions should still contain DESCRIPTION header")
	}
}

func TestWithoutIncludeDescriptions_Table(t *testing.T) {
	cfg := &DescConfig{
		Host: "localhost",
		Port: 8080,
	}
	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	output := buf.String()

	if strings.Contains(output, "DESCRIPTION") {
		t.Error("table without WithIncludeDescriptions should not contain DESCRIPTION header")
	}
}

// --- Helper ---

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("config-%d.yaml", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing yaml: %v", err)
	}
	return path
}
