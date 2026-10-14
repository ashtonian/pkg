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
	// cycleValue stands in for a struct pointer, map or slice that leads
	// back to one already being printed. Printing it with %v instead would
	// bypass redaction, or recurse forever.
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
// or map field containing it, is tagged sensitive:"true", or when a name it
// sits under matches a sensitive pattern (password, secret, token, key, ...):
// the leaf field's name, the name of any map or slice containing it, or any
// map key above it. A struct field's name does not cover the struct it holds
// — an Auth block's fields are judged by their own names — but a map key's
// covers everything beneath it. Maps and slices, including dynamic ones held
// in interfaces and nested to any depth, are expanded element by element, so
// no container is printed whole while something inside it is unchecked. It
// returns the first write error, or an error for an unknown format.
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
// [Fields]. Slices and maps are expanded element by element, including
// dynamic values held in interfaces, so a sensitive-looking map key is judged
// like a sensitive-looking field name.
func collectEntries(cfg any, showSensitive bool) []printEntry {
	w := &entryWalk{showSensitive: showSensitive, visiting: map[visit]bool{}}
	val := reflect.ValueOf(cfg)
	for val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return nil
		}
		w.visiting[visitOf(val)] = true
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
	// visiting holds the struct pointers, maps and slices on the current
	// path, so a cycle in the value graph ends instead of recursing forever.
	visiting map[visit]bool
}

// visit identifies a struct pointer, map or slice by its address and type; a
// struct and its first field share an address, so the type is part of it.
type visit struct {
	ptr uintptr
	typ reflect.Type
}

func visitOf(v reflect.Value) visit { return visit{ptr: v.Pointer(), typ: v.Type()} }

// site is where a value sits: the field name or map key a sensitive-name
// check reads, the description to print, and whether an enclosing tag or
// sensitive name already covers it.
type site struct {
	name      string
	desc      string
	sensitive bool
	// keyed marks a map key: a dynamic name whose sensitivity covers
	// everything beneath it, structs included.
	keyed bool
}

// walkStruct emits entries for val's fields. inherited is true beneath a
// field tagged sensitive:"true", so tagging a container hides everything in it.
func (w *entryWalk) walkStruct(val reflect.Value, prefix string, inherited bool) {
	for kf := range keyedFields(val.Type()) {
		w.walkValue(joinPath(prefix, kf.key), val.FieldByIndex(kf.field.Index), site{
			name:      kf.field.Name,
			desc:      kf.field.Tag.Get("desc"),
			sensitive: inherited || kf.field.Tag.Get("sensitive") == "true",
		})
	}
}

// walkValue emits the entries for the value at path.
func (w *entryWalk) walkValue(path string, v reflect.Value, s site) {
	elem := v
	for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Interface {
		if elem.IsNil() {
			w.leaf(path, v, reflect.Value{}, s)
			return
		}
		if elem.Kind() == reflect.Pointer && isBranch(elem.Elem().Type()) {
			if !w.enter(elem) {
				w.cycle(path, v, s)
				return
			}
			defer delete(w.visiting, visitOf(elem))
		}
		elem = elem.Elem()
	}

	named := isSensitiveName(s.name)
	switch {
	case isBranch(elem.Type()):
		w.walkStruct(elem, path, s.sensitive || s.keyed && named)
	case elem.Kind() == reflect.Map && elem.Len() > 0:
		if !w.enter(elem) {
			w.cycle(path, v, s)
			return
		}
		defer delete(w.visiting, visitOf(elem))
		keys := elem.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int {
			return cmp.Compare(fmt.Sprint(a.Interface()), fmt.Sprint(b.Interface()))
		})
		for _, k := range keys {
			key := fmt.Sprint(k.Interface())
			w.walkValue(path+"["+key+"]", elem.MapIndex(k), site{name: key, desc: s.desc, sensitive: s.sensitive || named, keyed: true})
		}
	case (elem.Kind() == reflect.Slice || elem.Kind() == reflect.Array) && elem.Len() > 0 && holdsContainers(elem.Type().Elem()):
		if elem.Kind() == reflect.Slice {
			if !w.enter(elem) {
				w.cycle(path, v, s)
				return
			}
			defer delete(w.visiting, visitOf(elem))
		}
		inner := site{name: s.name, desc: s.desc, sensitive: s.sensitive || named, keyed: s.keyed}
		for i := range elem.Len() {
			w.walkValue(fmt.Sprintf("%s[%d]", path, i), elem.Index(i), inner)
		}
	default:
		// Scalars, TextUnmarshaler structs, empty containers and slices of
		// scalars. A sensitive leaf is redacted whole — including
		// `APIKeys []string`.
		w.leaf(path, v, elem, s)
	}
}

// enter records a struct pointer, map or slice on the current path; false
// means it is already there.
func (w *entryWalk) enter(v reflect.Value) bool {
	k := visitOf(v)
	if w.visiting[k] {
		return false
	}
	w.visiting[k] = true
	return true
}

func (w *entryWalk) cycle(path string, v reflect.Value, s site) {
	w.entries = append(w.entries, printEntry{name: path, value: cycleValue, typeName: v.Type().String(), desc: s.desc})
}

// holdsContainers reports whether slice or array elements of type t may hold
// structs or maps at any depth, and so must be expanded rather than printed
// whole: struct branches, maps, interfaces (whose dynamic value is only known
// per element) and slices or arrays of those. A recursive container type
// (type Tree []Tree) is expanded too; its values end, and the walk's cycle
// guard stops one that does not.
func holdsContainers(t reflect.Type) bool {
	return holdsContainersOf(t, map[reflect.Type]bool{})
}

func holdsContainersOf(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = derefType(t)
	switch {
	case seen[t]:
		return true
	case isBranch(t), t.Kind() == reflect.Map, t.Kind() == reflect.Interface:
		return true
	case t.Kind() == reflect.Slice, t.Kind() == reflect.Array:
		seen[t] = true
		return holdsContainersOf(t.Elem(), seen)
	}
	return false
}

// leaf emits one entry. declared is the value as found, for its type; value
// is it dereferenced, or invalid when nil.
func (w *entryWalk) leaf(path string, declared, value reflect.Value, s site) {
	text := "<nil>"
	typ := declared.Type()
	if value.IsValid() {
		text = fmt.Sprintf("%v", value.Interface())
		if declared.Kind() == reflect.Interface {
			typ = value.Type()
		}
	}
	if !w.showSensitive && (s.sensitive || isSensitiveName(s.name)) {
		text = redactedValue
	}
	w.entries = append(w.entries, printEntry{name: path, value: text, typeName: typ.String(), desc: s.desc})
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

// printEnv writes one POSIX-shell `export NAME='value'` line per entry. NAME
// is the key path upper-cased with every character outside [A-Z0-9_]
// replaced by "_" — map keys and indexes included — and carries no
// environment prefix, so it reads as diagnostics: it is shell-safe to source,
// but a map entry's name need not match the variable the loader would read.
func printEnv(w io.Writer, entries []printEntry, includeDesc bool) error {
	ew := &errWriter{w: w}
	for _, e := range entries {
		if includeDesc && e.desc != "" {
			ew.printf("# %s\n", strings.ReplaceAll(e.desc, "\n", " "))
		}
		ew.printf("export %s=%s\n", shellName(e.name), shellQuote(e.value))
	}
	return ew.err
}

func shellName(path string) string {
	b := []byte(strings.ToUpper(path))
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '_'
		}
	}
	if len(b) == 0 || b[0] >= '0' && b[0] <= '9' {
		b = append([]byte{'_'}, b...)
	}
	return string(b)
}

// shellQuote single-quotes s, closing the quote around each quote it holds.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
