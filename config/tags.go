package config

import (
	"reflect"
	"strings"
	"unicode"
)

// fieldMeta holds parsed struct tag metadata for a single config field.
type fieldMeta struct {
	// FieldName is the Go struct field name.
	FieldName string
	// EnvName is the resolved environment variable name (with prefix).
	EnvName string
	// FileKey is the resolved YAML/JSON file key (snake_case).
	FileKey string
	// DefaultValue is the default from the config tag or default tag.
	DefaultValue string
	// HasDefault indicates whether a default was specified.
	HasDefault bool
	// Required indicates the field must have a non-zero value.
	Required bool
	// Sensitive indicates the field value should be redacted in output.
	Sensitive bool
	// Description is the human-readable field description.
	Description string
	// SecretKey is the key for secret provider lookup.
	SecretKey string
	// Validate is the go-playground/validator tag value.
	Validate string
	// FieldIndex is the struct field index path for nested access.
	FieldIndex []int
	// FieldType is the reflect.Type of the field.
	FieldType reflect.Type
}

// sensitivePatterns are substrings that trigger auto-detection of sensitive fields.
var sensitivePatterns = []string{
	"password", "secret", "token", "key", "credential",
	"auth", "private", "apikey", "api_key",
}

// parseStructFields extracts fieldMeta from a struct type, recursing into nested structs.
func parseStructFields(t reflect.Type, prefix string, indexPath []int) []fieldMeta {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var fields []fieldMeta
	for i := range t.NumField() {
		field := t.Field(i)

		if !field.IsExported() {
			continue
		}

		currentIndex := append(append([]int(nil), indexPath...), i)

		configTag := field.Tag.Get("config")
		if configTag == "-" {
			continue
		}

		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		// Recurse into nested structs (but not time.Duration, which is int64).
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			nestedPrefix := prefix + toUpperSnake(field.Name) + "_"
			nested := parseStructFields(ft, nestedPrefix, currentIndex)
			fields = append(fields, nested...)
			continue
		}

		meta := parseFieldMeta(field, prefix, currentIndex)
		fields = append(fields, meta)
	}
	return fields
}

// parseFieldMeta parses a single struct field's tags into fieldMeta.
func parseFieldMeta(field reflect.StructField, prefix string, indexPath []int) fieldMeta {
	configTag := field.Tag.Get("config")

	var envName, defaultValue string
	var hasDefault bool

	if configTag != "" {
		parts := strings.SplitN(configTag, ",", 2)
		envName = parts[0]
		if len(parts) > 1 {
			defaultValue = parts[1]
			hasDefault = true
		}
	}

	// Apply nesting prefix. Both auto-derived and explicit tag names get the
	// struct nesting prefix (e.g., "DSN" inside Database becomes "DATABASE_DSN").
	// The top-level WithEnvPrefix is applied separately in NewLoader.
	if envName == "" {
		envName = prefix + toUpperSnake(field.Name)
	} else {
		envName = prefix + envName
	}

	// Check for default tag as alternative.
	if !hasDefault {
		if defTag := field.Tag.Get("default"); defTag != "" {
			defaultValue = defTag
			hasDefault = true
		}
	}

	fileKey := toSnakeCase(envName)

	required := field.Tag.Get("required") == "true"
	sensitive := field.Tag.Get("sensitive") == "true"
	if !sensitive {
		sensitive = isSensitiveName(field.Name)
	}

	return fieldMeta{
		FieldName:    field.Name,
		EnvName:      envName,
		FileKey:      fileKey,
		DefaultValue: defaultValue,
		HasDefault:   hasDefault,
		Required:     required,
		Sensitive:    sensitive,
		Description:  field.Tag.Get("desc"),
		SecretKey:    field.Tag.Get("secret"),
		Validate:     field.Tag.Get("validate"),
		FieldIndex:   indexPath,
		FieldType:    field.Type,
	}
}

// isSensitiveName checks if a field name contains sensitive-looking substrings.
func isSensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// toUpperSnake converts a CamelCase name to UPPER_SNAKE_CASE.
// Example: "ServerHost" -> "SERVER_HOST", "DBMaxConns" -> "DB_MAX_CONNS".
func toUpperSnake(name string) string {
	var result strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			// Insert underscore before an uppercase letter that follows a lowercase,
			// or before an uppercase letter followed by a lowercase (for acronyms).
			if unicode.IsLower(prev) {
				result.WriteRune('_')
			} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				result.WriteRune('_')
			}
		}
		result.WriteRune(unicode.ToUpper(r))
	}
	return result.String()
}

// toSnakeCase converts an UPPER_SNAKE_CASE or CamelCase name to lower_snake_case.
func toSnakeCase(name string) string {
	return strings.ToLower(toUpperSnake(name))
}
