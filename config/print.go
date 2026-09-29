package config

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
)

const (
	redactedValue = "[REDACTED]"
	// cycleValue stands in for a pointer back to a struct already being
	// printed. Printing it with %v instead would bypass redaction.
	cycleValue = "[CYCLE]"
)

// Print formats accepted by [WithFormat].
const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatEnv   = "env"
)

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
	format              string
	showSensitive       bool
	writer              io.Writer
	sortByName          bool
	includeDescriptions bool
}

func defaultPrintOptions() printOptions {
	return printOptions{
		format: FormatTable,
		writer: os.Stdout,
	}
}

// WithFormat sets the output format: [FormatTable] (default), [FormatJSON] or
// [FormatEnv]. Any other value makes PrintRedacted return an error.
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

// WithWriter sets the output destination. Defaults to os.Stdout.
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

// PrintRedacted writes a struct's fields, keyed by koanf path, redacting
// sensitive values. A value is sensitive when its field, or any struct, slice
// or map field containing it, is tagged sensitive:"true", or when the leaf
// field's name matches a sensitive pattern (password, secret, token, key,
// ...). It returns the first write error, or an error for an unknown format.
func PrintRedacted(cfg any, opts ...PrintOption) error {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}

	entries := collectEntries(cfg, po.showSensitive)
	if po.sortByName {
		slices.SortFunc(entries, func(a, b printEntry) int { return cmp.Compare(a.name, b.name) })
	}

	switch po.format {
	case FormatTable:
		return printTable(po.writer, entries, po.includeDescriptions)
	case FormatJSON:
		return printJSON(po.writer, entries, po.includeDescriptions)
	case FormatEnv:
		return printEnv(po.writer, entries, po.includeDescriptions)
	default:
		return fmt.Errorf("config: unknown print format %q (want %q, %q or %q)",
			po.format, FormatTable, FormatJSON, FormatEnv)
	}
}

// ToMap returns a struct's fields as a map keyed by koanf path, redacting
// sensitive values (as [PrintRedacted] defines them) when redact is true.
func ToMap(cfg any, redact bool) map[string]any {
	entries := collectEntries(cfg, !redact)
	result := make(map[string]any, len(entries))
	for _, e := range entries {
		result[e.name] = e.value
	}
	return result
}

// collectEntries flattens cfg into print entries using the same key rules as
// [Fields], expanding slices and maps of structs element by element.
func collectEntries(cfg any, showSensitive bool) []printEntry {
	w := &entryWalk{showSensitive: showSensitive, visiting: map[uintptr]bool{}}
	val := reflect.ValueOf(cfg)
	for val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return nil
		}
		w.visiting[val.Pointer()] = true
		val = val.Elem()
	}
	if !val.IsValid() || !isBranch(val.Type()) {
		return nil
	}
	w.walkStruct(val, "", false)
	return w.entries
}

type entryWalk struct {
	showSensitive bool
	entries       []printEntry
	// visiting holds the struct pointers on the current path, so a pointer
	// cycle in the value graph ends instead of recursing forever.
	visiting map[uintptr]bool
}

// walkStruct emits entries for val's fields. inherited is true beneath a
// field tagged sensitive:"true", so tagging a container hides everything in it.
func (w *entryWalk) walkStruct(val reflect.Value, prefix string, inherited bool) {
	for kf := range keyedFields(val.Type()) {
		sensitive := inherited || kf.field.Tag.Get("sensitive") == "true"
		w.walkValue(joinPath(prefix, kf.key), val.FieldByIndex(kf.field.Index), kf.field, sensitive)
	}
}

// walkValue emits the entries for one value: field is the struct field it
// belongs to (for a slice or map element, the container's field).
func (w *entryWalk) walkValue(name string, v reflect.Value, field reflect.StructField, sensitive bool) {
	elem := v
	for elem.Kind() == reflect.Pointer {
		if elem.IsNil() {
			w.leaf(name, v, v, field, sensitive)
			return
		}
		if isBranch(elem.Elem().Type()) {
			ptr := elem.Pointer()
			if w.visiting[ptr] {
				w.entries = append(w.entries, printEntry{
					name: name, value: cycleValue, typeName: v.Type().String(), desc: field.Tag.Get("desc"),
				})
				return
			}
			w.visiting[ptr] = true
			defer delete(w.visiting, ptr)
		}
		elem = elem.Elem()
	}

	switch {
	case isBranch(elem.Type()):
		w.walkStruct(elem, name, sensitive)
	case (elem.Kind() == reflect.Slice || elem.Kind() == reflect.Array) && isBranch(derefType(elem.Type().Elem())):
		for i := range elem.Len() {
			w.walkValue(fmt.Sprintf("%s[%d]", name, i), elem.Index(i), field, sensitive)
		}
	case elem.Kind() == reflect.Map && isBranch(derefType(elem.Type().Elem())):
		keys := elem.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int {
			return cmp.Compare(fmt.Sprint(a.Interface()), fmt.Sprint(b.Interface()))
		})
		for _, k := range keys {
			w.walkValue(fmt.Sprintf("%s[%v]", name, k.Interface()), elem.MapIndex(k), field, sensitive)
		}
	default:
		// Scalars, TextUnmarshaler structs, and slices/maps of scalars. A
		// sensitive leaf is redacted whole — including `APIKeys []string`.
		w.leaf(name, v, elem, field, sensitive)
	}
}

// leaf emits one entry. declared carries the type to report; value is the
// dereferenced value to print.
func (w *entryWalk) leaf(name string, declared, value reflect.Value, field reflect.StructField, sensitive bool) {
	text := "<nil>"
	if value.Kind() != reflect.Pointer || !value.IsNil() {
		text = fmt.Sprintf("%v", value.Interface())
	}
	if !w.showSensitive && (sensitive || isSensitiveName(field.Name)) {
		text = redactedValue
	}
	w.entries = append(w.entries, printEntry{
		name:     name,
		value:    text,
		typeName: declared.Type().String(),
		desc:     field.Tag.Get("desc"),
	})
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

// errWriter keeps the first write error so a run of writes is checked once.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

func printTable(w io.Writer, entries []printEntry, includeDesc bool) error {
	nameW, valW, typeW := len("FIELD"), len("VALUE"), len("TYPE")
	for _, e := range entries {
		nameW = max(nameW, len(e.name))
		valW = max(valW, len(e.value))
		typeW = max(typeW, len(e.typeName))
	}

	ew := &errWriter{w: w}
	if includeDesc {
		row := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, valW, typeW)
		ew.printf(row, "FIELD", "VALUE", "TYPE", "DESCRIPTION")
		ew.printf("%s\n", strings.Repeat("-", nameW+valW+typeW+30))
		for _, e := range entries {
			ew.printf(row, e.name, e.value, e.typeName, e.desc)
		}
		return ew.err
	}
	row := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", nameW, valW)
	ew.printf(row, "FIELD", "VALUE", "TYPE")
	ew.printf("%s\n", strings.Repeat("-", nameW+valW+20))
	for _, e := range entries {
		ew.printf(row, e.name, e.value, e.typeName)
	}
	return ew.err
}

func printJSON(w io.Writer, entries []printEntry, includeDesc bool) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
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
		return enc.Encode(m)
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.name] = e.value
	}
	return enc.Encode(m)
}

func printEnv(w io.Writer, entries []printEntry, includeDesc bool) error {
	ew := &errWriter{w: w}
	for _, e := range entries {
		if includeDesc && e.desc != "" {
			ew.printf("# %s\n", e.desc)
		}
		ew.printf("export %s=%s\n", strings.ToUpper(strings.ReplaceAll(e.name, ".", "_")), e.value)
	}
	return ew.err
}
