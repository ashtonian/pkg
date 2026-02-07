package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source represents a configuration source that can provide key-value pairs.
type Source interface {
	// Name returns the source name for diagnostics.
	Name() string
	// Load returns a flat map of key -> string value.
	Load() (map[string]string, error)
}

// FileSource loads configuration from a YAML file.
type FileSource struct {
	path     string
	required bool
}

// NewFileSource creates a source that reads from a YAML file.
// If required is true, Load returns an error when the file does not exist.
func NewFileSource(path string, required bool) *FileSource {
	return &FileSource{path: path, required: required}
}

// Name returns the source identifier.
func (s *FileSource) Name() string {
	return fmt.Sprintf("file:%s", s.path)
}

// Load reads and parses the YAML file into a flat key-value map.
func (s *FileSource) Load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) && !s.required {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config file %s: %w", s.path, ErrFileRead)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", s.path, ErrFileParse)
	}

	result := make(map[string]string, len(raw))
	flattenMap("", raw, result)
	return result, nil
}

// flattenMap recursively flattens a nested map into dot-separated keys in snake_case.
func flattenMap(prefix string, m map[string]any, out map[string]string) {
	for key, val := range m {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "_" + key
		}
		switch v := val.(type) {
		case map[string]any:
			flattenMap(fullKey, v, out)
		default:
			out[fullKey] = fmt.Sprintf("%v", v)
		}
	}
}

// EnvSource loads configuration from environment variables.
type EnvSource struct {
	prefix string
}

// NewEnvSource creates a source that reads environment variables with the given prefix.
func NewEnvSource(prefix string) *EnvSource {
	return &EnvSource{prefix: prefix}
}

// Name returns the source identifier.
func (s *EnvSource) Name() string {
	return fmt.Sprintf("env:%s*", s.prefix)
}

// Load reads all environment variables matching the prefix and returns them
// as a flat map with the prefix stripped.
func (s *EnvSource) Load() (map[string]string, error) {
	result := make(map[string]string)
	for _, env := range os.Environ() {
		key, val, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		if s.prefix == "" || strings.HasPrefix(key, s.prefix) {
			result[key] = val
		}
	}
	return result, nil
}
