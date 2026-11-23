package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// A key edit is one concrete key set to a value — CheckKey's key, each of
// DecodeEdits' edits, each entry a provider's entry filter checks — applied
// as a source above a tree: the defaults, or a baseline config. Its value
// merges as a source's would (see mergeTrees): a map into what the tree
// holds there key by key, anything else in its place. An index sets that
// element of the list beneath, or appends one immediately after its last,
// and leaves the other elements as they are (listEdit).

// edit is one key and its value, resolved against a schema.
type edit struct {
	path keyPath // canonical: each field segment spelled as its field's key
	// parent is the node holding path's last segment: the struct, map or
	// list it selects from, or what the key reaches beneath. It is nil
	// beneath a remain field, where nothing is declared.
	parent *schemaNode
	value  any
	safe   string // path as diagnostics may show it
}

// resolveEdit parses key, spells each field segment as its field's key and
// copies value, so the edit owns it. A key that does not parse, is not
// concrete or names nothing config reads is an Unknown *KeyError. A key a
// section's remain field collects resolves: what is beneath it is the
// remain field's.
func resolveEdit(schema *schemaNode, key string, value any) (edit, *KeyError) {
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
		s := &p[i]
		if s.kind == segAny {
			return edit{}, &KeyError{Key: diagnosticPath(schema, p).String(), Unknown: true, Err: fmt.Errorf("%w: key must be concrete", ErrKey)}
		}
		parent = n
		if n == nil {
			continue
		}
		if n.kind == nodeBranch {
			var f *schemaField
			if s.kind == segField {
				f, _ = n.lookup(s.name)
			}
			if f == nil && n.acceptsUnknown {
				n = nil
				continue
			}
			if f != nil {
				s.name = f.key
			}
		}
		_, next, err := n.walkTo(cursor{}, p[i:i+1])
		if err != nil {
			return edit{}, &KeyError{Key: diagnosticPath(schema, p).String(), Unknown: true, Err: err}
		}
		n = next
	}
	return edit{path: p, parent: parent, value: copyInput(value), safe: diagnosticPath(schema, p).String()}, nil
}

// keyProblem is why a key cannot be set as written, in words naming keys
// and never a value, so a KeyError's message can carry it.
type keyProblem string

func (p keyProblem) Error() string { return string(p) }

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %w", ErrValidation, keyProblem(strings.TrimSpace(fmt.Sprintf(format, args...))))
}

func copyInput(v any) any {
	if v == nil {
		return nil
	}
	return deepcopy.Value(reflect.ValueOf(v)).Interface()
}

// resolveEdits resolves every entry of entries and orders the edits as
// they apply: by canonical key, an index by its number, and a key before the
// keys beneath it, so a list grows one element at a time and a key beneath
// another sets what it names in the value the other set. failed, sorted by
// key, holds every key resolveEdit refuses, and every key two entries set
// under different spellings: neither can be the one meant.
func resolveEdits(schema *schemaNode, entries map[string]any) (edits []edit, failed []*KeyError) {
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		e, err := resolveEdit(schema, key, entries[key])
		if err != nil {
			failed = append(failed, err)
			continue
		}
		edits = append(edits, e)
	}
	slices.SortStableFunc(edits, func(a, b edit) int { return comparePaths(a.path, b.path) })
	kept := edits[:0]
	for i := 0; i < len(edits); {
		same := i + 1
		for same < len(edits) && comparePaths(edits[i].path, edits[same].path) == 0 {
			same++
		}
		if same > i+1 {
			failed = append(failed, &KeyError{Key: edits[i].safe, Err: refuse("the key is set more than once, under different spellings")})
		} else {
			kept = append(kept, edits[i])
		}
		i = same
	}
	return kept, sortKeyErrors(failed)
}

// comparePaths orders keys segment by segment — a field and a map key
// alike by name, an index by its number — and a key before the keys
// beneath it.
func comparePaths(a, b keyPath) int {
	for i := range min(len(a), len(b)) {
		x, y := a[i], b[i]
		if xi, yi := x.kind == segIndex, y.kind == segIndex; xi != yi {
			if xi {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(x.index, y.index); c != 0 {
			return c
		}
		if c := strings.Compare(x.name, y.name); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func sortKeyErrors(errs []*KeyError) []*KeyError {
	slices.SortStableFunc(errs, func(a, b *KeyError) int { return strings.Compare(a.Key, b.Key) })
	return errs
}

// joinKeyErrors is errs, sorted by key, as one error: the one, or all joined.
func joinKeyErrors(errs []*KeyError) error {
	sortKeyErrors(errs)
	if len(errs) == 1 {
		return errs[0]
	}
	joined := make([]error, len(errs))
	for i, e := range errs {
		joined[i] = e
	}
	return errors.Join(joined...)
}

// rewrite gives e's value the shape a source's value has at e's key: keys
// spelled as their fields are, sep lists split, JSON parsed, typed maps and
// sections whole (see keyWalk). The value is rewritten where it sits, inside
// its parent, so its field's tags apply.
func (e *edit) rewrite(w *keyWalk) error {
	last := e.path[len(e.path)-1]
	at := e.path[:len(e.path)-1]
	value, _ := stringKeysIn(e.value)
	if e.parent != nil {
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
	}
	e.value = value
	switch {
	case len(w.twice) > 0:
		return refuse("keys spelled two ways set one field: %s", strings.Join(w.twice, ", "))
	case len(w.notJSON) > 0:
		return refuse("format:\"json\" values that are not JSON: %s", strings.Join(w.notJSON, ", "))
	case len(w.noText) > 0:
		return refuse("values that do not marshal to text: %s", strings.Join(w.noText, ", "))
	}
	return nil
}

// editTree is the source tree that sets edits, in order, over below, the
// tree beneath them: nested maps along their keys, and a listEdit at each
// index. It reads below only for the length of a list an edit indexes, and
// calls it at most once. An edit whose value or index it refuses is left out,
// in failed.
func editTree(schema *schemaNode, below func() map[string]any, edits []edit) (tree map[string]any, failed []*KeyError) {
	var root map[string]any
	beneath := func() any {
		if root == nil {
			root = below()
		}
		return root
	}
	tree = map[string]any{}
	for i := range edits {
		e := &edits[i]
		if err := e.rewrite(&keyWalk{schema: schema}); err != nil {
			failed = append(failed, &KeyError{Key: e.safe, Err: err})
			continue
		}
		out, err := e.set(tree, beneath, 0)
		if err != nil {
			failed = append(failed, &KeyError{Key: e.safe, Err: err})
			continue
		}
		tree = out.(map[string]any)
	}
	return tree, failed
}

// set returns tree, the edit tree at e.path[:i], with e set beneath it.
// tree is nil, what edits beneath this key built — a map, a listEdit — or
// the value an edit of a key holding e's set here, which e then edits in
// place. beneath reads what the tree below holds at e.path[:i]. On failure
// tree is as it was.
func (e *edit) set(tree any, beneath func() any, i int) (any, error) {
	if i == len(e.path) {
		return within(tree, e.value), nil
	}
	s := e.path[i]
	if s.kind == segIndex {
		return e.setElement(tree, beneath, i)
	}
	var m map[string]any
	whole := true // what is here replaces what lies below, so nothing below is read
	switch t := tree.(type) {
	case nil:
		m, whole = map[string]any{}, false
	case map[string]any:
		m, whole = t, false
	case wholeMap:
		m = t
	default:
		m = map[string]any{} // a key beneath a value that holds no keys replaces it
	}
	below := func() any { return entryOf(beneath(), s.name) }
	if whole {
		below = func() any { return nil }
	}
	child, err := e.set(m[s.name], below, i+1)
	if err != nil {
		return nil, err
	}
	m[s.name] = child
	if whole {
		return wholeMap(m), nil
	}
	return m, nil
}

// setElement is set at an index: a listEdit over the list below, or, where
// an earlier edit set a value here, that value as a list with one element
// set.
func (e *edit) setElement(tree any, beneath func() any, i int) (any, error) {
	s := e.path[i]
	edits, editing := tree.(listEdit)
	if tree != nil && !editing {
		list := elementsOf(tree)
		if s.index > len(list) {
			return nil, refuse("the list index is beyond the next element")
		}
		if s.index == len(list) {
			list = append(list, nil)
		}
		elem, err := e.set(list[s.index], func() any { return nil }, i+1)
		if err != nil {
			return nil, err
		}
		list[s.index] = elem
		return list, nil
	}
	size := inputLen(beneath())
	next := size // edits apply in index order, so those appending follow one another
	for k := range edits {
		if k >= size {
			next++
		}
	}
	if s.index > next {
		return nil, refuse("the list index is beyond the next element")
	}
	elem, err := e.set(edits[s.index], func() any { return inputElem(beneath(), s.index) }, i+1)
	if err != nil {
		return nil, err
	}
	if edits == nil {
		edits = listEdit{}
	}
	edits[s.index] = elem
	return edits, nil
}

// within is value set where the edit tree holds tree: a map merged into a
// map there key by key, keeping its kind, anything else in its place.
func within(tree, value any) any {
	in, ok := value.(map[string]any)
	if !ok {
		return value
	}
	var into map[string]any
	switch t := tree.(type) {
	case map[string]any:
		into = t
	case wholeMap:
		into = t
	default:
		return value
	}
	for k, v := range in {
		into[k] = within(into[k], v)
	}
	return tree
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

// inputElem is v's element at index i, when v is a list that long.
func inputElem(v any, i int) any {
	l := reflect.ValueOf(v)
	if !l.IsValid() || !isList(l) || i >= l.Len() {
		return nil
	}
	return l.Index(i).Interface()
}

// inputLen is v's length, when v is a list.
func inputLen(v any) int {
	l := reflect.ValueOf(v)
	if !l.IsValid() || !isList(l) {
		return 0
	}
	return l.Len()
}

// elementsOf is a fresh list of v's elements, when v is a list.
func elementsOf(v any) []any {
	out := make([]any, inputLen(v))
	l := reflect.ValueOf(v)
	for i := range out {
		out[i] = l.Index(i).Interface()
	}
	return out
}
