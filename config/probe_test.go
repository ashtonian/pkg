//go:build unit

package config

import (
	"os"
	"testing"
)

func TestProbe_SliceFromEnv(t *testing.T) {
	t.Setenv("APP_TAGS", "a,b,c")

	type cfg struct {
		Tags []string `koanf:"tags"`
	}

	c, err := GetConfig[cfg](WithEnvPrefix("APP"), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Tags type=%T value=%v len=%d", c.Tags, c.Tags, len(c.Tags))
	if len(c.Tags) != 3 {
		t.Errorf("Tags length = %d, want 3", len(c.Tags))
	}
}

func TestProbe_MapFromYAML(t *testing.T) {
	yamlContent := "labels:\n  env: prod\n  region: us\n"
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	os.WriteFile(path, []byte(yamlContent), 0o644)

	type cfg struct {
		Labels map[string]string `koanf:"labels"`
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Labels type=%T value=%v len=%d", c.Labels, c.Labels, len(c.Labels))
	if len(c.Labels) != 2 {
		t.Errorf("Labels length = %d, want 2", len(c.Labels))
	}
}

func TestProbe_PointerFields(t *testing.T) {
	yamlContent := "name: hello\ncount: 42\n"
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	os.WriteFile(path, []byte(yamlContent), 0o644)

	type cfg struct {
		Name  *string `koanf:"name"`
		Count *int    `koanf:"count"`
		Opt   *string `koanf:"opt"`  // not set
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Name=%v Count=%v Opt=%v", c.Name, c.Count, c.Opt)
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

func TestProbe_PointerStruct(t *testing.T) {
	yamlContent := "db:\n  host: dbhost\n  port: 5432\n"
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	os.WriteFile(path, []byte(yamlContent), 0o644)

	type DB struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	type cfg struct {
		DB    *DB     `koanf:"db"`
		Cache *DB     `koanf:"cache"` // not set
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("DB=%+v Cache=%v", c.DB, c.Cache)
	if c.DB == nil || c.DB.Host != "dbhost" {
		t.Errorf("DB = %v, want {Host:dbhost Port:5432}", c.DB)
	}
}

func TestProbe_SliceDefault(t *testing.T) {
	type cfg struct {
		Tags []string `koanf:"tags" default:"x,y,z"`
	}

	c, err := GetConfig[cfg](WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Tags type=%T value=%v len=%d", c.Tags, c.Tags, len(c.Tags))
	if len(c.Tags) != 3 {
		t.Errorf("Tags length = %d, want 3", len(c.Tags))
	}
}

func TestProbe_MapOfSlices(t *testing.T) {
	yamlContent := "routes:\n  api:\n    - /v1\n    - /v2\n  admin:\n    - /dashboard\n"
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	os.WriteFile(path, []byte(yamlContent), 0o644)

	type cfg struct {
		Routes map[string][]string `koanf:"routes"`
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Routes type=%T value=%v", c.Routes, c.Routes)
	if len(c.Routes) != 2 {
		t.Errorf("Routes length = %d, want 2", len(c.Routes))
	}
	if len(c.Routes["api"]) != 2 {
		t.Errorf("Routes[api] length = %d, want 2", len(c.Routes["api"]))
	}
}

func TestProbe_SliceOfStructs(t *testing.T) {
	yamlContent := "servers:\n  - host: a\n    port: 1\n  - host: b\n    port: 2\n"
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	os.WriteFile(path, []byte(yamlContent), 0o644)

	type Server struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	type cfg struct {
		Servers []Server `koanf:"servers"`
	}

	c, err := GetConfig[cfg](WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Servers type=%T value=%v len=%d", c.Servers, c.Servers, len(c.Servers))
	if len(c.Servers) != 2 {
		t.Errorf("Servers length = %d, want 2", len(c.Servers))
	}
}

func TestProbe_PrintRedacted_Slice(t *testing.T) {
	type cfg struct {
		Tags []string          `koanf:"tags"`
		Labels map[string]string `koanf:"labels"`
	}
	c := &cfg{
		Tags:   []string{"a", "b"},
		Labels: map[string]string{"k": "v"},
	}
	m := ToMap(c, false)
	t.Logf("ToMap result: %v", m)
}
