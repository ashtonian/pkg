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

		// Deref a pointer for kind inspection; a nil pointer is a leaf.
		fvElem, ftElem := fv, ft
		if ftElem.Kind() == reflect.Ptr {
			if fv.IsNil() {
				entries = append(entries, leafEntry(name, fv, field, showSensitive))
				continue
			}
			fvElem, ftElem = fv.Elem(), ftElem.Elem()
		}

		// Recurse into nested structs (but not time.Time, whose PkgPath is
		// "time"). Recursion redacts INNER sensitive fields individually — a
		// struct whose NAME merely matches a sensitive pattern (e.g. an "auth"
		// or "credential" sub-config) is NOT redacted whole, only its sensitive
		// leaf fields are.
		if ftElem.Kind() == reflect.Struct && ftElem.PkgPath() != "time" {
			entries = append(entries, walkStruct(fvElem.Interface(), name, showSensitive)...)
			continue
		}

		// Recurse into slices/arrays/maps whose ELEMENT is a struct, so inner
		// sensitive fields are redacted rather than stringified whole — the
		// secret leak the leaf branch caused for e.g. []Upstream.
		switch ftElem.Kind() {
		case reflect.Slice, reflect.Array:
			if isWalkableStruct(ftElem.Elem()) {
				for j := range fvElem.Len() {
					idx := fmt.Sprintf("%s[%d]", name, j)
					entries = append(entries, walkStruct(fvElem.Index(j).Interface(), idx, showSensitive)...)
				}
				continue
			}
		case reflect.Map:
			if isWalkableStruct(ftElem.Elem()) {
				it := fvElem.MapRange()
				for it.Next() {
					keyed := fmt.Sprintf("%s[%v]", name, it.Key().Interface())
					entries = append(entries, walkStruct(it.Value().Interface(), keyed, showSensitive)...)
				}
				continue
			}
		}

		// Leaf: scalars, time values, and slices/maps of scalars. A sensitive
		// leaf (tag OR name) is redacted whole — including a scalar slice/map of
		// secrets like `APIKeys []string`.
		entries = append(entries, leafEntry(name, fv, field, showSensitive))
	}
	return entries
}

// leafEntry builds a non-recursive print entry, redacting the value when the
// field is sensitive (explicit `sensitive:"true"` tag OR a sensitive-looking
// name) and secrets are not being shown.
func leafEntry(name string, fv reflect.Value, field reflect.StructField, showSensitive bool) printEntry {
	value := fmt.Sprintf("%v", fv.Interface())
	if !showSensitive && (field.Tag.Get("sensitive") == "true" || isSensitiveName(field.Name)) {
		value = redactedValue
	}
	return printEntry{
		name:     name,
		value:    value,
		typeName: field.Type.String(),
		desc:     field.Tag.Get("desc"),
	}
}

// isWalkableStruct reports whether t (or the type *t points at) is a struct
// walkStruct recurses into for redaction — any struct except time.Time.
func isWalkableStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && t.PkgPath() != "time"
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
