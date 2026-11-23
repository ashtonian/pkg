package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// An edit is one concrete key set to a value — a CheckKey, or an entry a
// provider's entry filter checks — applied as a source above a tree: the
// defaults. Its value merges as a source's would (see mergeTrees): a nested
// map key by key, a typed map, section or anything else in place of what
// the tree holds. A list index edits the element the tree holds there, or
// appends one immediately after its last; the list, with that element
// changed, then replaces the tree's.

// edit is one key and its value, resolved against a schema.
type edit struct {
	path keyPath // canonical: each field segment spelled as its field's key
	// parent is the node holding path's last segment: the struct, map or
	// list it selects from, or what the key reaches beneath.
	parent *schemaNode
	value  any
	safe   string // path as diagnostics may show it
}

// resolveEdit parses key, spells each field segment as its field's key and
// copies value, so the edit owns it. A key that does not parse, is not
// concrete or names no field is an unknown *KeyError.
func resolveEdit(schema *schemaNode, key string, value any) (edit, error) {
	p, err := parseKeyPath(key)
	if err != nil {
		return edit{}, &KeyError{Key: RedactText(key), Unknown: true, Err: err}
	}
	if len(p) == 0 {
		return edit{}, &KeyError{Key: "$", Unknown: true, Err: fmt.Errorf("%w: a key names one value, not the whole config", ErrKey)}
	}
	n := schema
	var parent *schemaNode
	for i := range p {
		if p[i].kind == segAny {
			return edit{}, &KeyError{Key: diagnosticPath(schema, p).String(), Unknown: true, Err: fmt.Errorf("%w: key must be concrete", ErrKey)}
		}
		if n.kind == nodeBranch && p[i].kind == segField {
			if f, _ := n.lookup(p[i].name); f != nil {
				p[i].name = f.key
			}
		}
		parent = n
		_, next, err := n.walkTo(cursor{}, p[i:i+1])
		if err != nil {
			return edit{}, &KeyError{Key: diagnosticPath(schema, p).String(), Unknown: true, Err: err}
		}
		n = next
	}
	return edit{path: p, parent: parent, value: copyInput(value), safe: diagnosticPath(schema, p).String()}, nil
}

func copyInput(v any) any {
	if v == nil {
		return nil
	}
	return deepcopy.Value(reflect.ValueOf(v)).Interface()
}

// rewrite gives e's value the shape a source's value has at e's key: keys
// spelled as their fields are, sep lists split, JSON parsed, typed maps and
// sections whole (see keyWalk). The value is rewritten where it sits, inside
// its parent, so its field's tags apply.
func (e *edit) rewrite(w *keyWalk) error {
	last := e.path[len(e.path)-1]
	at := e.path[:len(e.path)-1]
	value, _ := stringKeysIn(e.value)
	switch e.parent.kind {
	case nodeBranch:
		out := w.structMap(e.parent.typ, reflect.ValueOf(map[string]any{last.name: value}), at)
		if m, ok := out.(map[string]any); ok {
			value = m[last.name]
		}
	case nodeMap, nodeList:
		value = w.value(e.parent.typ.Elem(), value, e.path)
	case nodeDynamic:
		value = dynamicInput(value, e.path, w.failText)
	}
	e.value = value
	switch {
	case len(w.twice) > 0:
		return fmt.Errorf("%w: keys spelled two ways set one field: %s", ErrValidation, strings.Join(w.twice, ", "))
	case len(w.notJSON) > 0:
		return fmt.Errorf("%w: format:\"json\" values that are not JSON: %s", ErrValidation, strings.Join(w.notJSON, ", "))
	case len(w.noText) > 0:
		return fmt.Errorf("%w: values that do not marshal to text: %s", ErrValidation, strings.Join(w.noText, ", "))
	}
	return nil
}

// editTree is the source tree that sets edits above below: nested maps
// along their keys, and at a list index the list below holds there, with
// the element edited. below is called once, and only when an edit indexes a
// list; what it returns is only read. Each failure is a *KeyError.
func editTree(schema *schemaNode, below func() map[string]any, edits []edit) (map[string]any, error) {
	var root map[string]any
	beneath := func() any {
		if root == nil {
			root = below()
		}
		return root
	}
	tree := map[string]any{}
	for i := range edits {
		e := &edits[i]
		if err := e.rewrite(&keyWalk{schema: schema}); err != nil {
			return nil, &KeyError{Key: e.safe, Err: err}
		}
		out, err := e.set(tree, beneath, 0)
		if err != nil {
			return nil, &KeyError{Key: e.safe, Err: err}
		}
		tree = out.(map[string]any)
	}
	return tree, nil
}

// set returns tree, the edit tree at e.path[:i], with e set beneath it;
// beneath reads what the tree below holds there.
func (e *edit) set(tree any, beneath func() any, i int) (any, error) {
	if i == len(e.path) {
		return e.value, nil
	}
	s := e.path[i]
	if s.kind == segIndex {
		list, ok := tree.([]any)
		if !ok {
			list = elementsOf(beneath())
		}
		switch {
		case s.index > len(list):
			return nil, fmt.Errorf("%w: list index is beyond the next element", ErrValidation)
		case s.index == len(list):
			list = append(list, nil)
		}
		elem := list[s.index]
		sub, err := e.set(nil, func() any { return elem }, i+1)
		if err != nil {
			return nil, err
		}
		list[s.index] = mergeValue(copyInput(elem), sub)
		return list, nil
	}
	m, ok := tree.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	child, err := e.set(m[s.name], func() any { return entryOf(beneath(), s.name) }, i+1)
	if err != nil {
		return nil, err
	}
	m[s.name] = child
	return m, nil
}

// entryOf is what v holds under key, when v is a map keyed by strings.
func entryOf(v any, key string) any {
	m := reflect.ValueOf(v)
	if !m.IsValid() || m.Kind() != reflect.Map || m.Type().Key().Kind() != reflect.String {
		return nil
	}
	e := m.MapIndex(reflect.ValueOf(key).Convert(m.Type().Key()))
	if !e.IsValid() {
		return nil
	}
	return e.Interface()
}

// elementsOf is a fresh list of v's elements, when v is a list.
func elementsOf(v any) []any {
	l := reflect.ValueOf(v)
	if !l.IsValid() || !isList(l) {
		return nil
	}
	out := make([]any, l.Len())
	for i := range out {
		out[i] = l.Index(i).Interface()
	}
	return out
}
