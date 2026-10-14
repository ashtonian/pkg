package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
)

// EnvVar is the environment variable that sets f under prefix — the prefix
// [WithEnvPrefix] was given, with or without its trailing "_". It is empty
// when prefix is.
func (f Field) EnvVar(prefix string) string {
	if prefix == "" {
		return ""
	}
	return envName(strings.TrimSuffix(prefix, "_"), f.Path)
}

// PrintableDefault returns f's `default` tag and true when diagnostics may
// print it, and false when f has no default or it must be withheld: f is
// Sensitive, or its type could hold a sensitive value beneath it — a map or
// interface, whose entries only a decoded value reveals, or a struct, slice
// or array holding a sensitive field or one of those.
func (f Field) PrintableDefault() (string, bool) {
	def, ok := f.Tag.Lookup("default")
	if !ok || f.Sensitive || mayHoldSensitive(f.Type, map[reflect.Type]bool{}) {
		return "", false
	}
	return def, true
}

// usageRow is one key an operator can set.
type usageRow struct {
	Key         string `json:"key"`
	Env         string `json:"env,omitempty"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Description string `json:"description,omitempty"`
	// pattern marks a map entry: Key and Env hold a <key> placeholder.
	pattern    bool
	hasDefault bool
	withheld   bool // the default exists but is not shown
}

// PrintUsage writes every key of T an operator can set: its path, the
// environment variable that sets it under envPrefix (none when envPrefix is
// empty), its type, its `default` tag, whether `validate:"required"` makes it
// mandatory, and its `desc` tag. A map's entries are one row per element
// field, written with a <key> placeholder, and a slice of structs' elements
// one row per element field with an <i> placeholder and no variable. A
// default diagnostics must withhold (see [Field.PrintableDefault]) prints as
// [REDACTED] unless [WithShowSensitive] is set.
//
// [WithFormat] selects a table (the default), JSON (an array of objects) or
// FormatEnv: a template of `export NAME='default'` lines, with a key that has
// no default, a sensitive key and a map entry written as a commented-out
// line; FormatEnv needs an envPrefix. [WithWriter] and [WithSortByName]
// apply; descriptions are always included.
func PrintUsage[T any](envPrefix string, opts ...PrintOption) error {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	rows := usageRows(reflect.TypeFor[T](), strings.TrimSuffix(envPrefix, "_"), po.showSensitive)
	if po.sortByName {
		slices.SortStableFunc(rows, func(a, b usageRow) int { return cmp.Compare(a.Key, b.Key) })
	}
	switch po.format {
	case FormatTable:
		return printUsageTable(po.writer, rows, envPrefix != "")
	case FormatJSON:
		enc := json.NewEncoder(po.writer)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case FormatEnv:
		if envPrefix == "" {
			return errors.New("config: usage in FormatEnv needs an environment prefix")
		}
		return printUsageEnv(po.writer, rows)
	default:
		return fmt.Errorf("config: unknown print format %q (want %q, %q or %q)",
			po.format, FormatTable, FormatJSON, FormatEnv)
	}
}

// usageRows describes every key of root, a map's entries through its
// element: a sensitive map covers all of them.
func usageRows(root reflect.Type, prefix string, showSensitive bool) []usageRow {
	var rows []usageRow
	for _, f := range fieldsOf(root) {
		t := derefType(f.Type)
		isMap := t.Kind() == reflect.Map && t.Key().Kind() == reflect.String
		isList := (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && isBranch(derefType(t.Elem()))
		if !isMap && !isList {
			rows = append(rows, usageRowOf(f, f.Path, f.EnvVar(prefix), showSensitive))
			continue
		}
		if _, ok := f.Tag.Lookup("default"); ok { // the collection's own default, as a whole
			rows = append(rows, usageRowOf(f, f.Path, f.EnvVar(prefix), showSensitive))
		}
		elem := derefType(t.Elem())
		if isList { // elements are set in files; no variable addresses one
			for _, leaf := range fieldsOf(elem) {
				leaf.Sensitive = leaf.Sensitive || f.Sensitive
				rows = append(rows, usagePattern(usageRowOf(leaf, f.Path+"[<i>]."+leaf.Path, "", showSensitive)))
			}
			continue
		}
		key, env := f.Path+".<key>", ""
		if prefix != "" {
			env = f.EnvVar(prefix) + "_<KEY>"
		}
		if !isBranch(elem) {
			entry := Field{Path: f.Path, Tag: reflect.StructTag(fmt.Sprintf("desc:%q", f.Tag.Get("desc"))), Type: t.Elem(), Sensitive: f.Sensitive}
			rows = append(rows, usagePattern(usageRowOf(entry, key, env, showSensitive)))
			continue
		}
		for _, leaf := range fieldsOf(elem) {
			leafEnv := ""
			if prefix != "" {
				leafEnv = env + envName("", leaf.Path)
			}
			leaf.Sensitive = leaf.Sensitive || f.Sensitive
			rows = append(rows, usagePattern(usageRowOf(leaf, key+"."+leaf.Path, leafEnv, showSensitive)))
		}
	}
	return rows
}

func usagePattern(r usageRow) usageRow {
	r.pattern = true
	return r
}

// usageRowOf describes f, shown as key and env.
func usageRowOf(f Field, key, env string, showSensitive bool) usageRow {
	row := usageRow{
		Key:         key,
		Env:         env,
		Type:        f.Type.String(),
		Required:    slices.Contains(strings.Split(f.Tag.Get("validate"), ","), "required"),
		Sensitive:   f.Sensitive,
		Description: f.Tag.Get("desc"),
	}
	def, has := f.Tag.Lookup("default")
	row.hasDefault = has
	if has {
		row.Default = def
		if _, printable := f.PrintableDefault(); !printable && !showSensitive {
			row.Default = redactedValue
			row.withheld = true
		}
	}
	return row
}

func printUsageTable(w io.Writer, rows []usageRow, withEnv bool) error {
	defaultText := func(r usageRow) string {
		if !r.hasDefault && r.Required {
			return "(required)"
		}
		return r.Default
	}
	keyW, envW, typeW, defW := len("KEY"), len("ENV"), len("TYPE"), len("DEFAULT")
	for _, r := range rows {
		keyW = max(keyW, len(r.Key))
		envW = max(envW, len(r.Env))
		typeW = max(typeW, len(r.Type))
		defW = max(defW, len(defaultText(r)))
	}
	ew := &errWriter{w: w}
	if withEnv {
		row := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n", keyW, envW, typeW, defW)
		ew.printf(row, "KEY", "ENV", "TYPE", "DEFAULT", "DESCRIPTION")
		for _, r := range rows {
			ew.printf(row, r.Key, r.Env, r.Type, defaultText(r), r.Description)
		}
		return ew.err
	}
	row := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", keyW, typeW, defW)
	ew.printf(row, "KEY", "TYPE", "DEFAULT", "DESCRIPTION")
	for _, r := range rows {
		ew.printf(row, r.Key, r.Type, defaultText(r), r.Description)
	}
	return ew.err
}

func printUsageEnv(w io.Writer, rows []usageRow) error {
	ew := &errWriter{w: w}
	for i, r := range rows {
		if i > 0 {
			ew.printf("\n")
		}
		if r.Description != "" {
			ew.printf("# %s\n", strings.ReplaceAll(r.Description, "\n", " "))
		}
		note := r.Key + " (" + r.Type + ")"
		if r.Required {
			note += ", required"
		}
		ew.printf("# %s\n", note)
		if r.Env == "" {
			ew.printf("# set in a config file\n")
			continue
		}
		if r.pattern || r.Sensitive || r.withheld || !r.hasDefault {
			ew.printf("# export %s=\n", r.Env)
			continue
		}
		ew.printf("export %s=%s\n", r.Env, shellQuote(r.Default))
	}
	return ew.err
}
