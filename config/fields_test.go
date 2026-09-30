//go:build unit

package config

import (
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"
)

type fieldsInner struct {
	Host string `koanf:"host" default:"localhost"`
}

type fieldsEmbedded struct {
	Region string `koanf:"region" default:"us"`
}

type fieldsCfg struct {
	Name       string            `koanf:"name,omitempty" default:"svc"`
	Ignored    string            `koanf:"-" default:"must-not-load"`
	Rest       map[string]any    `koanf:",remain"`
	Untagged   int               `default:"3"`
	Inner      fieldsInner       `koanf:"inner"`
	InnerPtr   *fieldsInner      `koanf:"inner_ptr"`
	Squashed   fieldsEmbedded    `koanf:",squash"`
	Started    time.Time         `koanf:"started"`
	ListenAddr netip.Addr        `koanf:"listen_addr"`
	Timeout    time.Duration     `koanf:"timeout" default:"5s"`
	Tags       []string          `koanf:"tags"`
	Labels     map[string]string `koanf:"labels"`
	Any        any               `koanf:"any"`
}

func fieldPaths(fields []Field) []string {
	paths := make([]string, 0, len(fields))
	for _, f := range fields {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestFields_KeyRules(t *testing.T) {
	got := fieldPaths(Fields[fieldsCfg]())
	want := []string{
		"name",           // tag options are not part of the name
		"untagged",       // lowercased field name
		"inner.host",     // nested struct
		"inner_ptr.host", // pointer to struct
		"region",         // squash merges into the parent
		"started",        // TextUnmarshaler struct is one leaf
		"listen_addr",    // ditto
		"timeout",
		"tags",
		"labels",
		"any",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Fields paths = %v\nwant           %v", got, want)
	}
}

// DefaultsFromTags turned koanf:"-" into
// an "ignored" key and loaded the field's default.
func TestDefaultsFromTags_SkipsIgnoredField(t *testing.T) {
	defaults := DefaultsFromTags[fieldsCfg]()
	want := map[string]any{
		"name":           "svc",
		"untagged":       "3",
		"inner.host":     "localhost",
		"inner_ptr.host": "localhost",
		"region":         "us",
		"timeout":        "5s",
	}
	if !reflect.DeepEqual(defaults, want) {
		t.Errorf("DefaultsFromTags = %v\nwant             %v", defaults, want)
	}

}

// A TextUnmarshaler struct is a leaf for env mapping as well, so a key with
// an underscore resolves exactly (listen_addr, not listen.addr).
func TestGetConfig_TextUnmarshalerLeafFromEnv(t *testing.T) {
	t.Setenv("FLD_LISTEN_ADDR", "10.0.0.7")
	cfg, err := GetConfig[fieldsCfg](t.Context(), WithEnvPrefix("FLD"), WithSkipValidation())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got := cfg.ListenAddr.String(); got != "10.0.0.7" {
		t.Errorf("ListenAddr = %s, want 10.0.0.7", got)
	}
}

type fieldsNode struct {
	Name string      `koanf:"name" default:"root"`
	Next *fieldsNode `koanf:"next"`
}

// A recursive type overflowed the stack.
// The walk now stops at the recursion; the recursive part still decodes
// from a file.
func TestFields_RecursiveTypeTerminates(t *testing.T) {
	if got := fieldPaths(Fields[fieldsNode]()); !slices.Equal(got, []string{"name"}) {
		t.Errorf("Fields = %v, want [name]", got)
	}

	path := writeYAML(t, "name: a\nnext:\n  name: b\n")
	cfg, err := GetConfig[fieldsNode](t.Context(), WithMustConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Next == nil || cfg.Next.Name != "b" {
		t.Errorf("Next = %+v, want the file's nested node", cfg.Next)
	}
}

// An interface T dereferenced a nil
// reflect.Type and panicked.
func TestFields_NonStructHasNoFields(t *testing.T) {
	if got := Fields[any](); len(got) != 0 {
		t.Errorf("Fields[any] = %v, want none", got)
	}
	if got := Fields[int](); len(got) != 0 {
		t.Errorf("Fields[int] = %v, want none", got)
	}
	if got := DefaultsFromTags[any](); len(got) != 0 {
		t.Errorf("DefaultsFromTags[any] = %v, want empty", got)
	}
	if got := fieldPaths(Fields[*fieldsInner]()); !slices.Equal(got, []string{"host"}) {
		t.Errorf("Fields[*fieldsInner] = %v, want [host]", got)
	}
}

type envLimits struct {
	MaxPageSize *int `koanf:"max_page_size"`
	Size        int  `koanf:"size"`
	Enabled     bool `koanf:"enabled"`
}

type envMapCfg struct {
	Features map[string]envLimits `koanf:"features"`
	Labels   map[string]string    `koanf:"labels"`
}

// A map of structs has keys no schema names; its variables resolve by
// matching the element's field names, so a key with underscores (audit_logs)
// and a field with underscores (max_page_size) both survive.
func TestGetConfig_EnvIntoMapOfStructs(t *testing.T) {
	t.Setenv("MAPENV_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE", "500")
	t.Setenv("MAPENV_FEATURES_DEVICES_SIZE", "3")
	t.Setenv("MAPENV_FEATURES_DEVICES_ENABLED", "true")
	t.Setenv("MAPENV_LABELS_ENV", "prod")

	cfg, err := GetConfig[envMapCfg](t.Context(), WithEnvPrefix("MAPENV"), WithSkipValidation())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	audit := cfg.Features["audit_logs"]
	if audit.MaxPageSize == nil || *audit.MaxPageSize != 500 {
		t.Errorf("features[audit_logs].max_page_size = %v, want 500 (features = %+v)", audit.MaxPageSize, cfg.Features)
	}
	if audit.Size != 0 {
		t.Errorf("features[audit_logs].size = %d: the longer suffix max_page_size must win", audit.Size)
	}
	if d := cfg.Features["devices"]; d.Size != 3 || !d.Enabled {
		t.Errorf("features[devices] = %+v, want size 3, enabled", d)
	}
	if cfg.Labels["env"] != "prod" {
		t.Errorf("labels = %v: a map of scalars keeps the plain fallback", cfg.Labels)
	}
}
