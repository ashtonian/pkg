package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/knadh/koanf/v2"
)

// DefaultProvider declares defaults belonging to a root configuration schema.
// ConfigDefaults runs on a fresh zero *T and must return deterministic data
// without I/O or changing global state. Keys are dot paths, as in WithDefaults;
// values may be nested maps, structs or collections. A declaration cannot
// set both a section and a dot path beneath it. The returned data is copied.
//
// These defaults override tags, below WithDefaults and every input source.
// Load, Usage, JSONSchema and PrintTemplate use them. Only the root's method
// runs; defaults on collection element types remain their default tags.
// Unknown keys and values the built-in decoder cannot decode are ErrSchema
// errors, even when a later source would replace them.
type DefaultProvider interface {
	ConfigDefaults() map[string]any
}

func (l *loader) addDeclaredDefaults(declarations bool) error {
	if tags := defaultsFrom(fieldsOf(l.root)); len(tags) > 0 {
		if err := l.addFlat("struct-tag defaults", tags, constant(Source{Kind: SourceTag})); err != nil {
			return err
		}
	}
	if !declarations {
		return nil
	}
	p, ok := reflect.New(derefType(l.root)).Interface().(DefaultProvider)
	if !ok {
		return nil
	}
	declared := p.ConfigDefaults()
	if len(declared) == 0 {
		return nil
	}
	copy := deepCopy(reflect.ValueOf(declared)).Interface().(map[string]any)
	for key := range copy {
		for end := strings.LastIndexByte(key, '.'); end >= 0; end = strings.LastIndexByte(key[:end], '.') {
			if _, overlap := copy[key[:end]]; overlap {
				return fmt.Errorf("ConfigDefaults: %w: a section and a key beneath it are both set", ErrSchema)
			}
		}
	}
	checked := &loader{k: koanf.New("."), root: l.root, schema: l.schema}
	if err := checked.addFlat("ConfigDefaults", copy, constant(Source{Kind: SourceSchemaDefaults})); err != nil {
		return fmt.Errorf("%w: %w", ErrSchema, err)
	}
	tree := checked.k.Raw()
	if _, err := decodeDefaultTree(l.root, l.schema, tree); err != nil {
		return fmt.Errorf("ConfigDefaults: %w: %w", ErrSchema, err)
	}
	return l.merge("ConfigDefaults", tree, constant(Source{Kind: SourceSchemaDefaults}))
}

type declaredDefaults struct {
	root  *schemaNode
	value reflect.Value
	tree  map[string]any
}

func readDefaults(t reflect.Type, declarations bool) (declaredDefaults, error) {
	t = derefType(t)
	root, err := rootSchema(t)
	if err != nil {
		return declaredDefaults{}, err
	}
	l := &loader{k: koanf.New("."), root: t, schema: root}
	if err := l.addDeclaredDefaults(declarations); err != nil {
		return declaredDefaults{}, err
	}
	tree := l.k.Raw()
	fillElementDefaults(t, tree)
	v, err := decodeDefaultTree(t, root, tree)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return declaredDefaults{root: root, value: v, tree: tree}, err
}

func decodeDefaultTree(t reflect.Type, root *schemaNode, tree map[string]any) (reflect.Value, error) {
	out := reflect.New(derefType(t))
	md, err := (&options{}).decodeInput(root, tree, out.Interface(), nil)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("decoding defaults of %s: %w", typeText(t), err)
	}
	if len(md.Unused) > 0 {
		unknown := &UnknownKeysError{Type: t}
		for _, name := range md.Unused {
			unknown.Keys = append(unknown.Keys, unusedKey(root, tree, name))
		}
		return reflect.Value{}, fmt.Errorf("defaults of %s: %w: %w", typeText(t), ErrSchema, unknown)
	}
	return out.Elem(), nil
}

func (d declaredDefaults) has(path keyPath) bool {
	var value any = d.tree
	for _, s := range path {
		if s.kind != segField && s.kind != segKey {
			return false
		}
		m, ok := value.(map[string]any)
		if !ok {
			v := derefValue(reflect.ValueOf(value))
			return v.IsValid() && v.Kind() == reflect.Struct && d.field(d.root, path).IsValid()
		}
		value, ok = m[s.name]
		if !ok {
			return false
		}
	}
	return true
}

func (d declaredDefaults) field(root *schemaNode, path keyPath) reflect.Value {
	v := d.value
	for _, s := range path {
		v = derefValue(v)
		if !v.IsValid() || s.kind != segField || root.kind != nodeBranch {
			return reflect.Value{}
		}
		f := root.byKey[s.name]
		if f == nil {
			return reflect.Value{}
		}
		v, _ = f.value(v)
		root = f.node
	}
	return v
}

func defaultText(v reflect.Value) (string, bool, error) {
	w := &valueTree{visiting: map[visit]bool{}}
	n := w.value(v, site{})
	if w.err != nil {
		return "", false, w.err
	}
	if n.hasRedaction() {
		return "", true, nil
	}
	if n.kind == outScalar {
		return scalarText(n.scalar), false, nil
	}
	b, err := json.Marshal(n.plain())
	return string(b), false, err
}
