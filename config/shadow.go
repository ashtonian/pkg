package config

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding"
	"fmt"
	"hash"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Shadow is one shadowed input: Source sets Key, and a higher-precedence
// source, By, sets it too, so Source's value cannot take effect. It names
// sources, never values.
type Shadow struct {
	// Key is the key in the package's key grammar, or the enclosing
	// sensitive container when the keys inside it are withheld.
	Key string `json:"key"`
	// Source is the source whose input is shadowed. An environment variable
	// that names a withheld key is reported by its prefix only.
	Source Source `json:"source"`
	// By is the source whose value the merge keeps for Key.
	By Source `json:"by"`
}

func (s Shadow) String() string {
	return s.Key + " (" + s.Source.String() + ", kept by " + s.By.String() + ")"
}

// ShadowedInputsError reports the inputs of a load that a higher-precedence
// source outranks (see [WithShadowedInputs]). A load fails with it only when
// a WithShadowedInputs handler returns it.
type ShadowedInputsError struct {
	// Type is the config type that was loaded.
	Type reflect.Type `json:"-"`
	// Inputs are the shadowed inputs, sorted by Key, then Source.
	Inputs []Shadow `json:"inputs"`
}

func (e *ShadowedInputsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "inputs of %s that a higher-precedence source outranks: ", typeText(e.Type))
	for i, s := range e.Inputs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s.String())
	}
	return b.String()
}

// WithShadowedInputs decides what a load does with shadowed inputs: values a
// source sets for a key that a higher-precedence source also sets, so the
// lower value cannot take effect — a config file's key that a leftover
// environment variable or a typed flag also sets. handle is called with them
// whenever there are any, at every [Load] and every reload. A nil return lets
// the load go on; an error fails it with [ErrValidation], the error kept as
// its cause. Return shadowed to require that every key have one source:
//
//	config.WithShadowedInputs(func(_ context.Context, shadowed *config.ShadowedInputsError) error {
//	    return shadowed
//	})
//
// Overriding a file from the environment or the command line is a legitimate
// pattern, so without a handler shadowed inputs never fail a load:
// [LoadSnapshot] and every [NewReloader] load list them in Snapshot.Shadowed
// and log them at debug level, and [Load] does not look for them. A nil
// handle restores that default.
//
// Two kinds of layering are designed overrides and never reported: defaults
// (default tags, [DefaultProvider] and [WithDefaults]) beneath any source,
// and the precedence inside the environment layer — [WithEnvPrefixes]'
// narrower prefixes over broader ones, prefixed names over env tag names —
// which is one source. Inputs are named by key and source, never value; an
// input at a key no field reads is left to [WithUnknownKeys].
func WithShadowedInputs(handle func(ctx context.Context, shadowed *ShadowedInputsError) error) Option {
	return func(o *options) {
		o.shadowedInputs = handle
	}
}

// judgeShadowed finds the shadowed inputs of a load of root, logs them at
// debug level and hands a copy to the WithShadowedInputs handler.
func (o *options) judgeShadowed(ctx context.Context, root reflect.Type, in *inputs) ([]Shadow, error) {
	shadowed := in.shadowed()
	if len(shadowed) == 0 {
		return nil, nil
	}
	o.log().DebugContext(ctx, "config inputs a higher-precedence source outranks", "shadowed", shadowTexts(shadowed))
	if o.shadowedInputs != nil {
		if err := o.shadowedInputs(ctx, &ShadowedInputsError{Type: root, Inputs: slices.Clone(shadowed)}); err != nil {
			return nil, fmt.Errorf("loading config: %w: %w", ErrValidation, err)
		}
	}
	return shadowed, nil
}

func shadowTexts(shadowed []Shadow) []string {
	texts := make([]string, len(shadowed))
	for i, s := range shadowed {
		texts[i] = s.String()
	}
	return texts
}

// inputs is what the non-default sources of one load set — each input by
// raw path, and the load's attribution tree, which says which source the
// merge kept at every path. With a digest key it holds a keyed digest of
// each input's value, never the value, so a Reloader can tell what changed
// since the previous load without keeping a copy.
//
// Every reload records every input, so recording reuses one path buffer,
// one key buffer and one encoder for the whole load.
type inputs struct {
	schema  *schemaNode
	tree    *originNode
	enc     *encoder // nil: no digests
	sources []sourceInputs
	seen    map[string]int // source name → merges under it so far
	path    []string       // the raw path of the walk, segment by segment
	key     []byte         // path's encoding, built for each input
}

// sourceInputs are one source's inputs: each one's digest — zero without a
// digest key — by its encoded raw path (see encodePath). id names the
// source the same way at every load of one set of options; rank is its
// merge position in this load, which an optional file's absence shifts.
// label names an input's source; it holds names, never values, and is asked
// only for an input that is reported.
type sourceInputs struct {
	id     string
	rank   int
	label  labeler
	values map[string][sha256.Size]byte
}

func newInputs(schema *schemaNode, tree *originNode, key []byte) *inputs {
	in := &inputs{schema: schema, tree: tree, seen: map[string]int{}}
	if key != nil {
		in.enc = newEncoder(key)
	}
	return in
}

// add records the inputs of tree, the source called name merged at rank. A
// map[string]any merges key by key, so its entries are the inputs; any
// other value replaces what was there whole, so it is one input.
func (in *inputs) add(name string, rank int, tree map[string]any, label labeler) {
	if label.defaults() {
		return
	}
	id := name
	if n := in.seen[name]; n > 0 {
		id = name + " #" + strconv.Itoa(n+1) // one file named twice
	}
	in.seen[name]++
	s := sourceInputs{id: id, rank: rank, label: label, values: make(map[string][sha256.Size]byte, leaves(tree))}
	in.walk(tree, s.values)
	in.sources = append(in.sources, s)
}

func (in *inputs) walk(m map[string]any, values map[string][sha256.Size]byte) {
	for key, val := range m {
		in.path = append(in.path, key)
		if child, ok := val.(map[string]any); ok {
			in.walk(child, values)
		} else {
			var sum [sha256.Size]byte
			if in.enc != nil {
				sum = in.enc.digest(val)
			}
			in.key = encodePath(in.key[:0], in.path)
			values[string(in.key)] = sum
		}
		in.path = in.path[:len(in.path)-1]
	}
}

// leaves counts the inputs tree holds.
func leaves(tree map[string]any) int {
	n := 0
	for _, val := range tree {
		if child, ok := val.(map[string]any); ok {
			n += leaves(child)
		} else {
			n++
		}
	}
	return n
}

// encodePath appends path to b, each segment as its length, ":" and its
// bytes: no key text can be mistaken for structure.
func encodePath(b []byte, path []string) []byte {
	for _, seg := range path {
		b = strconv.AppendInt(b, int64(len(seg)), 10)
		b = append(b, ':')
		b = append(b, seg...)
	}
	return b
}

// decodePath is the raw path path encodes.
func decodePath(path string) []string {
	var raw []string
	for rest := path; rest != ""; {
		var seg string
		seg, rest = nextSegment(rest)
		raw = append(raw, seg)
	}
	return raw
}

// nextSegment splits the first segment off path, an encodePath encoding.
func nextSegment(path string) (seg, rest string) {
	colon := strings.IndexByte(path, ':')
	n, _ := strconv.Atoi(path[:colon]) // encodePath wrote it
	end := colon + 1 + n
	return path[colon+1 : end], path[end:]
}

// shadowed lists every input a higher-precedence source outranks.
func (in *inputs) shadowed() []Shadow {
	var out []Shadow
	for _, s := range in.sources {
		for path := range s.values {
			out = in.appendShadowed(out, path, s.label, s.rank)
		}
	}
	return sortShadows(out)
}

// changedSince lists the inputs that changed since prev, the previous load's
// inputs by source, while a higher-precedence source holds their key now: a
// value changed, a key set or a key removed. A source absent from this load
// has nothing to report; one new to it reports every input it sets.
func (in *inputs) changedSince(prev map[string]sourceInputs) []Shadow {
	var out []Shadow
	for _, s := range in.sources {
		before := prev[s.id]
		for path, sum := range s.values {
			if was, ok := before.values[path]; !ok || was != sum {
				out = in.appendShadowed(out, path, s.label, s.rank)
			}
		}
		for path := range before.values {
			if _, ok := s.values[path]; !ok {
				out = in.appendShadowed(out, path, before.label, s.rank)
			}
		}
	}
	return sortShadows(out)
}

// bySource is in without the tree: what a Reloader keeps between loads.
func (in *inputs) bySource() map[string]sourceInputs {
	out := make(map[string]sourceInputs, len(in.sources))
	for _, s := range in.sources {
		out[s.id] = s
	}
	return out
}

// appendShadowed appends the input at path of a source merged at rank,
// which label names, when a higher-ranked source holds path.
func (in *inputs) appendShadowed(out []Shadow, path string, label labeler, rank int) []Shadow {
	holder := in.tree.holder(path)
	if holder == nil || holder.rank <= rank {
		return out
	}
	full, ok := inputKey(in.schema, path)
	if !ok {
		return out
	}
	key := diagnosticPath(in.schema, full)
	src, by := label.at(decodePath(path)), holder.kept()
	if len(key) < len(full) {
		src, by = withheld(src), withheld(by)
	}
	return append(out, Shadow{Key: key.orRoot(), Source: src, By: by})
}

// holder is the node whose source the merge kept for path, a raw path's
// encodePath encoding: the node at path, or the ancestor a source replaced
// whole with a value that is no map. It is nil when nothing holds path.
func (n *originNode) holder(path string) *originNode {
	node := n
	for rest := path; rest != ""; {
		var key string
		key, rest = nextSegment(rest)
		if !node.merges {
			return node
		}
		child := node.children[key]
		if child == nil {
			return nil
		}
		node = child
	}
	return node
}

// kept is the source of what n holds: its own, or for a map that a higher
// source made of a value, the one source of everything in it.
func (n *originNode) kept() Source {
	if !n.merges {
		return n.src
	}
	if src, ok := n.uniform(); ok {
		return src
	}
	return Source{Kind: SourceMixed}
}

// inputKey is the key an input at raw path — its encodePath encoding —
// sets: cut at a list or a value decoded whole, which the input sets as
// part of it. It is false for a path no field reads.
func inputKey(root *schemaNode, raw string) (keyPath, bool) {
	var p keyPath
	n := root
	for rest := raw; rest != ""; {
		var name string
		name, rest = nextSegment(rest)
		switch n.kind {
		case nodeBranch:
			f := n.byKey[name]
			if f == nil {
				return nil, false
			}
			p, n = p.field(f.key), f.node
		case nodeMap:
			p, n = p.key(name), n.elem
		default:
			return p, true
		}
	}
	return p, true
}

// withheld is src for a key cut short to withhold the keys of a sensitive
// container: a variable's name spells the key it sets, so only its prefix
// is kept.
func withheld(src Source) Source {
	if src.Kind == SourceEnv {
		return Source{Kind: SourceEnv, Prefix: src.Prefix}
	}
	return src
}

func sortShadows(s []Shadow) []Shadow {
	slices.SortFunc(s, func(a, b Shadow) int {
		return cmp.Or(
			strings.Compare(a.Key, b.Key),
			strings.Compare(a.Source.String(), b.Source.String()),
			strings.Compare(a.Source.Prefix, b.Source.Prefix),
			strings.Compare(a.By.String(), b.By.String()),
			strings.Compare(a.By.Prefix, b.By.Prefix),
		)
	})
	return slices.Compact(s)
}

// encoder digests values for one load: HMAC-SHA256 under the Reloader's
// key over a canonical encoding — type-tagged, every string
// length-prefixed, pointers and interfaces followed, map entries sorted —
// so equal values digest equally however they were built (fresh pointers,
// maps in any order), and a digest cannot be checked against guessed
// values. A pointer, map or slice met again within itself is written as a
// cycle. It reuses its hash and buffers for every value; it is not safe for
// concurrent use.
type encoder struct {
	h        hash.Hash
	w        io.Writer // h, or a map key's buffer while one is encoded
	buf      []byte    // one write's bytes
	num      []byte    // a scalar's text, which buf prefixes with its length
	sum      []byte
	keys     []string       // a stack of string-keyed maps' sorted keys
	visiting map[visit]bool // the containers being encoded; made on first use
}

func newEncoder(key []byte) *encoder {
	h := hmac.New(sha256.New, key)
	return &encoder{h: h, w: h}
}

func (e *encoder) digest(v any) [sha256.Size]byte {
	e.h.Reset()
	e.any(v)
	e.sum = e.h.Sum(e.sum[:0])
	return [sha256.Size]byte(e.sum)
}

// The type names the parsers' own values are tagged with; reflection
// tags every other value with the same names.
var (
	stringTypeName   = reflect.TypeFor[string]().String()
	boolTypeName     = reflect.TypeFor[bool]().String()
	intTypeName      = reflect.TypeFor[int]().String()
	int64TypeName    = reflect.TypeFor[int64]().String()
	float64TypeName  = reflect.TypeFor[float64]().String()
	listType         = reflect.TypeFor[[]any]()
	stringMapType    = reflect.TypeFor[map[string]any]()
	textMarshalerTyp = reflect.TypeFor[encoding.TextMarshaler]()
)

// any encodes v. The values parsers and providers produce take a path
// without reflection; it writes what value writes for them.
func (e *encoder) any(v any) {
	switch x := v.(type) {
	case nil:
		e.tag('z')
	case string:
		e.text(stringTypeName)
		e.text(x)
	case bool:
		e.text(boolTypeName)
		e.num = strconv.AppendBool(e.num[:0], x)
		e.bytes(e.num)
	case int:
		e.text(intTypeName)
		e.int(int64(x))
	case int64:
		e.text(int64TypeName)
		e.int(x)
	case float64:
		e.text(float64TypeName)
		e.float(x)
	case []any:
		e.text(listType.String())
		if x == nil {
			e.length(0)
			return
		}
		if len(x) > 0 {
			at := visit{ptr: reflect.ValueOf(&x[0]).Pointer(), typ: listType}
			if !e.enter(at) {
				return
			}
			defer e.leave(at)
		}
		e.length(len(x))
		for _, elem := range x {
			e.any(elem)
		}
	case map[string]any:
		e.text(stringMapType.String())
		if x == nil {
			e.length(0)
			return
		}
		at := visit{ptr: reflect.ValueOf(x).Pointer(), typ: stringMapType}
		if !e.enter(at) {
			return
		}
		defer e.leave(at)
		mark := len(e.keys)
		for k := range x {
			e.keys = append(e.keys, k)
		}
		// A nested map appends past these, so they stay put while it runs.
		keys := e.keys[mark:]
		slices.Sort(keys)
		e.length(len(keys))
		for _, k := range keys {
			e.text(stringTypeName)
			e.text(k)
			e.any(x[k])
		}
		e.keys = e.keys[:mark]
	default:
		e.value(reflect.ValueOf(v))
	}
}

func (e *encoder) value(v reflect.Value) {
	if !v.IsValid() {
		e.tag('z')
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			e.tag('z')
			return
		}
		if v.Kind() == reflect.Pointer {
			at := visitOf(v)
			if !e.enter(at) {
				return
			}
			defer e.leave(at)
		}
		e.value(v.Elem())
		return
	}
	e.text(v.Type().String())
	if v.CanInterface() && marshalsText(v.Type()) {
		if m, ok := textMarshaler(v); ok {
			if text, err := m.MarshalText(); err == nil {
				e.tag('t')
				e.bytes(text)
				return
			}
		}
	}
	switch v.Kind() {
	case reflect.Bool:
		e.num = strconv.AppendBool(e.num[:0], v.Bool())
		e.bytes(e.num)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.num = strconv.AppendUint(e.num[:0], v.Uint(), 10)
		e.bytes(e.num)
	case reflect.Float32, reflect.Float64:
		e.float(v.Float())
	case reflect.Complex64, reflect.Complex128:
		e.text(strconv.FormatComplex(v.Complex(), 'g', -1, 128))
	case reflect.String:
		e.text(v.String())
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			e.length(0)
			return
		}
		if v.Kind() == reflect.Slice && v.Len() > 0 {
			at := visit{ptr: v.Pointer(), typ: v.Type()}
			if !e.enter(at) {
				return
			}
			defer e.leave(at)
		}
		e.length(v.Len())
		for i := range v.Len() {
			e.value(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() {
			e.length(0)
			return
		}
		at := visitOf(v)
		if !e.enter(at) {
			return
		}
		defer e.leave(at)
		e.entries(v)
	case reflect.Struct:
		e.length(v.NumField())
		for i := range v.NumField() {
			e.value(v.Field(i))
		}
	}
	// A channel, function or unsafe pointer is its type alone.
}

// marshalsText reports a type that is, or whose pointer is, a
// TextMarshaler: only a type with methods can be, so a parser's scalars are
// never boxed to ask.
func marshalsText(t reflect.Type) bool {
	if t.Implements(textMarshalerTyp) {
		return true
	}
	return t.Kind() != reflect.Interface && reflect.PointerTo(t).Implements(textMarshalerTyp)
}

// entries writes a map's entries in order: by key for string keys, as any
// writes a map[string]any's, and by the keys' encodings otherwise.
func (e *encoder) entries(v reflect.Value) {
	keys := v.MapKeys()
	if v.Type().Key().Kind() == reflect.String {
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		e.length(len(keys))
		for _, k := range keys {
			e.value(k)
			e.value(v.MapIndex(k))
		}
		return
	}
	type entry struct {
		key []byte
		val reflect.Value
	}
	entries := make([]entry, len(keys))
	w := e.w
	for i, k := range keys {
		var encoded bytes.Buffer
		e.w = &encoded
		e.value(k)
		entries[i] = entry{key: encoded.Bytes(), val: v.MapIndex(k)}
	}
	e.w = w
	slices.SortFunc(entries, func(a, b entry) int { return bytes.Compare(a.key, b.key) })
	e.length(len(entries))
	for _, en := range entries {
		e.bytes(en.key)
		e.value(en.val)
	}
}

// enter marks the container at as being encoded, or writes a cycle and
// reports false when it already is. Every enter is paired with a leave, so
// the set is empty between values.
func (e *encoder) enter(at visit) bool {
	if e.visiting == nil {
		e.visiting = map[visit]bool{}
	}
	if e.visiting[at] {
		e.tag('c')
		return false
	}
	e.visiting[at] = true
	return true
}

func (e *encoder) leave(at visit) { delete(e.visiting, at) }

// The writers are a hash and a bytes.Buffer, whose writes never fail.

func (e *encoder) tag(b byte) {
	e.buf = append(e.buf[:0], b)
	_, _ = e.w.Write(e.buf)
}

func (e *encoder) length(n int) {
	e.buf = strconv.AppendInt(append(e.buf[:0], 'n'), int64(n), 10)
	e.buf = append(e.buf, ':')
	_, _ = e.w.Write(e.buf)
}

func (e *encoder) bytes(b []byte) {
	e.length(len(b))
	_, _ = e.w.Write(b)
}

func (e *encoder) text(s string) {
	e.length(len(s))
	e.buf = append(e.buf[:0], s...)
	_, _ = e.w.Write(e.buf)
}

func (e *encoder) int(i int64) {
	e.num = strconv.AppendInt(e.num[:0], i, 10)
	e.bytes(e.num)
}

func (e *encoder) float(f float64) {
	e.num = strconv.AppendFloat(e.num[:0], f, 'g', -1, 64)
	e.bytes(e.num)
}
