package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const redactedValue = "[REDACTED]"

// PrintOption configures output of Print.
type PrintOption func(*printOptions)

type printOptions struct {
	format       string
	showSensitive bool
	writer       io.Writer
	sortByName   bool
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

// WithSortByName sorts output fields alphabetically by env name.
func WithSortByName() PrintOption {
	return func(o *printOptions) {
		o.sortByName = true
	}
}

// printConfig writes the loaded config fields to the writer in the requested format.
func printConfig(fields []fieldMeta, values map[string]string, opts ...PrintOption) {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}

	entries := buildEntries(fields, values, po.showSensitive)

	if po.sortByName {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].envName < entries[j].envName
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

type printEntry struct {
	envName string
	value   string
	typeName string
}

func buildEntries(fields []fieldMeta, values map[string]string, showSensitive bool) []printEntry {
	entries := make([]printEntry, 0, len(fields))
	for _, f := range fields {
		val := values[f.EnvName]
		if f.Sensitive && !showSensitive {
			val = redactedValue
		}
		entries = append(entries, printEntry{
			envName:  f.EnvName,
			value:    val,
			typeName: f.FieldType.String(),
		})
	}
	return entries
}

func printTable(w io.Writer, entries []printEntry) {
	// Find max widths for alignment.
	nameW, valW := len("ENV VARIABLE"), len("VALUE")
	for _, e := range entries {
		if len(e.envName) > nameW {
			nameW = len(e.envName)
		}
		if len(e.value) > valW {
			valW = len(e.value)
		}
	}

	fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", nameW, valW)
	fmt.Fprintf(w, fmtStr, "ENV VARIABLE", "VALUE", "TYPE")
	fmt.Fprintln(w, strings.Repeat("-", nameW+valW+20))
	for _, e := range entries {
		fmt.Fprintf(w, fmtStr, e.envName, e.value, e.typeName)
	}
}

func printJSON(w io.Writer, entries []printEntry) {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.envName] = e.value
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

func printEnv(w io.Writer, entries []printEntry) {
	for _, e := range entries {
		fmt.Fprintf(w, "export %s=%s\n", e.envName, e.value)
	}
}

// toMap converts loaded fields into a map, optionally redacting sensitive values.
func toMap(fields []fieldMeta, values map[string]string, redact bool) map[string]any {
	result := make(map[string]any, len(fields))
	for _, f := range fields {
		val := values[f.EnvName]
		if f.Sensitive && redact {
			val = redactedValue
		}
		result[f.EnvName] = val
	}
	return result
}
