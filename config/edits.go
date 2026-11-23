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
// DecodeEdits' edits, each stored entry a load reads, each entry
// WithInvalidEntries judges — applied as a source above a tree: the
// defaults, a baseline config, or the layers merged beneath. Its value
// merges as a source's would (see mergeTrees): a map into what the tree
// holds there key by key, anything else in its place. An index sets that
// element of the list beneath, or appends one immediately after its last,
// and leaves the other elements as they are (listEdit).
//
// A batch is prepared (prepareEdits) and then set (setEdits): resolved,
// rewritten and checked against itself first, then set over the tree
// beneath, which only an index needs to read.

// edit is one key and its value, resolved against a schema.
type edit struct {
	path keyPath // canonical (see resolveSchemaPath)
	// parent is the node holding path's last segment: the struct, map or
	// list it selects from, or what the key reaches beneath — beneath a key
	// a remain field collects, the node of the field's element type.
	parent  *schemaNode
	value   any
	safe    string       // path as diagnostics may show it
	aliased []string     // each field the key names by a former name, as a load reports it
	unknown []UnknownKey // keys inside value that no field reads, found as it is rewritten
	// unkeyed is the koanf:"-" fields of the typed sections value sets,
	// found as it is rewritten (see unkeyed.go).
	unkeyed []unkeyedFields
}

// resolveKey parses key and spells it canonically (see resolveSchemaPath).
func resolveKey(schema *schemaNode, key string) (keyPath, *schemaNode, []string, *KeyError) {
	p, err := parseKeyPath(key)
	if err != nil {
		return nil, nil, nil, unparsedKeyError(key, err)
	}
	parent, aliased, ke := resolveSchemaPath(schema, p)
	return p, parent, aliased, ke
}

// resolveSchemaPath spells p, in place, the one way this package prints it: each
// field segment as its field's key, a map entry by the text of the key it
// decodes to (see keyCodec), and a key beneath an interface, or one a
// remain field collects, as a field wherever it can be one. It returns the
// node holding p's last segment and each field p names by a former name. A
// key that is empty, is not concrete or names nothing config reads is an
// Unknown *KeyError; one a section's remain field collects resolves as
// walkTo resolves it (see schemaNode.collects).
func resolveSchemaPath(schema *schemaNode, p keyPath) (parent *schemaNode, aliased []string, ke *KeyError) {
	if len(p) == 0 {
		return nil, nil, newKeyError(schema, p, true, notRead(p, "a key names one value, not the whole config"))
	}
	unknown := func(err error) *KeyError { return newKeyError(schema, p, true, err) }
	n := schema
	for i := range p {
		s := &p[i]
		if s.kind == segAny {
			return nil, nil, unknown(notConcrete(p))
		}
		parent = n
		former, err := spellSegment(n, p, i)
		if err != nil {
			return nil, nil, unknown(err)
		}
		if former != "" {
			aliased = append(aliased, aliasNote(schema, p[:i].field(former), p[:i+1]))
		}
		// The cursor starts at p[:i], so a fault names where it is.
		_, next, err := n.walkTo(cursor{path: slices.Clip(p[:i])}, p[i:i+1])
		if err != nil {
			if f, ok := err.(*keyFault); ok && n.kind == nodeBranch && s.kind == segField {
				if closest := closestField(n, s.name); closest != "" {
					f.suggestion = p[:i].field(closest)
				}
			}
			return nil, nil, unknown(err)
		}
		n = next
	}
	return parent, aliased, nil
}

// spellSegment spells p[i], a segment of a key at n, in place, the one way
// this package prints it: a field by its key, not a former name or another
// letter case; a key n's remain field collects, or one beneath an
// interface, as a field wherever it can be one; and a map entry by the text
// of the key it decodes to (see keyCodec), which must read back to that key.
// [*] and an index are left as written. former is the former name a field
// was named by, or "". resolveSchemaPath and applierPrefix spell every
// segment through it, so a key and an applier's prefix are spelled alike.
func spellSegment(n *schemaNode, p keyPath, i int) (former string, err error) {
	s := &p[i]
	switch {
	case n.kind == nodeDynamic && s.kind == segKey, n.kind == nodeBranch && n.collects(*s):
		*s = memberSegment(s.name)
	case n.kind == nodeBranch && s.kind == segField:
		// A field spelled as a map key is left as written: walkTo refuses
		// it.
		if f, alias := n.lookup(s.name); f != nil {
			written := s.name
			s.name = f.key
			if alias {
				return written, nil
			}
		}
	case n.kind == nodeMap && s.kind == segKey:
		text, err := n.keys.canonical(s.name)
		if err != nil {
			return "", notRead(p, "%s is keyed by %s, which the entry's key does not decode into", p[:i], typeText{n.typ.Key()})
		}
		s.name = text
	}
	return "", nil
}

// aliasNote is how a load reports a key written under a former field name:
// the key as written and as it is now, or only the part diagnostics may show.
func aliasNote(schema *schemaNode, written, now keyPath) string {
	if safe := diagnosticPath(schema, now); len(safe) < len(now) {
		return safe.String() + " (sensitive contents)"
	}
	return fmt.Sprintf("%s (now %s)", written, now)
}

func copyInput(v any) any {
	if v == nil {
		return nil
	}
	return deepcopy.Value(reflect.ValueOf(v)).Interface()
}

// prepareEdits resolves, rewrites and checks every entry of entries and
// orders the edits as they apply: by canonical key, an index by its number,
// and a key before the keys beneath it, so a list grows one element at a
// time and a key beneath another sets what it names in the value the other
// set. refused, sorted by key, holds every key that cannot apply: one
// resolveEdit refuses, one two entries set under different spellings —
// neither can be the one meant — one whose value does not rewrite, and one
// the batch leaves nothing to set (see unsettable). Each edit owns its
// value.
func prepareEdits(schema *schemaNode, entries map[string]any) (edits []edit, refused []*KeyError) {
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		p, parent, aliased, ke := resolveKey(schema, key)
		if ke != nil {
			refused = append(refused, ke)
			continue
		}
		edits = append(edits, edit{path: p, parent: parent, value: copyInput(entries[key]), safe: shownKey(schema, p), aliased: aliased})
	}
	slices.SortStableFunc(edits, func(a, b edit) int { return comparePaths(a.path, b.path) })
	kept := edits[:0]
	for i := 0; i < len(edits); {
		same := i + 1
		for same < len(edits) && comparePaths(edits[i].path, edits[same].path) == 0 {
			same++
		}
		if same > i+1 {
			refused = append(refused, newKeyError(schema, edits[i].path, false, refuse("the key is set more than once, under different spellings")))
		} else {
			kept = append(kept, edits[i])
		}
		i = same
	}
	edits, failed := rewriteEdits(schema, kept)
	edits, unset := unsettable(schema, edits)
	return edits, sortKeyErrors(slices.Concat(refused, failed, unset))
}

// rewriteEdits rewrites each edit's value (see rewrite), refusing those that
// do not rewrite.
func rewriteEdits(schema *schemaNode, edits []edit) (kept []edit, refused []*KeyError) {
	kept = edits[:0]
	for _, e := range edits {
		if err := e.rewrite(schema); err != nil {
			refused = append(refused, newKeyError(schema, e.path, false, err))
			continue
		}
		kept = append(kept, e)
	}
	return kept, refused
}

// unsettable refuses each edit, of edits in key order, that the batch leaves
// nothing to set: a key beneath one set to a value that holds no keys, or no
// elements, where the key selects one — a number, a string, null — and a key
// selecting an element of a value where an earlier key selects a field or
// entry of it, which no one value can hold both of. The earlier edit stands.
func unsettable(schema *schemaNode, edits []edit) (kept []edit, refused []*KeyError) {
	kept = edits[:0]
	var open []int // kept edits whose key holds the edit being looked at, outermost first
	for _, e := range edits {
		for len(open) > 0 && !holds(kept[open[len(open)-1]].path, e.path) {
			open = open[:len(open)-1]
		}
		if len(open) > 0 {
			h := kept[open[len(open)-1]]
			if next := e.path[len(h.path)]; !canSelect(h.value, next) {
				refused = append(refused, newKeyError(schema, e.path, false, refuse("%s is set to a value with no %s beneath it", h.path, shownText(selects(next)))))
				continue
			}
		}
		if len(kept) > 0 {
			prev := kept[len(kept)-1].path
			if c := commonPrefix(prev, e.path); c < len(prev) && c < len(e.path) && (prev[c].kind == segIndex) != (e.path[c].kind == segIndex) {
				refused = append(refused, newKeyError(schema, e.path, false, refuse("keys beneath %s select both its elements and its keys", e.path[:c])))
				continue
			}
		}
		kept = append(kept, e)
		open = append(open, len(kept)-1)
	}
	return kept, refused
}

// canSelect reports whether value, as rewritten, holds what s selects: an
// element of a list, or a key of a map.
func canSelect(value any, s segment) bool {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return false
	}
	if s.kind == segIndex {
		return isList(v)
	}
	return v.Kind() == reflect.Map
}

func selects(s segment) string {
	if s.kind == segIndex {
		return "elements"
	}
	return "keys"
}

// holds reports whether key a holds key b: b begins with a and goes beneath
// it.
func holds(a, b keyPath) bool {
	return len(a) < len(b) && commonPrefix(a, b) == len(a)
}

// commonPrefix is how many leading segments a and b share, as comparePaths
// compares them.
func commonPrefix(a, b keyPath) int {
	n := 0
	for n < min(len(a), len(b)) && comparePaths(a[n:n+1], b[n:n+1]) == 0 {
		n++
	}
	return n
}

// comparePaths orders keys segment by segment — a field and a map key
// alike by name, an index by its number, names before indexes — and a key
// before the keys beneath it.
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

// sortKeyErrors orders errs by key, in place, as comparePaths orders keys:
// pool[2] before pool[10]. A key that does not parse — a malformed key as
// written, the root's $ — follows every key that does, by its text.
func sortKeyErrors(errs []*KeyError) []*KeyError {
	type sortable struct {
		err    *KeyError
		path   keyPath
		parsed bool
	}
	items := make([]sortable, len(errs))
	for i, e := range errs {
		p, err := parseKeyPath(e.Key)
		items[i] = sortable{err: e, path: p, parsed: err == nil}
	}
	slices.SortStableFunc(items, func(a, b sortable) int {
		switch {
		case a.parsed && b.parsed:
			return comparePaths(a.path, b.path)
		case a.parsed != b.parsed:
			if a.parsed {
				return -1
			}
			return 1
		}
		return strings.Compare(a.err.Key, b.err.Key)
	})
	for i, it := range items {
		errs[i] = it.err
	}
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
// its parent, so its field's tags apply. The keys inside it that no field
// reads are recorded in e.unknown.
func (e *edit) rewrite(schema *schemaNode) error {
	w := keyWalk{schema: schema}
	last := e.path[len(e.path)-1]
	at := e.path[:len(e.path)-1]
	value, _ := stringKeysIn(e.value)
	if e.parent != nil {
		switch {
		case e.parent.kind == nodeBranch:
			out := w.structMap(e.parent.typ, reflect.ValueOf(map[string]any{last.name: value}), at)
			if m, ok := out.(map[string]any); ok {
				value = m[last.name]
			}
		case e.parent.kind == nodeMap, e.parent.kind == nodeList:
			value = w.value(e.parent.typ.Elem(), value, e.path)
		case e.parent.kind == nodeDynamic:
			value = dynamicInput(value, e.path, w.failText)
		}
	}
	e.value, e.unknown, e.unkeyed = value, w.unknown, w.unkeyed
	switch {
	case len(w.twice) > 0:
		return refuse("keys spelled two ways set one key: %s", shownText(strings.Join(w.twice, ", ")))
	case len(w.notJSON) > 0:
		return refuse("format:\"json\" values that are not JSON: %s", shownText(strings.Join(w.notJSON, ", ")))
	case len(w.noText) > 0:
		return refuse("values that do not marshal to text: %s", shownText(keyList(w.noText)))
	}
	return nil
}

// editsUnkeyed is the koanf:"-" fields of the typed sections edits set.
func editsUnkeyed(edits []edit) []unkeyedFields {
	var out []unkeyedFields
	for _, e := range edits {
		out = append(out, e.unkeyed...)
	}
	return out
}

// setEdits is the source tree that sets edits, prepared and in order, over
// below, the tree beneath them: nested maps along their keys, and a listEdit
// at each index. It reads below only for the length of a list an edit
// indexes, calls it at most once and changes neither below nor the edits. An
// edit whose index it refuses is left out, in refused.
func setEdits(schema *schemaNode, below func() map[string]any, edits []edit) (tree map[string]any, refused []*KeyError) {
	t := newEditTree(below)
	for i := range edits {
		if err := t.set(&edits[i]); err != nil {
			refused = append(refused, newKeyError(schema, edits[i].path, false, err))
		}
	}
	return t.tree, refused
}

// editTree is the source tree setEdits builds, one edit at a time, so a
// caller learns which edit each refusal is.
type editTree struct {
	tree    map[string]any
	beneath func() any
}

func newEditTree(below func() map[string]any) *editTree {
	var (
		root map[string]any
		read bool
	)
	return &editTree{tree: map[string]any{}, beneath: func() any {
		if !read {
			root, read = below(), true
		}
		return root
	}}
}

// set sets e in the tree, which a refused edit leaves as it was.
func (t *editTree) set(e *edit) error {
	out, err := e.set(t.tree, t.beneath, 0)
	if err != nil {
		return err
	}
	t.tree = out.(map[string]any)
	return nil
}

// set returns tree, the edit tree at e.path[:i], with e set beneath it.
// tree is nil, what edits beneath this key built — a map, a listEdit — or
// the value an edit of a key holding e's set here, which e then edits in
// place (unsettable has refused one that cannot hold e). beneath reads what
// the tree below holds at e.path[:i]. On failure tree is as it was.
func (e *edit) set(tree any, beneath func() any, i int) (any, error) {
	if i == len(e.path) {
		return within(tree, copyInput(e.value)), nil
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
		if typed := reflect.ValueOf(tree); typed.Kind() == reflect.Map {
			return e.setEntry(typed, i)
		}
		return nil, refuse("keys beneath %s select both its elements and its keys", e.path[:i])
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

// setEntry is set in typed, a map with its own keys an earlier edit of a key
// holding e's set (see keysAsText): it replaces what lies below whole, and
// e sets the entry its key's text reads as.
func (e *edit) setEntry(typed reflect.Value, i int) (any, error) {
	c := keyCodecOf(typed.Type().Key())
	if c == nil {
		return nil, refuse("%s is keyed by %s, which has no text to select an entry by", e.path[:i], typeText{typed.Type().Key()})
	}
	k, err := c.parse(e.path[i].name)
	if err != nil {
		return nil, refuse("%s is keyed by %s, which the entry's key does not decode into", e.path[:i], typeText{typed.Type().Key()})
	}
	var at any
	if cur := typed.MapIndex(k); cur.IsValid() {
		at = cur.Interface()
	}
	child, err := e.set(at, func() any { return nil }, i+1)
	if err != nil {
		return nil, err
	}
	return setMapValue(typed, k, reflect.ValueOf(&child).Elem()).Interface(), nil
}

// within is value set where the edit tree holds tree, what an earlier edit
// of a key holding this one set: a map[string]any merges into a map there
// key by key, at every depth, as it merges into what a source beneath holds;
// any other value, a typed one included, replaces it. A map an earlier edit
// set whole stays whole.
func within(tree, value any) any {
	into, overMap := mergedMap(tree)
	if mergeStepOf(overMap, value) != stepKeys {
		return value
	}
	for k, v := range value.(map[string]any) {
		into[k] = within(into[k], v)
	}
	return tree
}

// setElement is set at an index: a listEdit over the list below, or, where
// an earlier edit set a list here, that list with one element set.
func (e *edit) setElement(tree any, beneath func() any, i int) (any, error) {
	s := e.path[i]
	edits, editing := tree.(listEdit)
	if tree != nil && !editing {
		if !isList(reflect.ValueOf(tree)) {
			return nil, refuse("keys beneath %s select both its elements and its keys", e.path[:i])
		}
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

// entryOf is what v, a tree beneath an edit, holds under key where the
// edit's map merges into it (see mergeValue): a typed map is replaced
// whole, and holds nothing an edit reads.
func entryOf(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
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
