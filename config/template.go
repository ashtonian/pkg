package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
)

// PrintTemplate writes a config file for T, built from its struct: the file
// an application ships, or an operator starts from. Every key appears in
// declaration order with its default as the value decoding produces — a
// duration as 30s, a list as a list — preceded by its `desc` and a line
// naming its type, whether it is required or sensitive, and the environment
// variable that sets it under envPrefix (none when envPrefix is empty). A key
// with no default, a sensitive key and a default [Field.PrintableDefault]
// withholds are commented out with no value. A map or list with no default
// is commented out with one example entry built from its element's defaults.
// A struct with no key to set is commented out whole, since an empty one
// would load as null over its defaults.
//
// Loading the file gives the same config as loading no source at all, and
// strict keys accept it. [WithFormat] selects FormatYAML (the default) or
// FormatJSON, which has no comments and leaves the commented keys out;
// [WithWriter] applies. Defaults are decoded as Load decodes them without
// [WithDecodeHook]: a default only a custom hook can decode is an
// ErrValidation error.
func PrintTemplate[T any](envPrefix string, opts ...PrintOption) error {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	t := reflect.TypeFor[T]()
	n, err := templateOf(t, strings.TrimSuffix(envPrefix, "_"))
	if err != nil {
		return err
	}
	switch po.formatOr(FormatYAML) {
	case FormatYAML:
		y := &yamlWriter{ew: &errWriter{w: po.writer}}
		y.ew.printf("# Configuration for %s: every key with its default. A commented key has\n# no default, or one that is not shown; uncomment it to set it.\n", t)
		y.mapping(n, 0, false, 0, "")
		return y.ew.err
	case FormatJSON:
		return writeJSON(po.writer, n)
	default:
		return fmt.Errorf("config: unknown template format %q (want %q or %q)", po.format, FormatYAML, FormatJSON)
	}
}

// templateOf builds T's template tree.
func templateOf(t reflect.Type, prefix string) (*outNode, error) {
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	defaults, err := decodedDefaults(t)
	if err != nil {
		return nil, err
	}
	b := &templateBuilder{walking: map[reflect.Type]bool{}}
	n := &outNode{kind: outMap}
	if err := b.branch(n, root, cursor{}, defaults, prefix, false); err != nil {
		return nil, err
	}
	return n, nil
}

// decodedDefaults decodes t's `default` tags into a new t, as the lowest
// source of a load decodes them.
func decodedDefaults(t reflect.Type) (reflect.Value, error) {
	tree, err := confmap.Provider(defaultsFrom(fieldsOf(t)), ".").Read()
	if err != nil {
		return reflect.Value{}, fmt.Errorf("reading the default tags of %s: %w: %w", t, ErrSource, err)
	}
	out := reflect.New(t)
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       (&options{}).decodeHook(nil),
		WeaklyTypedInput: true,
		Result:           out.Interface(),
		TagName:          "koanf",
	})
	if err != nil {
		return reflect.Value{}, err
	}
	if err := dec.Decode(tree); err != nil {
		root, _ := schemaOf(t)
		return reflect.Value{}, fmt.Errorf("decoding the default tags of %s: %w: %w", t, ErrValidation, decodeErrors(root, tree, err))
	}
	return out.Elem(), nil
}

type templateBuilder struct {
	walking map[reflect.Type]bool // the structs on the current path: a recursive type stops
}

// branch adds node's keys to n from val, a node value holding defaults, and
// reports whether any key is set rather than commented out. env is the
// variable prefix for keys here, or empty where no variable reaches; crossed
// reports a map already crossed on the way.
func (b *templateBuilder) branch(n *outNode, node *schemaNode, c cursor, val reflect.Value, env string, crossed bool) error {
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
			fv, _ = val.FieldByIndexErr(f.index)
		}
		fenv := ""
		if env != "" {
			fenv = env + "_" + strings.ToUpper(f.key)
		}
		e := outEntry{key: f.key}
		if desc := f.sf.Tag.Get("desc"); desc != "" {
			e.comments = strings.Split(desc, "\n")
		}
		if f.node.kind == nodeBranch {
			child := &outNode{kind: outMap}
			if err := b.branch(child, f.node, fc, derefValue(fv), fenv, crossed); err != nil {
				return err
			}
			e.node, e.commented = child, !child.live()
		} else {
			e.comments = append(e.comments, meta(f, fc, fenv))
			var err error
			if e.node, e.commented, err = b.leaf(f, fc, fv, fenv, crossed); err != nil {
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
func (b *templateBuilder) leaf(f *schemaField, c cursor, fv reflect.Value, env string, crossed bool) (*outNode, bool, error) {
	printable := !c.sensitive() && !mayHoldSensitive(f.sf.Type, map[reflect.Type]bool{})
	if _, has := f.sf.Tag.Lookup("default"); has && printable {
		w := &valueTree{visiting: map[visit]bool{}}
		return w.value(fv, site{}), false, nil
	}
	if ex, has := f.sf.Tag.Lookup("example"); has && printable {
		v, err := decodeTag(f.sf.Type, f.sf.Tag, ex)
		if err != nil {
			return nil, true, fmt.Errorf("decoding the example of %s: %w: %w", c.path.orRoot(), ErrSchema, err)
		}
		w := &valueTree{visiting: map[visit]bool{}}
		return w.value(v, site{}), true, nil
	}
	n, err := b.example(f.node, c, env, crossed)
	return n, true, err
}

// example lays out one example entry of a map or list, built from its
// element's defaults; any other key has none.
func (b *templateBuilder) example(node *schemaNode, c cursor, env string, crossed bool) (*outNode, error) {
	if node.kind != nodeMap && node.kind != nodeList {
		return nil, nil
	}
	elemEnv := ""
	if node.kind == nodeMap && env != "" && !crossed {
		elemEnv = env + "_<KEY>"
	}
	var elem *outNode
	switch node.elem.kind {
	case nodeBranch:
		defaults, err := decodedDefaults(node.elem.typ)
		if err != nil {
			return nil, err
		}
		elem = &outNode{kind: outMap}
		if err := b.branch(elem, node.elem, c.intoEntry(segment{kind: segAny}, node.typ.Elem()), defaults, elemEnv, true); err != nil {
			return nil, err
		}
	default:
		elem = scalarNode("<" + node.typ.Elem().String() + ">")
	}
	if node.kind == nodeList {
		return &outNode{kind: outList, items: []*outNode{elem}}, nil
	}
	entry := outEntry{key: "<name>", node: elem}
	if elemEnv != "" && node.elem.kind != nodeBranch {
		entry.comments = []string{"env " + elemEnv}
	}
	return &outNode{kind: outMap, entries: []outEntry{entry}}, nil
}

// meta is a key's type line: its Go type, whether it is required or
// sensitive, and the variable that sets it.
func meta(f *schemaField, c cursor, env string) string {
	parts := []string{f.sf.Type.String()}
	if slices.Contains(strings.Split(f.sf.Tag.Get("validate"), ","), "required") {
		parts = append(parts, "required")
	}
	if c.sensitive() {
		parts = append(parts, "sensitive")
	}
	text := strings.Join(parts, ", ")
	var names []string
	if env != "" && (f.node.kind == nodeLeaf || f.node.kind == nodeDynamic || f.node.kind == nodeList && f.node.elem.kind != nodeBranch) {
		names = append(names, env)
	}
	if env != "" {
		names = append(names, envTagNames(f.sf.Tag)...)
	}
	if len(names) > 0 {
		text += "; env " + strings.Join(names, ", ")
	}
	return text
}
