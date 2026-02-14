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

// TranslateFunc translates an error code, field, tag, and param into a localized string.
// This type is designed to be adapted from pkg/i18n externally without
// the config package importing pkg/i18n directly.
type TranslateFunc func(code, field, tag, param string) string

// PrintOption configures output of PrintRedacted.
type PrintOption func(*printOptions)

type printOptions struct {
	format              string
	showSensitive       bool
	writer              io.Writer
	sortByName          bool
	translator          TranslateFunc
	includeDescriptions bool
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

// WithTranslator sets a translation function for localized error messages
// in validation output. The translator is stored for consumers who call
// ToConfigErrors and ConfigError.LocalizedMessage.
func WithTranslator(fn TranslateFunc) PrintOption {
	return func(o *printOptions) {
		o.translator = fn
	}
}

// WithIncludeDescriptions includes desc:"" tag values in the printed output.
// When set, the table, JSON, and env output includes the desc:"description"
// tag value alongside each field.
func WithIncludeDescriptions() PrintOption {
	return func(o *printOptions) {
		o.includeDescriptions = true
	}
}

type printEntry struct {
	name     string
	value    string
	typeName string
	desc     string
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
		printJSON(po.writer, entries, po.includeDescriptions)
	case "env":
		printEnv(po.writer, entries, po.includeDescriptions)
	default:
		printTable(po.writer, entries, po.includeDescriptions)
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
			desc:     field.Tag.Get("desc"),
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

func printTable(w io.Writer, entries []printEntry, includeDesc bool) {
	nameW, valW, typeW := len("FIELD"), len("VALUE"), len("TYPE")
	for _, e := range entries {
		if len(e.name) > nameW {
			nameW = len(e.name)
		}
		if len(e.value) > valW {
			valW = len(e.value)
		}
		if len(e.typeName) > typeW {
			typeW = len(e.typeName)
		}
	}

	if includeDesc {
		fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, valW, typeW)
		fmt.Fprintf(w, fmtStr, "FIELD", "VALUE", "TYPE", "DESCRIPTION")
		fmt.Fprintln(w, strings.Repeat("-", nameW+valW+typeW+30))
		for _, e := range entries {
			fmt.Fprintf(w, fmtStr, e.name, e.value, e.typeName, e.desc)
		}
	} else {
		fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", nameW, valW)
		fmt.Fprintf(w, fmtStr, "FIELD", "VALUE", "TYPE")
		fmt.Fprintln(w, strings.Repeat("-", nameW+valW+20))
		for _, e := range entries {
			fmt.Fprintf(w, fmtStr, e.name, e.value, e.typeName)
		}
	}
}

func printJSON(w io.Writer, entries []printEntry, includeDesc bool) {
	if includeDesc {
		type jsonEntry struct {
			Value       string `json:"value"`
			Type        string `json:"type"`
			Description string `json:"description,omitempty"`
		}
		m := make(map[string]jsonEntry, len(entries))
		for _, e := range entries {
			m[e.name] = jsonEntry{Value: e.value, Type: e.typeName, Description: e.desc}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
	} else {
		m := make(map[string]string, len(entries))
		for _, e := range entries {
			m[e.name] = e.value
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
	}
}

func printEnv(w io.Writer, entries []printEntry, includeDesc bool) {
	for _, e := range entries {
		envName := strings.ToUpper(strings.ReplaceAll(e.name, ".", "_"))
		if includeDesc && e.desc != "" {
			fmt.Fprintf(w, "# %s\n", e.desc)
		}
		fmt.Fprintf(w, "export %s=%s\n", envName, e.value)
	}
}
