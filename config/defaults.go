package config

import (
	"reflect"
	"strings"
)

// DefaultsFromTags walks the struct type T for `default:"value"` tags
// recursively, building a flat map[string]any keyed by koanf
// dot-paths (using `koanf:"name"` tags for path segments). The returned
// map is suitable for use with confmap.Provider or WithDefaults.
//
// This function exists because koanf/mapstructure do not natively read
// `default:"value"` struct tags. DefaultsFromTags bridges this gap by
// extracting defaults into a map that koanf loads as its lowest-precedence
// source.
//
// Example:
//
//	type ServerConfig struct {
//	    Host string `koanf:"host" default:"localhost"`
//	    Port int    `koanf:"port" default:"8080"`
//	}
//	type AppConfig struct {
//	    Server ServerConfig `koanf:"server"`
//	    Debug  bool         `koanf:"debug" default:"false"`
//	}
//	defaults := config.DefaultsFromTags[AppConfig]()
//	// Returns: {"server.host": "localhost", "server.port": "8080", "debug": "false"}
func DefaultsFromTags[T any]() map[string]any {
	result := make(map[string]any)
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return result
	}
	walkDefaults(t, "", result)
	return result
}

// envMappingFromType walks the struct type to build a map from env var names
// to koanf dot-paths. This resolves the ambiguity when koanf tags contain
// underscores (e.g., koanf:"max_conns" nested under koanf:"db").
//
// Without this, the naive "_" → "." transform turns APP_DB_MAX_CONNS into
// "db.max.conns" (wrong) instead of "db.max_conns" (correct).
func envMappingFromType(t reflect.Type, envPrefix string) map[string]string {
	result := make(map[string]string)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return result
	}
	walkEnvMapping(t, envPrefix, "", result)
	return result
}

// walkEnvMapping recursively walks struct fields, building env-var → koanf-path entries.
func walkEnvMapping(t reflect.Type, envPrefix, koanfPrefix string, out map[string]string) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		koanfTag := field.Tag.Get("koanf")
		if koanfTag == "-" {
			continue
		}
		if koanfTag == "" {
			koanfTag = strings.ToLower(field.Name)
		}

		koanfPath := koanfTag
		if koanfPrefix != "" {
			koanfPath = koanfPrefix + "." + koanfTag
		}

		// Env var name: prefix + "_" + uppercase(koanf tag preserving underscores).
		envName := envPrefix + "_" + strings.ToUpper(koanfTag)

		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		// Recurse into nested structs (not time, not maps, not slices).
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			walkEnvMapping(ft, envName, koanfPath, out)
			continue
		}

		// Leaf field: register the mapping.
		out[envName] = koanfPath
	}
}

// walkDefaults recursively walks struct fields, extracting default tags.
func walkDefaults(t reflect.Type, prefix string, out map[string]any) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		koanfTag := field.Tag.Get("koanf")
		if koanfTag == "-" || koanfTag == "" {
			// If no koanf tag, use the field name in lowercase as a fallback.
			koanfTag = strings.ToLower(field.Name)
		}

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		// Recurse into nested structs (but not special types like time.Duration).
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			walkDefaults(ft, key, out)
			continue
		}

		// Extract the default tag value.
		if defVal, ok := field.Tag.Lookup("default"); ok {
			out[key] = defVal
		}
	}
}
