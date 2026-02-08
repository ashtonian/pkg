package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Test config structs ---

type BasicConfig struct {
	Host    string        `config:"HOST,localhost" desc:"Server hostname"`
	Port    int           `config:"PORT,8080" validate:"min=1,max=65535"`
	Debug   bool          `config:"DEBUG,false"`
	Timeout time.Duration `config:"TIMEOUT,30s"`
}

type RequiredConfig struct {
	Name string `config:"NAME" required:"true"`
	Port int    `config:"PORT,8080"`
}

type SensitiveConfig struct {
	Host     string `config:"HOST,localhost"`
	Password string `config:"PASSWORD" sensitive:"true"`
	APIKey   string `config:"API_KEY"` // auto-detected sensitive
	Token    string `config:"TOKEN"`   // auto-detected sensitive
	Normal   string `config:"NORMAL,hello"`
}

type NestedConfig struct {
	Server   ServerSubConfig
	Database DatabaseSubConfig
}

type ServerSubConfig struct {
	Host string `config:"HOST,0.0.0.0"`
	Port int    `config:"PORT,8080"`
}

type DatabaseSubConfig struct {
	DSN      string `config:"DSN" required:"true"`
	MaxConns int    `config:"MAX_CONNS,25"`
}

type SecretConfig struct {
	DBPassword string `config:"DB_PASSWORD" secret:"db/password" sensitive:"true"`
	APIKey     string `config:"API_KEY" secret:"api/key"`
}

type ValidationConfig struct {
	Port    int    `config:"PORT,8080" validate:"min=1,max=65535"`
	Workers int    `config:"WORKERS,4" validate:"min=1,max=100"`
	Email   string `config:"EMAIL" validate:"omitempty,email"`
}

type AllTypesConfig struct {
	Str     string        `config:"STR,hello"`
	Bool    bool          `config:"BOOL,true"`
	Int     int           `config:"INT,42"`
	Int8    int8          `config:"INT8,8"`
	Int16   int16         `config:"INT16,16"`
	Int32   int32         `config:"INT32,32"`
	Int64   int64         `config:"INT64,64"`
	Uint    uint          `config:"UINT,10"`
	Uint8   uint8         `config:"UINT8,8"`
	Uint16  uint16        `config:"UINT16,16"`
	Uint32  uint32        `config:"UINT32,32"`
	Uint64  uint64        `config:"UINT64,64"`
	Float32 float32       `config:"FLOAT32,1.5"`
	Float64 float64       `config:"FLOAT64,2.5"`
	Dur     time.Duration `config:"DUR,5s"`
}

type IgnoredFieldConfig struct {
	Host    string `config:"HOST,localhost"`
	Ignored string `config:"-"`
}

type DefaultTagConfig struct {
	Host string `config:"HOST" default:"fallback-host"`
	Port int    `config:"PORT" default:"9090"`
}

type ExplicitNameConfig struct {
	Connection string `config:"DATABASE_URL" required:"true"`
	MaxConns   int    `config:"DB_MAX_CONNS,25"`
}

// --- GetConfig Tests ---

func TestGetConfig_Defaults(t *testing.T) {
	cfg, err := GetConfig[BasicConfig](WithSkipValidation())
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

func TestGetConfig_EnvVars(t *testing.T) {
	t.Setenv("APP_HOST", "api.example.com")
	t.Setenv("APP_PORT", "9000")
	t.Setenv("APP_DEBUG", "true")
	t.Setenv("APP_TIMEOUT", "10s")

	cfg, err := GetConfig[BasicConfig](
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
	yamlContent := `host: from-file
port: 3000
`
	path := writeYAML(t, yamlContent)

	t.Setenv("HOST", "from-env")

	cfg, err := GetConfig[BasicConfig](
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Env should override file.
	if cfg.Host != "from-env" {
		t.Errorf("Host = %q, want %q (env should override file)", cfg.Host, "from-env")
	}
	// File value should be used when no env var.
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want %d (from file)", cfg.Port, 3000)
	}
}

func TestGetConfig_FileOverridesDefault(t *testing.T) {
	yamlContent := `host: from-file
port: 3000
`
	path := writeYAML(t, yamlContent)

	cfg, err := GetConfig[BasicConfig](
		WithConfigFile(path),
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

func TestGetConfig_AllTypes(t *testing.T) {
	cfg, err := GetConfig[AllTypesConfig](WithSkipValidation())
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
	if cfg.Int8 != 8 {
		t.Errorf("Int8 = %d, want 8", cfg.Int8)
	}
	if cfg.Int16 != 16 {
		t.Errorf("Int16 = %d, want 16", cfg.Int16)
	}
	if cfg.Int32 != 32 {
		t.Errorf("Int32 = %d, want 32", cfg.Int32)
	}
	if cfg.Int64 != 64 {
		t.Errorf("Int64 = %d, want 64", cfg.Int64)
	}
	if cfg.Uint != 10 {
		t.Errorf("Uint = %d, want 10", cfg.Uint)
	}
	if cfg.Uint8 != 8 {
		t.Errorf("Uint8 = %d, want 8", cfg.Uint8)
	}
	if cfg.Uint16 != 16 {
		t.Errorf("Uint16 = %d, want 16", cfg.Uint16)
	}
	if cfg.Uint32 != 32 {
		t.Errorf("Uint32 = %d, want 32", cfg.Uint32)
	}
	if cfg.Uint64 != 64 {
		t.Errorf("Uint64 = %d, want 64", cfg.Uint64)
	}
	if cfg.Float32 != 1.5 {
		t.Errorf("Float32 = %f, want 1.5", cfg.Float32)
	}
	if cfg.Float64 != 2.5 {
		t.Errorf("Float64 = %f, want 2.5", cfg.Float64)
	}
	if cfg.Dur != 5*time.Second {
		t.Errorf("Dur = %v, want 5s", cfg.Dur)
	}
}

func TestGetConfig_EnvAllTypes(t *testing.T) {
	t.Setenv("STR", "world")
	t.Setenv("BOOL", "false")
	t.Setenv("INT", "99")
	t.Setenv("UINT", "200")
	t.Setenv("FLOAT64", "3.14")
	t.Setenv("DUR", "1m")

	cfg, err := GetConfig[AllTypesConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Str != "world" {
		t.Errorf("Str = %q, want %q", cfg.Str, "world")
	}
	if cfg.Bool != false {
		t.Errorf("Bool = %v, want false", cfg.Bool)
	}
	if cfg.Int != 99 {
		t.Errorf("Int = %d, want 99", cfg.Int)
	}
	if cfg.Uint != 200 {
		t.Errorf("Uint = %d, want 200", cfg.Uint)
	}
	if cfg.Float64 != 3.14 {
		t.Errorf("Float64 = %f, want 3.14", cfg.Float64)
	}
	if cfg.Dur != time.Minute {
		t.Errorf("Dur = %v, want 1m", cfg.Dur)
	}
}

// --- Required Field Tests ---

func TestGetConfig_RequiredField_Missing(t *testing.T) {
	_, err := GetConfig[RequiredConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !errors.Is(err, ErrRequired) {
		t.Errorf("expected ErrRequired, got: %v", err)
	}
	if !strings.Contains(err.Error(), "NAME") {
		t.Errorf("error should mention field NAME, got: %v", err)
	}
}

func TestGetConfig_RequiredField_Set(t *testing.T) {
	t.Setenv("NAME", "test-app")

	cfg, err := GetConfig[RequiredConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "test-app" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-app")
	}
}

// --- Nested Config Tests ---

func TestGetConfig_NestedStruct(t *testing.T) {
	t.Setenv("SERVER_HOST", "10.0.0.1")
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("DATABASE_DSN", "postgres://localhost/test")
	t.Setenv("DATABASE_MAX_CONNS", "50")

	cfg, err := GetConfig[NestedConfig](WithSkipValidation())
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

func TestGetConfig_NestedWithPrefix(t *testing.T) {
	t.Setenv("APP_SERVER_HOST", "10.0.0.1")
	t.Setenv("APP_SERVER_PORT", "9090")
	t.Setenv("APP_DATABASE_DSN", "postgres://localhost/test")

	cfg, err := GetConfig[NestedConfig](
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

func TestGetConfig_SecretProvider(t *testing.T) {
	provider := NewMapSecretProvider(map[string]string{
		"db/password": "supersecret",
		"api/key":     "key-123",
	})

	cfg, err := GetConfig[SecretConfig](
		WithSecretProvider(provider, context.Background()),
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

func TestGetConfig_SecretProvider_Error(t *testing.T) {
	provider := NewMapSecretProvider(map[string]string{})

	_, err := GetConfig[SecretConfig](
		WithSecretProvider(provider, context.Background()),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
	if !errors.Is(err, ErrSecretProvider) {
		t.Errorf("expected ErrSecretProvider, got: %v", err)
	}
}

func TestEnvSecretProvider(t *testing.T) {
	t.Setenv("SECRET_DB_PASSWORD", "from-env-secret")

	provider := NewEnvSecretProvider("SECRET_")
	val, err := provider.GetSecret(context.Background(), "db/password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "from-env-secret" {
		t.Errorf("val = %q, want %q", val, "from-env-secret")
	}
}

func TestEnvSecretProvider_Missing(t *testing.T) {
	provider := NewEnvSecretProvider("SECRET_")
	_, err := provider.GetSecret(context.Background(), "nonexistent/key")
	if err == nil {
		t.Fatal("expected error for missing env secret")
	}
}

// --- Validation Tests ---

func TestGetConfig_Validation_Pass(t *testing.T) {
	cfg, err := GetConfig[ValidationConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestGetConfig_Validation_Fail(t *testing.T) {
	t.Setenv("PORT", "0")

	_, err := GetConfig[ValidationConfig]()
	if err == nil {
		t.Fatal("expected validation error for Port=0")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("expected ErrValidation, got: %v", err)
	}
}

func TestGetConfig_Validation_Skip(t *testing.T) {
	t.Setenv("PORT", "0")

	cfg, err := GetConfig[ValidationConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0", cfg.Port)
	}
}

// --- File Source Tests ---

func TestGetConfig_YAMLFile(t *testing.T) {
	yamlContent := `host: yaml-host
port: 7777
debug: true
timeout: 15s
`
	path := writeYAML(t, yamlContent)

	cfg, err := GetConfig[BasicConfig](
		WithConfigFile(path),
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
  max_conns: 10
`
	path := writeYAML(t, yamlContent)

	cfg, err := GetConfig[NestedConfig](
		WithConfigFile(path),
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
	cfg, err := GetConfig[BasicConfig](
		WithConfigFile("/nonexistent/config.yaml"),
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
	_, err := GetConfig[BasicConfig](
		WithMustConfigFile("/nonexistent/config.yaml"),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for missing required file")
	}
	if !errors.Is(err, ErrFileRead) {
		t.Errorf("expected ErrFileRead, got: %v", err)
	}
}

func TestGetConfig_InvalidYAML(t *testing.T) {
	path := writeYAML(t, "{{invalid yaml content")

	_, err := GetConfig[BasicConfig](
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !errors.Is(err, ErrFileParse) {
		t.Errorf("expected ErrFileParse, got: %v", err)
	}
}

func TestGetConfig_MultipleFiles_MergedLeftToRight(t *testing.T) {
	base := writeYAML(t, `host: base-host
port: 1111
`)
	override := writeYAML(t, `port: 2222
`)
	cfg, err := GetConfig[BasicConfig](
		WithConfigFiles(base, override),
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

// --- Ignored Field Tests ---

func TestGetConfig_IgnoredField(t *testing.T) {
	t.Setenv("HOST", "test-host")
	t.Setenv("IGNORED", "should-not-set")

	cfg, err := GetConfig[IgnoredFieldConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "test-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "test-host")
	}
	if cfg.Ignored != "" {
		t.Errorf("Ignored = %q, want empty (should be ignored)", cfg.Ignored)
	}
}

// --- Default Tag Tests ---

func TestGetConfig_DefaultTag(t *testing.T) {
	cfg, err := GetConfig[DefaultTagConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "fallback-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "fallback-host")
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want %d", cfg.Port, 9090)
	}
}

// --- Explicit Name Tests ---

func TestGetConfig_ExplicitName(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://explicit/db")

	cfg, err := GetConfig[ExplicitNameConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Connection != "postgres://explicit/db" {
		t.Errorf("Connection = %q, want %q", cfg.Connection, "postgres://explicit/db")
	}
	if cfg.MaxConns != 25 {
		t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, 25)
	}
}

// --- Programmatic Defaults Tests ---

func TestGetConfig_ProgrammaticDefaults(t *testing.T) {
	cfg, err := GetConfig[BasicConfig](
		WithDefaults(map[string]any{
			"HOST": "prog-host",
			"PORT": 5555,
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
	yamlContent := `host: from-file
`
	path := writeYAML(t, yamlContent)

	cfg, err := GetConfig[BasicConfig](
		WithDefaults(map[string]any{
			"HOST": "prog-host",
			"PORT": 5555,
		}),
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// File overrides programmatic default.
	if cfg.Host != "from-file" {
		t.Errorf("Host = %q, want %q (file should override programmatic default)", cfg.Host, "from-file")
	}
	// Programmatic default used when file has no value.
	if cfg.Port != 5555 {
		t.Errorf("Port = %d, want %d (from programmatic default)", cfg.Port, 5555)
	}
}

// --- Print Tests ---

func TestLoader_Print_Table(t *testing.T) {
	var buf bytes.Buffer

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loader.Print(WithWriter(&buf))
	output := buf.String()

	if !strings.Contains(output, "ENV VARIABLE") {
		t.Error("table should contain header")
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Error("sensitive fields should be redacted")
	}
	if !strings.Contains(output, "localhost") {
		t.Error("normal values should appear")
	}
}

func TestLoader_Print_JSON(t *testing.T) {
	var buf bytes.Buffer

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loader.Print(WithFormat("json"), WithWriter(&buf))
	output := buf.String()

	if !strings.Contains(output, "[REDACTED]") {
		t.Error("JSON should redact sensitive fields")
	}
}

func TestLoader_Print_Env(t *testing.T) {
	var buf bytes.Buffer

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loader.Print(WithFormat("env"), WithWriter(&buf))
	output := buf.String()

	if !strings.Contains(output, "export HOST=localhost") {
		t.Errorf("env format should contain export, got:\n%s", output)
	}
}

func TestLoader_Print_ShowSensitive(t *testing.T) {
	t.Setenv("PASSWORD", "mypass")

	var buf bytes.Buffer

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loader.Print(WithWriter(&buf), WithShowSensitive(true))
	output := buf.String()

	if !strings.Contains(output, "mypass") {
		t.Error("ShowSensitive should reveal password")
	}
}

func TestLoader_Print_SortByName(t *testing.T) {
	var buf bytes.Buffer

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loader.Print(WithWriter(&buf), WithSortByName())
	output := buf.String()

	// API_KEY should appear before HOST when sorted.
	apiIdx := strings.Index(output, "API_KEY")
	hostIdx := strings.Index(output, "HOST")
	if apiIdx < 0 || hostIdx < 0 {
		t.Fatalf("expected both API_KEY and HOST in output:\n%s", output)
	}
	if apiIdx > hostIdx {
		t.Error("API_KEY should appear before HOST when sorted")
	}
}

// --- ToMap Tests ---

func TestLoader_ToMap_Redacted(t *testing.T) {
	t.Setenv("PASSWORD", "secret123")

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	m := loader.ToMap(true)
	if m["PASSWORD"] != "[REDACTED]" {
		t.Errorf("PASSWORD = %v, want [REDACTED]", m["PASSWORD"])
	}
	if m["HOST"] != "localhost" {
		t.Errorf("HOST = %v, want localhost", m["HOST"])
	}
}

func TestLoader_ToMap_Unredacted(t *testing.T) {
	t.Setenv("PASSWORD", "secret123")

	loader, err := NewLoader[SensitiveConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	_, err = loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	m := loader.ToMap(false)
	if m["PASSWORD"] != "secret123" {
		t.Errorf("PASSWORD = %v, want secret123", m["PASSWORD"])
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

// --- Loader Fields Tests ---

func TestLoader_Fields(t *testing.T) {
	loader, err := NewLoader[BasicConfig]()
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	fields := loader.Fields()
	if len(fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(fields))
	}

	names := make(map[string]bool)
	for _, f := range fields {
		names[f.FieldName] = true
	}
	for _, expected := range []string{"Host", "Port", "Debug", "Timeout"} {
		if !names[expected] {
			t.Errorf("expected field %q in Fields()", expected)
		}
	}
}

// --- Non-Struct Type Tests ---

func TestNewLoader_NonStruct(t *testing.T) {
	_, err := NewLoader[string]()
	if err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

// --- Error Type Tests ---

func TestErrUnsupportedType_BadInt(t *testing.T) {
	t.Setenv("PORT", "not-a-number")

	_, err := GetConfig[BasicConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for invalid int value")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got: %v", err)
	}
}

func TestErrUnsupportedType_BadBool(t *testing.T) {
	t.Setenv("DEBUG", "not-a-bool")

	_, err := GetConfig[BasicConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for invalid bool value")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got: %v", err)
	}
}

func TestErrUnsupportedType_BadDuration(t *testing.T) {
	t.Setenv("TIMEOUT", "not-a-duration")

	_, err := GetConfig[BasicConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for invalid duration value")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got: %v", err)
	}
}

// --- toUpperSnake Tests ---

func TestToUpperSnake(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Host", "HOST"},
		{"ServerHost", "SERVER_HOST"},
		{"DBMaxConns", "DB_MAX_CONNS"},
		{"MaxConns", "MAX_CONNS"},
		{"DSN", "DSN"},
		{"APIKey", "API_KEY"},
		{"HTTPServer", "HTTP_SERVER"},
		{"ID", "ID"},
		{"UserID", "USER_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toUpperSnake(tt.input)
			if got != tt.expected {
				t.Errorf("toUpperSnake(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// --- envKeyFromSecret Tests ---

func TestEnvKeyFromSecret(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"db/password", "DB_PASSWORD"},
		{"api/key", "API_KEY"},
		{"app.secret.key", "APP_SECRET_KEY"},
		{"my-service/token", "MY_SERVICE_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := envKeyFromSecret(tt.input)
			if got != tt.expected {
				t.Errorf("envKeyFromSecret(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// --- Custom Validator Tests ---

func TestGetConfig_CustomValidator(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("WORKERS", "4")

	// Providing a custom validator should still work.
	cfg, err := GetConfig[ValidationConfig](
		WithValidator(nil), // nil falls back to default
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

// --- MapSecretProvider Tests ---

func TestMapSecretProvider_NotFound(t *testing.T) {
	provider := NewMapSecretProvider(map[string]string{
		"existing": "value",
	})
	_, err := provider.GetSecret(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// --- Precedence Integration Test ---

func TestGetConfig_FullPrecedence(t *testing.T) {
	// Setup: struct default "localhost", file "file-host", env "env-host".
	yamlContent := `host: file-host
`
	path := writeYAML(t, yamlContent)
	t.Setenv("HOST", "env-host")

	cfg, err := GetConfig[BasicConfig](
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Env should win over file and default.
	if cfg.Host != "env-host" {
		t.Errorf("Host = %q, want %q (env should have highest precedence)", cfg.Host, "env-host")
	}
}

func TestGetConfig_Precedence_ProgrammaticOverStructDefault(t *testing.T) {
	cfg, err := GetConfig[BasicConfig](
		WithDefaults(map[string]any{
			"HOST": "prog-host",
		}),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Programmatic default should override struct tag default.
	if cfg.Host != "prog-host" {
		t.Errorf("Host = %q, want %q", cfg.Host, "prog-host")
	}
}

// --- Pointer Type Tests ---

type PointerConfig struct {
	Name    *string `config:"NAME"`
	Port    *int    `config:"PORT"`
	Verbose *bool   `config:"VERBOSE"`
}

func TestGetConfig_PointerString_Set(t *testing.T) {
	t.Setenv("NAME", "hello")

	cfg, err := GetConfig[PointerConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name == nil {
		t.Fatal("Name should not be nil")
	}
	if *cfg.Name != "hello" {
		t.Errorf("*Name = %q, want %q", *cfg.Name, "hello")
	}
}

func TestGetConfig_PointerString_Unset(t *testing.T) {
	cfg, err := GetConfig[PointerConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != nil {
		t.Errorf("Name should be nil when unset, got %q", *cfg.Name)
	}
}

func TestGetConfig_PointerInt_Set(t *testing.T) {
	t.Setenv("PORT", "9090")

	cfg, err := GetConfig[PointerConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port == nil {
		t.Fatal("Port should not be nil")
	}
	if *cfg.Port != 9090 {
		t.Errorf("*Port = %d, want %d", *cfg.Port, 9090)
	}
}

func TestGetConfig_PointerBool_Set(t *testing.T) {
	t.Setenv("VERBOSE", "true")

	cfg, err := GetConfig[PointerConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Verbose == nil {
		t.Fatal("Verbose should not be nil")
	}
	if *cfg.Verbose != true {
		t.Errorf("*Verbose = %v, want true", *cfg.Verbose)
	}
}

// --- Slice Type Tests ---

type SliceConfig struct {
	Tags  []string `config:"TAGS"`
	Ports []int    `config:"PORTS"`
}

func TestGetConfig_SliceString(t *testing.T) {
	t.Setenv("TAGS", "a,b,c")

	cfg, err := GetConfig[SliceConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tags) != 3 {
		t.Fatalf("Tags length = %d, want 3", len(cfg.Tags))
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if cfg.Tags[i] != w {
			t.Errorf("Tags[%d] = %q, want %q", i, cfg.Tags[i], w)
		}
	}
}

func TestGetConfig_SliceInt(t *testing.T) {
	t.Setenv("PORTS", "80,443,8080")

	cfg, err := GetConfig[SliceConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Ports) != 3 {
		t.Fatalf("Ports length = %d, want 3", len(cfg.Ports))
	}
	wantPorts := []int{80, 443, 8080}
	for i, w := range wantPorts {
		if cfg.Ports[i] != w {
			t.Errorf("Ports[%d] = %d, want %d", i, cfg.Ports[i], w)
		}
	}
}

func TestGetConfig_SliceInt_BadElement(t *testing.T) {
	t.Setenv("PORTS", "80,abc")

	_, err := GetConfig[SliceConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for invalid slice element")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got: %v", err)
	}
}

func TestGetConfig_SliceUnset(t *testing.T) {
	cfg, err := GetConfig[SliceConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tags != nil {
		t.Errorf("Tags should be nil when unset, got %v", cfg.Tags)
	}
}

// --- Map Type Tests ---

type MapConfig struct {
	Labels  map[string]string `config:"LABELS"`
	Limits  map[string]int    `config:"LIMITS"`
}

func TestGetConfig_MapStringString(t *testing.T) {
	t.Setenv("LABELS", "env=prod,region=us")

	cfg, err := GetConfig[MapConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Labels) != 2 {
		t.Fatalf("Labels length = %d, want 2", len(cfg.Labels))
	}
	if cfg.Labels["env"] != "prod" {
		t.Errorf("Labels[env] = %q, want %q", cfg.Labels["env"], "prod")
	}
	if cfg.Labels["region"] != "us" {
		t.Errorf("Labels[region] = %q, want %q", cfg.Labels["region"], "us")
	}
}

func TestGetConfig_MapStringInt(t *testing.T) {
	t.Setenv("LIMITS", "cpu=4,mem=8")

	cfg, err := GetConfig[MapConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Limits) != 2 {
		t.Fatalf("Limits length = %d, want 2", len(cfg.Limits))
	}
	if cfg.Limits["cpu"] != 4 {
		t.Errorf("Limits[cpu] = %d, want %d", cfg.Limits["cpu"], 4)
	}
	if cfg.Limits["mem"] != 8 {
		t.Errorf("Limits[mem] = %d, want %d", cfg.Limits["mem"], 8)
	}
}

func TestGetConfig_Map_BadFormat(t *testing.T) {
	t.Setenv("LABELS", "noequals")

	_, err := GetConfig[MapConfig](WithSkipValidation())
	if err == nil {
		t.Fatal("expected error for bad map format")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got: %v", err)
	}
}

func TestGetConfig_MapUnset(t *testing.T) {
	cfg, err := GetConfig[MapConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Labels != nil {
		t.Errorf("Labels should be nil when unset, got %v", cfg.Labels)
	}
}

// --- TextUnmarshaler Test ---

type netAddr struct {
	Host string
	Port int
}

func (a *netAddr) UnmarshalText(text []byte) error {
	s := string(text)
	parts := strings.SplitN(s, ":", 2)
	a.Host = parts[0]
	if len(parts) == 2 {
		p, err := strconv.Atoi(parts[1])
		if err != nil {
			return err
		}
		a.Port = p
	}
	return nil
}

type TextUnmarshalerConfig struct {
	Addr netAddr `config:"ADDR"`
}

func TestGetConfig_TextUnmarshaler(t *testing.T) {
	t.Setenv("ADDR", "127.0.0.1:8080")

	cfg, err := GetConfig[TextUnmarshalerConfig](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr.Host != "127.0.0.1" {
		t.Errorf("Addr.Host = %q, want %q", cfg.Addr.Host, "127.0.0.1")
	}
	if cfg.Addr.Port != 8080 {
		t.Errorf("Addr.Port = %d, want %d", cfg.Addr.Port, 8080)
	}
}

// --- YAML File with Slice/Map Tests ---

func TestGetConfig_YAMLArray(t *testing.T) {
	yamlContent := `tags: [a, b, c]
`
	path := writeYAML(t, yamlContent)

	type cfg struct {
		Tags []string `config:"TAGS"`
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
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
