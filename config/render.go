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

// valueTree shares key and scalar projection across raw and redacted output.
// Sensitive containers stay whole so policies can also withhold their keys.
type valueTree struct {
	raw      bool
	leaf     func(string, Field, any) any
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
	w.fields(n, v, cursor{})
	return n
}

// fields adds struct val's keys to mapping n, squashed structs' in place.
func (w *valueTree) fields(n *outNode, val reflect.Value, c cursor) {
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
		next := c.intoField(f)
		n.entries = append(n.entries, outEntry{key: f.key, node: w.atValue(fv, next)})
	}
}

func (w *valueTree) value(v reflect.Value, s site) *outNode {
	c := cursor{tagged: s.sensitive, covered: s.named, owner: Field{Type: v.Type()}}
	return w.atValue(v, c)
}

func (w *valueTree) atValue(v reflect.Value, c cursor) *outNode {
	elem := v
	interfaceValue := false
	for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Interface {
		interfaceValue = interfaceValue || elem.Kind() == reflect.Interface
		if elem.IsNil() {
			return w.project(c, &outNode{kind: outNull})
		}
		if elem.Kind() == reflect.Pointer {
			if !w.enter(elem) {
				return w.project(c, scalarNode(cycleValue))
			}
			defer delete(w.visiting, visitOf(elem))
		}
		elem = elem.Elem()
	}
	c.dynamic = c.dynamic || interfaceValue
	sensitive := c.sensitive()
	whole := expandable(elem.Type()) || interfaceValue
	if sensitive && whole && w.leaf != nil {
		inner := *w
		inner.raw = true
		inner.leaf = nil
		// Pointer guards are already held; descend from the dereferenced value.
		n := inner.atValue(elem, c)
		w.err = inner.err
		return w.project(c, n)
	}
	switch {
	case isBranch(elem.Type()):
		if !w.raw && sensitive && interfaceValue {
			return redactedNode()
		}
		n := &outNode{kind: outMap}
		w.fields(n, elem, c)
		return n
	case expandable(elem.Type()):
		if (elem.Kind() == reflect.Map || elem.Kind() == reflect.Slice) && elem.IsNil() {
			return w.project(c, &outNode{kind: outNull})
		}
		if !w.raw && sensitive {
			return redactedNode()
		}
		if elem.Kind() != reflect.Array {
			if !w.enter(elem) {
				return w.project(c, scalarNode(cycleValue))
			}
			defer delete(w.visiting, visitOf(elem))
		}
		if elem.Kind() == reflect.Map {
			n := &outNode{kind: outMap}
			keys := elem.MapKeys()
			slices.SortFunc(keys, func(a, b reflect.Value) int { return cmp.Compare(a.String(), b.String()) })
			for _, k := range keys {
				n.entries = append(n.entries, outEntry{key: k.String(), node: w.atValue(elem.MapIndex(k), c.intoEntry(segment{kind: segKey, name: k.String()}, elem.Type().Elem()))})
			}
			if len(n.entries) == 0 {
				return w.project(c, n)
			}
			return n
		}
		n := &outNode{kind: outList}
		for i := range elem.Len() {
			n.items = append(n.items, w.atValue(elem.Index(i), c.intoEntry(segment{kind: segIndex, index: i}, elem.Type().Elem())))
		}
		if len(n.items) == 0 {
			return w.project(c, n)
		}
		return n
	}
	if uncheckedHolder(elem) {
		c.owner.Sensitive = true
	}
	if !w.raw && c.sensitive() {
		return redactedNode()
	}
	value, err := scalarOf(elem)
	if err != nil {
		w.err = fmt.Errorf("config: projecting %s: %w", c.path.orRoot(), err)
		return &outNode{kind: outNull}
	}
	return w.project(c, scalarNode(value))
}

func (w *valueTree) project(c cursor, n *outNode) *outNode {
	if w.leaf == nil {
		return n
	}
	field := cloneField(c.owner)
	field.Sensitive = c.sensitive()
	return scalarNode(w.leaf(c.path.String(), field, n.plain()))
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

// scalarOf uses the decoder's scalar forms; custom decoders must accept their
// text marshaler's representation.
func scalarOf(v reflect.Value) (any, error) {
	if v.Type() == durationType {
		return time.Duration(v.Int()).String(), nil
	}
	if m, ok := textMarshaler(v); ok {
		b, err := m.MarshalText()
		return string(b), err
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return nil, nil
			}
			return string(v.Bytes()), nil
		}
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			values := make([]any, v.Len())
			for i := range v.Len() {
				values[i] = v.Index(i).Uint()
			}
			return values, nil
		}
	}
	return nil, fmt.Errorf("%w: %s has no scalar representation; implement encoding.TextMarshaler", ErrSchema, typeText(v.Type()))
}

func textMarshaler(v reflect.Value) (encoding.TextMarshaler, bool) {
	if m, ok := v.Interface().(encoding.TextMarshaler); ok {
		return m, true
	}
	if reflect.PointerTo(v.Type()).Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		// Map values are not addressable; use a copy for pointer-receiver methods.
		p := reflect.New(v.Type())
		p.Elem().Set(deepCopy(v))
		return p.Interface().(encoding.TextMarshaler), true
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
// supported finite values back gives the same config, apart from redaction;
// [Tree] describes the scalar and cycle limits. It returns nil on error;
// use [RedactedTreeChecked] to receive the error.
func RedactedTree(cfg any) map[string]any {
	result, _ := RedactedTreeChecked(cfg)
	return result
}

// RedactedTreeChecked returns [RedactedTree]'s values, or nil and an error
// when a reached type has invalid keys or no scalar representation, or its
// text marshaler fails. No partial tree is returned.
// It does not decode default or example tags.
func RedactedTreeChecked(cfg any) (map[string]any, error) {
	return (&valueTree{visiting: map[visit]bool{}}).tree(cfg)
}

// Tree returns cfg as nested maps, lists and decoder-compatible scalars,
// including sensitive values. The result is detached from cfg. Cycles are
// represented as [CYCLE], as in RedactedTree, and cannot round-trip. Opaque
// types without a scalar/text representation return ErrSchema; MarshalText
// errors are returned with the key. Custom decoders must accept that text.
func Tree(cfg any) (map[string]any, error) { return TreeFunc(cfg, nil) }

// TreeFunc projects cfg into a tree, replacing each leaf with leaf's result.
// The key uses the package's grammar; Field carries the declared metadata.
// Sensitive maps, lists and interfaces are passed once, whole, including
// their keys. Empty or nil containers are leaves. A nil callback is identity.
// The callback receives detached tree values and may change them freely.
func TreeFunc(cfg any, leaf func(key string, f Field, v any) any) (map[string]any, error) {
	w := &valueTree{visiting: map[visit]bool{}, raw: true, leaf: leaf}
	return w.tree(cfg)
}

func (w *valueTree) tree(cfg any) (map[string]any, error) {
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
