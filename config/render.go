package config

import (
	"bytes"
	"cmp"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"go.yaml.in/yaml/v3"
)

// outNode is a value laid out for output, in order: a scalar, a mapping, a
// sequence or null. Configs, templates and environment output are built as
// outNode trees and written by one writer per format.
type outNode struct {
	kind     outKind
	scalar   any  // outScalar: string, bool, int64, uint64 or float64
	redacted bool // a scalar standing in for a sensitive value
	entries  []outEntry
	items    []*outNode
}

type outKind uint8

const (
	outNull outKind = iota
	outScalar
	outMap
	outList
)

// outEntry is one key of a mapping. A template comments an entry out — it,
// and everything beneath it — and gives it comment lines; a config's tree
// has neither.
type outEntry struct {
	key       string
	node      *outNode
	comments  []string
	commented bool
}

func scalarNode(v any) *outNode { return &outNode{kind: outScalar, scalar: v} }

func redactedNode() *outNode { return &outNode{kind: outScalar, scalar: redactedValue, redacted: true} }

// valueTree lays out a config value with every sensitive value redacted: the
// rule PrintRedacted applies. A sensitive map, list or interface is one
// redacted value, so the keys it holds, which may themselves be secrets, are
// not shown. A value graph that loops back shows [CYCLE].
type valueTree struct {
	err      error
	visiting map[visit]bool
}

func (w *valueTree) root(cfg any) *outNode {
	v := reflect.ValueOf(cfg)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return &outNode{kind: outMap}
		}
		v = v.Elem()
	}
	if !v.IsValid() || !isBranch(v.Type()) {
		return &outNode{kind: outMap}
	}
	n := &outNode{kind: outMap}
	w.fields(n, v, false)
	return n
}

// fields adds struct val's keys to mapping n, squashed structs' in place.
func (w *valueTree) fields(n *outNode, val reflect.Value, inherited bool) {
	if w.err != nil {
		return
	}
	schema, err := metadataOf(val.Type())
	if err != nil {
		w.err = err
		return
	}
	for _, f := range schema.fields {
		fv, err := f.value(val)
		if errors.Is(err, gowalker.ErrMissing) {
			continue
		}
		if err != nil {
			w.err = fmt.Errorf("%w: %w", ErrSchema, err)
			return
		}
		n.entries = append(n.entries, outEntry{key: f.key, node: w.value(fv, site{named: f.named, sensitive: inherited || f.tagged})})
	}
}

func (w *valueTree) value(v reflect.Value, s site) *outNode {
	elem := v
	for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Interface {
		if elem.IsNil() {
			return &outNode{kind: outNull}
		}
		if elem.Kind() == reflect.Pointer {
			if !w.enter(elem) {
				return scalarNode(cycleValue)
			}
			defer delete(w.visiting, visitOf(elem))
		}
		elem = elem.Elem()
	}
	named := s.named
	t := elem.Type()
	switch {
	case isBranch(t):
		n := &outNode{kind: outMap}
		w.fields(n, elem, s.sensitive || s.keyed && named)
		return n
	case expandable(t):
		if (elem.Kind() == reflect.Map || elem.Kind() == reflect.Slice) && elem.IsNil() {
			return &outNode{kind: outNull}
		}
		if s.sensitive || named {
			return redactedNode() // its keys and elements may be secrets
		}
		if elem.Kind() != reflect.Array {
			if !w.enter(elem) {
				return scalarNode(cycleValue)
			}
			defer delete(w.visiting, visitOf(elem))
		}
		if elem.Kind() == reflect.Map {
			n := &outNode{kind: outMap}
			keys := elem.MapKeys()
			slices.SortFunc(keys, func(a, b reflect.Value) int { return cmp.Compare(a.String(), b.String()) })
			for _, k := range keys {
				n.entries = append(n.entries, outEntry{key: k.String(), node: w.value(elem.MapIndex(k), site{named: isSensitiveName(k.String()), keyed: true})})
			}
			return n
		}
		n := &outNode{kind: outList}
		for i := range elem.Len() {
			n.items = append(n.items, w.value(elem.Index(i), site{named: s.named, keyed: s.keyed}))
		}
		return n
	}
	if s.sensitive || named || uncheckedHolder(elem) {
		return redactedNode()
	}
	return scalarNode(scalarOf(elem))
}

func (w *valueTree) enter(v reflect.Value) bool {
	k := visitOf(v)
	if w.visiting[k] {
		return false
	}
	w.visiting[k] = true
	return true
}

// expandable reports a map keyed by strings, or a slice or array, whose
// entries the tree shows one by one.
func expandable(t reflect.Type) bool {
	if opaque(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Map:
		return t.Key().Kind() == reflect.String
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() != reflect.Uint8
	}
	return false
}

var durationType = reflect.TypeFor[time.Duration]()

// scalarOf is the value v decodes from: a duration as its text (30s), a type
// that marshals to text as that text, and other scalars as themselves.
// Loading the result back decodes to v.
func scalarOf(v reflect.Value) any {
	if v.Type() == durationType {
		return time.Duration(v.Int()).String()
	}
	if m, ok := textMarshaler(v); ok {
		if b, err := m.MarshalText(); err == nil {
			return string(b)
		}
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return string(v.Bytes())
		}
	}
	return fmt.Sprintf("%v", v.Interface())
}

func textMarshaler(v reflect.Value) (encoding.TextMarshaler, bool) {
	if m, ok := v.Interface().(encoding.TextMarshaler); ok {
		return m, true
	}
	if v.CanAddr() {
		if m, ok := v.Addr().Interface().(encoding.TextMarshaler); ok {
			return m, true
		}
	}
	return nil, false
}

// plain converts an outNode tree to maps, slices and scalars.
func (n *outNode) plain() any {
	switch n.kind {
	case outScalar:
		return n.scalar
	case outMap:
		m := make(map[string]any, len(n.entries))
		for _, e := range n.entries {
			if !e.commented {
				m[e.key] = e.node.plain()
			}
		}
		return m
	case outList:
		l := make([]any, len(n.items))
		for i, item := range n.items {
			l[i] = item.plain()
		}
		return l
	}
	return nil
}

// RedactedTree returns cfg's keys and values as a nested tree — maps keyed as
// the config's keys are, lists, and scalars of the types the config decodes
// from (a duration as "30s", a time as its text) — with every sensitive value
// replaced by "[REDACTED]", as [PrintRedacted] judges it; a sensitive map,
// list or interface is replaced whole, its keys included. Marshal it with any
// koanf parser to show a loaded config as YAML, JSON, TOML or dotenv. Loading
// the result back gives the same config, apart from what was redacted.
// It returns nil on a structural schema error; use [RedactedTreeChecked]
// to receive that error.
func RedactedTree(cfg any) map[string]any {
	result, _ := RedactedTreeChecked(cfg)
	return result
}

// RedactedTreeChecked returns [RedactedTree]'s values, or nil and ErrSchema
// when a reached type has invalid or ambiguous keys. No partial tree is returned.
// It does not decode default or example tags.
func RedactedTreeChecked(cfg any) (map[string]any, error) {
	w := &valueTree{visiting: map[visit]bool{}}
	n := w.root(cfg)
	if w.err != nil {
		return nil, w.err
	}
	m, _ := n.plain().(map[string]any)
	return m, nil
}

// yamlWriter writes an outNode mapping as block YAML, in entry order, with
// comments and commented-out entries.
type yamlWriter struct {
	ew *errWriter
}

func (y *yamlWriter) line(indent int, commented bool, commentIndent int, text string) {
	if commented {
		y.ew.printf("%s# %s%s\n", strings.Repeat(" ", commentIndent), strings.Repeat(" ", indent-commentIndent), text)
		return
	}
	y.ew.printf("%s%s\n", strings.Repeat(" ", indent), text)
}

// mapping writes n's entries at indent. Within a commented-out entry every
// line is commented at commentIndent, the entry's own indentation. lead, when
// set, starts the first entry's line ("- " for a list item) at indent-len(lead).
func (y *yamlWriter) mapping(n *outNode, indent int, commented bool, commentIndent int, lead string) {
	for i, e := range n.entries {
		at, prefix := indent, ""
		if i == 0 && lead != "" {
			at, prefix = indent-len(lead), lead
		}
		// An entry's comments describe it, so they stay comments when the
		// entry is commented out; inside a commented-out entry they are
		// commented out with everything else.
		for _, text := range e.comments {
			y.line(at, commented, commentIndent, "# "+text)
		}
		c, ci := commented, commentIndent
		if e.commented && !commented {
			c, ci = true, at
		}
		y.entry(at, prefix, e.key, e.node, indent, c, ci)
	}
}

// entry writes one key at indent, its first line led by prefix; what it holds
// is written beneath it at nested+2.
func (y *yamlWriter) entry(indent int, prefix, key string, n *outNode, nested int, commented bool, commentIndent int) {
	k := prefix + yamlScalar(key)
	switch {
	case n == nil:
		y.line(indent, commented, commentIndent, k+":")
	case n.kind == outMap && len(n.entries) > 0:
		y.line(indent, commented, commentIndent, k+":")
		y.mapping(n, nested+2, commented, commentIndent, "")
	case n.kind == outList && len(n.items) > 0:
		y.line(indent, commented, commentIndent, k+":")
		y.sequence(n, nested+2, commented, commentIndent)
	default:
		y.line(indent, commented, commentIndent, k+": "+yamlInline(n))
	}
}

// sequence writes n's items at indent, each led by "- ".
func (y *yamlWriter) sequence(n *outNode, indent int, commented bool, commentIndent int) {
	for _, item := range n.items {
		switch {
		case item.kind == outMap && len(item.entries) > 0:
			y.mapping(item, indent+2, commented, commentIndent, "- ")
		case item.kind == outList && len(item.items) > 0:
			y.line(indent, commented, commentIndent, "-")
			y.sequence(item, indent+2, commented, commentIndent)
		default:
			y.line(indent, commented, commentIndent, "- "+yamlInline(item))
		}
	}
}

// yamlInline writes a scalar, null, or an empty mapping or sequence.
func yamlInline(n *outNode) string {
	switch n.kind {
	case outScalar:
		return yamlScalar(n.scalar)
	case outMap:
		return "{}"
	case outList:
		return "[]"
	}
	return "null"
}

// yamlScalar writes v as one YAML scalar, quoted where YAML would read it as
// something else; text YAML would write over several lines is written as a
// JSON string, which YAML reads as a double-quoted scalar.
func yamlScalar(v any) string {
	b, err := yaml.Marshal(v)
	text := strings.TrimSuffix(string(b), "\n")
	if err != nil || strings.Contains(text, "\n") {
		j, _ := json.Marshal(v)
		return string(j)
	}
	return text
}

// writeJSON writes an outNode as indented JSON in entry order, leaving out
// commented entries.
func writeJSON(w io.Writer, n *outNode) error {
	var b bytes.Buffer
	jsonValue(&b, n, 0)
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

func jsonValue(b *bytes.Buffer, n *outNode, indent int) {
	pad := func(i int) { b.WriteString(strings.Repeat("  ", i)) }
	switch n.kind {
	case outScalar:
		j, _ := json.Marshal(n.scalar)
		b.Write(j)
	case outMap:
		var live []outEntry
		for _, e := range n.entries {
			if !e.commented {
				live = append(live, e)
			}
		}
		if len(live) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, e := range live {
			pad(indent + 1)
			k, _ := json.Marshal(e.key)
			b.Write(k)
			b.WriteString(": ")
			jsonValue(b, e.node, indent+1)
			if i < len(live)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(indent)
		b.WriteByte('}')
	case outList:
		if len(n.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range n.items {
			pad(indent + 1)
			jsonValue(b, item, indent+1)
			if i < len(n.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(indent)
		b.WriteByte(']')
	default:
		b.WriteString("null")
	}
}

// scalarText is a scalar as the environment layer reads it.
func scalarText(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case bool:
		return strconv.FormatBool(s)
	case int64:
		return strconv.FormatInt(s, 10)
	case uint64:
		return strconv.FormatUint(s, 10)
	case float64:
		return strconv.FormatFloat(s, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}
