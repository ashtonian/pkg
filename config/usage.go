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
}

// PrintUsage writes every key of T an operator can set: its path, the
// environment variable that sets it under envPrefix (none when envPrefix is
// empty), its type, its `default` tag, whether `validate:"required"` makes it
// mandatory, and its `desc` tag. A map's entries are one row per element
// field, written with a <key> placeholder. A sensitive key's default prints
// as [REDACTED] unless [WithShowSensitive] is set.
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
		if t.Kind() != reflect.Map || t.Key().Kind() != reflect.String {
			rows = append(rows, usageRowOf(f, f.Path, f.EnvVar(prefix), showSensitive))
			continue
		}
		key, env := f.Path+".<key>", ""
		if prefix != "" {
			env = f.EnvVar(prefix) + "_<KEY>"
		}
		elem := derefType(t.Elem())
		if !isBranch(elem) {
			entry := Field{Path: f.Path, Tag: f.Tag, Type: t.Elem(), Sensitive: f.Sensitive}
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
	row.Default, row.hasDefault = f.Tag.Lookup("default")
	if row.hasDefault && f.Sensitive && !showSensitive {
		row.Default = redactedValue
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
		if r.pattern || r.Sensitive || !r.hasDefault {
			ew.printf("# export %s=\n", r.Env)
			continue
		}
		ew.printf("export %s=%s\n", r.Env, shellQuote(r.Default))
	}
	return ew.err
}
