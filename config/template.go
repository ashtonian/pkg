package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// PrintTemplate writes a config file for T, built from its struct: the file an
// application ships, or an operator starts from. Every key appears in
// declaration order with its default as the value decoding produces — a
// duration as 30s, a list as a list — preceded by its `desc` and a line naming
// its type, whether it is required or sensitive, and the environment
// variables that set it under envPrefix and the [WithFallbackEnvPrefixes]
// prefixes, in precedence order (none when there is no prefix). A key with no
// default is commented out with its printable example, if any; a sensitive key
// and a default [Field.PrintableDefault] withholds have no value. A map or
// list with no default is commented out with one example entry built from its
// element's defaults. A struct with no key to set is commented out whole,
// since an empty one would load as null over its defaults.
//
// Loading the file gives the same config as loading no source at all, and
// strict keys accept it. [WithFormat] selects FormatYAML (the default) or
// FormatJSON, which has no comments and leaves the commented keys out;
// [WithWriter] and WithFallbackEnvPrefixes apply. Tag and [Defaulter]
// defaults are decoded as Load decodes them without [WithDecodeHook]: a
// default only a custom hook can decode is an ErrValidation error.
func PrintTemplate[T any](envPrefix string, opts ...PrintOption) error {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	t := reflect.TypeFor[T]()
	n, err := templateOf(t, po.envPrefixes(envPrefix))
	if err != nil {
		return err
	}
	switch po.formatOr(FormatYAML) {
	case FormatYAML:
		y := &yamlWriter{ew: &errWriter{w: po.writer}}
		y.ew.printf("# Configuration for %s: every key with its default. A commented key has\n# no default, or one that is not shown; uncomment it to set it.\n", typeText(t))
		y.mapping(n, 0, false, 0, "")
		return y.ew.err
	case FormatJSON:
		return writeJSON(po.writer, n)
	default:
		return fmt.Errorf("config: unknown template format %q (want %q or %q)", po.format, FormatYAML, FormatJSON)
	}
}

// templateOf builds T's template tree, naming variables under prefixes, an
// envPrefixList.
func templateOf(t reflect.Type, prefixes []string) (*outNode, error) {
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	defaults, err := readDefaults(t)
	if err != nil {
		return nil, err
	}
	b := &templateBuilder{defaults: defaults, walking: map[reflect.Type]bool{}}
	n := &outNode{kind: outMap}
	if err := b.branch(n, root, cursor{}, defaults.value, rootEnvNames(prefixes)); err != nil {
		return nil, err
	}
	return n, nil
}

func decodedDefaults(t reflect.Type) (reflect.Value, error) {
	d, err := readDefaults(t)
	return d.value, err
}

type templateBuilder struct {
	defaults declaredDefaults
	walking  map[reflect.Type]bool // the structs on the current path: a recursive type stops
}

// branch adds node's keys to n from val, a node value holding defaults; env
// names the variables that address node.
func (b *templateBuilder) branch(n *outNode, node *schemaNode, c cursor, val reflect.Value, env envNames) error {
	if b.walking[node.typ] {
		return nil
	}
	if val.IsValid() && val.Kind() != reflect.Struct {
		val = reflect.Value{} // a nil pointer: no defaults beneath it
	}
	b.walking[node.typ] = true
	defer delete(b.walking, node.typ)
	for _, f := range node.fields {
		fc := c.intoField(f)
		fv := reflect.Value{}
		if val.IsValid() {
			fv, _ = f.value(val)
		}
		fenv := env.field(f.key)
		e := outEntry{key: f.key}
		if desc := f.sf.Tag.Get("desc"); desc != "" {
			e.comments = strings.Split(desc, "\n")
		}
		if f.node.kind == nodeBranch && b.defaults.has(fc.path) && derefValue(fv).Kind() != reflect.Struct {
			e.node = &outNode{kind: outNull}
			n.entries = append(n.entries, e)
			continue
		}
		if f.node.kind == nodeBranch {
			if jsonFormat(f.sf.Tag) {
				e.comments = append(e.comments, meta(f, fc, fenv))
			}
			child := &outNode{kind: outMap}
			if err := b.branch(child, f.node, fc, derefValue(fv), fenv); err != nil {
				return err
			}
			e.node, e.commented = child, !child.live()
		} else {
			e.comments = append(e.comments, meta(f, fc, fenv))
			var err error
			if e.node, e.commented, err = b.leaf(f, fc, fv, fenv); err != nil {
				return err
			}
		}
		n.entries = append(n.entries, e)
	}
	return nil
}

// live reports a mapping with at least one entry not commented out.
func (n *outNode) live() bool {
	return slices.ContainsFunc(n.entries, func(e outEntry) bool { return !e.commented })
}

// leaf lays out a key that is not a struct: its default, or a commented-out
// placeholder or example.
func (b *templateBuilder) leaf(f *schemaField, c cursor, fv reflect.Value, env envNames) (*outNode, bool, error) {
	printable := !c.sensitive() && !mayHoldSensitive(f.sf.Type, map[reflect.Type]bool{})
	if _, has := f.sf.Tag.Lookup("default"); (has || b.defaults.has(c.path)) && printable {
		w := &valueTree{visiting: map[visit]bool{}}
		n := w.value(fv, site{})
		if n.hasRedaction() {
			return nil, true, w.err
		}
		return n, false, w.err
	}
	if ex, has := f.sf.Tag.Lookup("example"); has && printable {
		v, err := decodeTag(f.sf.Type, f.sf.Tag, ex)
		if err != nil {
			return nil, true, fmt.Errorf("decoding the example of %s: %w: %w", c.path.orRoot(), ErrSchema, err)
		}
		w := &valueTree{visiting: map[visit]bool{}}
		n := w.value(v, site{})
		return n, true, w.err
	}
	n, err := b.example(f.node, c, env)
	return n, true, err
}

// example lays out one example entry of a map or list, built from its
// element's defaults; any other key has none.
func (b *templateBuilder) example(node *schemaNode, c cursor, env envNames) (*outNode, error) {
	if node.kind != nodeMap && node.kind != nodeList {
		return nil, nil
	}
	var elemEnv envNames // a list element has no variable
	if node.kind == nodeMap {
		elemEnv = env.entries(node.typ)
	}
	var elem *outNode
	switch node.elem.kind {
	case nodeBranch:
		defaults, err := decodedDefaults(node.elem.typ)
		if err != nil {
			return nil, err
		}
		elem = &outNode{kind: outMap}
		if err := b.branch(elem, node.elem, c.intoEntry(segment{kind: segAny}, node.typ.Elem()), defaults, elemEnv); err != nil {
			return nil, err
		}
	default:
		elem = scalarNode("<" + typeText(node.typ.Elem()) + ">")
	}
	if node.kind == nodeList {
		return &outNode{kind: outList, items: []*outNode{elem}}, nil
	}
	entry := outEntry{key: "<name>", node: elem}
	if names := elemEnv.set("", node.typ.Elem()); len(names) > 0 && node.elem.kind != nodeBranch {
		entry.comments = []string{"env " + strings.Join(names, ", ")}
	}
	return &outNode{kind: outMap, entries: []outEntry{entry}}, nil
}

// meta is a key's type line: its Go type, whether it is required or
// sensitive, and the variables that set it.
func meta(f *schemaField, c cursor, env envNames) string {
	parts := []string{typeText(f.sf.Type)}
	if slices.Contains(strings.Split(f.sf.Tag.Get("validate"), ","), "required") {
		parts = append(parts, "required")
	}
	if c.sensitive() {
		parts = append(parts, "sensitive")
	}
	text := strings.Join(parts, ", ")
	if names := env.set(f.sf.Tag, f.sf.Type); len(names) > 0 {
		text += "; env " + strings.Join(names, ", ")
		if jsonFormat(f.sf.Tag) {
			text += " (JSON)"
		}
	}
	return text
}
