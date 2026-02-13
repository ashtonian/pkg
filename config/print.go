package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
)

const redactedValue = "[REDACTED]"

// sensitivePatterns are substrings that trigger auto-detection of sensitive fields.
// This is a fixed-size array (not a slice) so it cannot be appended to or
// reassigned — safe for concurrent reads without synchronisation.
var sensitivePatterns = [...]string{
	"password", "secret", "token", "key", "credential",
	"auth", "private", "apikey", "api_key",
}

// PrintOption configures output of PrintRedacted.
type PrintOption func(*printOptions)

type printOptions struct {
	format        string
	showSensitive bool
	writer        io.Writer
	sortByName    bool
}

func defaultPrintOptions() printOptions {
	return printOptions{
		format: "table",
		writer: os.Stdout,
	}
}

// WithFormat sets the output format: "table", "json", or "env".
func WithFormat(format string) PrintOption {
	return func(o *printOptions) {
		o.format = format
	}
}

// WithShowSensitive includes sensitive values in the output instead of redacting.
func WithShowSensitive(show bool) PrintOption {
	return func(o *printOptions) {
		o.showSensitive = show
	}
}

// WithWriter sets the output destination.
func WithWriter(w io.Writer) PrintOption {
	return func(o *printOptions) {
		o.writer = w
	}
}

// WithSortByName sorts output fields alphabetically by name.
func WithSortByName() PrintOption {
	return func(o *printOptions) {
		o.sortByName = true
	}
}

type printEntry struct {
	name     string
	value    string
	typeName string
}

// PrintRedacted prints a struct's fields as a table, redacting any field
// tagged sensitive:"true" or whose name matches sensitive patterns
// (password, secret, token, key, etc.). Operates on any struct value.
func PrintRedacted(cfg any, opts ...PrintOption) {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}

	entries := walkStruct(cfg, "", po.showSensitive)

	if po.sortByName {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].name < entries[j].name
		})
	}

	switch po.format {
	case "json":
		printJSON(po.writer, entries)
	case "env":
		printEnv(po.writer, entries)
	default:
		printTable(po.writer, entries)
	}
}

// ToMap returns a struct's fields as a map with optional redaction of
// sensitive fields.
func ToMap(cfg any, redact bool) map[string]any {
	entries := walkStruct(cfg, "", !redact)
	result := make(map[string]any, len(entries))
	for _, e := range entries {
		result[e.name] = e.value
	}
	return result
}

// walkStruct recursively walks a struct and builds print entries.
func walkStruct(v any, prefix string, showSensitive bool) []printEntry {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil
	}

	t := val.Type()
	var entries []printEntry

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		koanfTag := field.Tag.Get("koanf")
		if koanfTag == "-" {
			continue
		}

		name := koanfTag
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		if prefix != "" {
			name = prefix + "." + name
		}

		fv := val.Field(i)
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		// Recurse into nested structs (but not time.Duration which is int64).
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			nested := walkStruct(fv.Interface(), name, showSensitive)
			entries = append(entries, nested...)
			continue
		}

		// Determine if sensitive.
		sensitive := field.Tag.Get("sensitive") == "true" || isSensitiveName(field.Name)
		displayVal := fmt.Sprintf("%v", fv.Interface())
		if sensitive && !showSensitive {
			displayVal = redactedValue
		}

		entries = append(entries, printEntry{
			name:     name,
			value:    displayVal,
			typeName: field.Type.String(),
		})
	}
	return entries
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

func printTable(w io.Writer, entries []printEntry) {
	nameW, valW := len("FIELD"), len("VALUE")
	for _, e := range entries {
		if len(e.name) > nameW {
			nameW = len(e.name)
		}
		if len(e.value) > valW {
			valW = len(e.value)
		}
	}

	fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", nameW, valW)
	fmt.Fprintf(w, fmtStr, "FIELD", "VALUE", "TYPE")
	fmt.Fprintln(w, strings.Repeat("-", nameW+valW+20))
	for _, e := range entries {
		fmt.Fprintf(w, fmtStr, e.name, e.value, e.typeName)
	}
}

func printJSON(w io.Writer, entries []printEntry) {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.name] = e.value
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

func printEnv(w io.Writer, entries []printEntry) {
	for _, e := range entries {
		envName := strings.ToUpper(strings.ReplaceAll(e.name, ".", "_"))
		fmt.Fprintf(w, "export %s=%s\n", envName, e.value)
	}
}
