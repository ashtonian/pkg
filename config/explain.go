package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// explainRow is one key of an explanation.
type explainRow struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	Input      Source   `json:"input"`
	Transforms []Source `json:"transforms,omitempty"`
	Exact      bool     `json:"exact"`
}

// PrintExplain writes where each value of a snapshot's config came from: the
// key, the value as [PrintRedacted] shows it — sensitive values redacted —
// the input source the merge kept, and the stages that changed it after
// decoding. A snapshot recorded by [LoadSnapshot] or a reloader
// [WithProvenance] has provenance; any other is an error.
//
// [WithFormat] selects FormatTable (the default) or FormatJSON (an array of
// rows); keys come in declaration order, or by name with [WithSortByName].
// It returns the first write error, or an error for any other format.
func PrintExplain[T any](s Snapshot[T], opts ...PrintOption) error {
	if s.Provenance == nil {
		return errors.New("config: the snapshot has no provenance; load it with LoadSnapshot or a reloader WithProvenance")
	}
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	format := po.formatOr(FormatTable)
	if format != FormatTable && format != FormatJSON {
		return fmt.Errorf("config: PrintExplain writes %q or %q, not %q", FormatTable, FormatJSON, format)
	}

	var rows []explainRow
	root, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return err
	}
	values := map[string]string{}
	entries, err := collectEntriesChecked(s.Config)
	if err != nil {
		return err
	}
	for _, e := range entries {
		values[e.name] = e.value
	}
	seen := make(map[string]bool, len(s.Provenance))
	if err := compareEvery(root, rootValue(s.Config), func(d *delta) {
		key := d.path.String()
		o, ok := s.Provenance[key]
		if !ok {
			return
		}
		value, ok := values[key]
		if !ok {
			value = d.value(0).Text // scalar lists have one printer row, but one origin per element
		}
		seen[key] = true
		rows = append(rows, explainRow{Key: key, Value: value, Input: o.Input, Transforms: o.Transforms, Exact: o.Exact})
	}); err != nil {
		return err
	}
	// A value no walk reaches — beneath a container compared whole — keeps
	// its provenance row, with no value shown.
	for _, key := range slices.Sorted(maps.Keys(s.Provenance)) {
		if !seen[key] {
			o := s.Provenance[key]
			rows = append(rows, explainRow{Key: key, Value: redactedValue, Input: o.Input, Transforms: o.Transforms, Exact: o.Exact})
		}
	}
	if po.sortByName {
		slices.SortFunc(rows, func(a, b explainRow) int { return cmp.Compare(a.Key, b.Key) })
	}

	if format == FormatJSON {
		enc := json.NewEncoder(po.writer)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	return explainTable(po.writer, rows)
}

func explainTable(w io.Writer, rows []explainRow) error {
	keyW, valW := len("KEY"), len("VALUE")
	sources := make([]string, len(rows))
	for i, r := range rows {
		keyW, valW = max(keyW, len(r.Key)), max(valW, len(r.Value))
		var b strings.Builder
		b.WriteString(r.Input.String())
		for _, t := range r.Transforms {
			b.WriteString(" → " + t.String())
		}
		if !r.Exact {
			b.WriteString(" (inexact)")
		}
		sources[i] = b.String()
	}
	ew := &errWriter{w: w}
	row := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", keyW, valW)
	ew.printf(row, "KEY", "VALUE", "SOURCE")
	ew.printf("%s\n", strings.Repeat("-", keyW+valW+30))
	for i, r := range rows {
		ew.printf(row, r.Key, r.Value, sources[i])
	}
	return ew.err
}
