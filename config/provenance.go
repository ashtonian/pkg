package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/v2"
)

// SourceKind names the kind of source that set a value.
type SourceKind string

// Source kinds, lowest precedence first among the inputs.
const (
	SourceZero     SourceKind = "zero"              // no source set it: the type's zero value
	SourceTag      SourceKind = "tag_default"       // a default:"..." tag
	SourceDefaults SourceKind = "defaults"          // WithDefaults
	SourceBase     SourceKind = "base_provider"     // WithBaseProvider; Name is its position
	SourceFile     SourceKind = "file"              // a config file; Name is its path
	SourceEnv      SourceKind = "environment"       // Name is the variable
	SourceOverride SourceKind = "override_provider" // WithOverrideProvider; Name is its position
	SourceFlag     SourceKind = "flag"              // a command-line flag, as an OriginProvider names it
	SourceSecrets  SourceKind = "secrets"           // the secret populator, as a transform
	SourcePostLoad SourceKind = "post_load"         // a WithPostLoad hook, as a transform; Name is its position
	SourceMixed    SourceKind = "mixed"             // a value compared whole that more than one source set part of
	SourceUnknown  SourceKind = "unknown"           // decoding made the value: no source can be named
)

// Source names what set a value: its kind, and a name — a file's path, a
// variable, a flag, a provider's position. It never holds a value.
type Source struct {
	Kind SourceKind `json:"kind"`
	Name string     `json:"name,omitempty"`
}

func (s Source) String() string {
	if s.Name == "" {
		return string(s.Kind)
	}
	return string(s.Kind) + " " + s.Name
}

// Origin is where one key's value came from: the input source that set it
// as the sources merged, and the pipeline stages that changed it after
// decoding.
type Origin struct {
	// Input is the source whose value the merge kept for the key: what the
	// key held when it was decoded, before any stage ran. A key no source
	// set holds its zero value.
	Input Source `json:"input"`
	// Transforms are the stages that changed the value after decoding, in
	// order: the secret populator, then post-load hooks by position. A stage
	// that wrote the value the key already held is not listed. With any
	// listed, the value is the last one's — computed from whatever it read —
	// and Input names only what it replaced.
	Transforms []Source `json:"transforms,omitempty"`
	// Exact is false when Input cannot be named with certainty: decoding
	// reshaped the input — a weakly decoded shape, or a decode hook that
	// rebuilt a map or list — so Input is the one source of what the value
	// was decoded from, or unknown.
	Exact bool `json:"exact"`
}

// OriginProvider is a map provider that names a source for each key it
// returns, read once with the load's context: kongprovider names the flag
// behind each value. Annotations are keyed in the package's key grammar; an
// annotation follows field aliases and case folding as the data does. An
// annotation on a section covers the keys beneath it, and a key with none is
// attributed to the provider. An annotation naming a key the data does not
// hold, using a selector of the wrong kind, naming a list element, or naming
// the key another annotation names with another source fails the load with
// [ErrSource]. Only provenance reads the annotations; it is used for
// providers loaded with a nil parser.
type OriginProvider interface {
	koanf.Provider
	ReadWithOrigins(ctx context.Context) (map[string]any, map[string]Source, error)
}

// LoadSnapshot runs [Load] and records where every key's value came from:
// the snapshot's Provenance holds an [Origin] for each key, and its
// Generation is 0. Recording costs a tree of the merged sources, a copy and
// a comparison of the decoded config around the secret populator and each
// post-load hook, and one walk for the rows; Load records nothing.
func LoadSnapshot[T any](ctx context.Context, opts ...Option) (Snapshot[T], error) {
	o := buildOptions(opts)
	o.provenance = true
	l, err := load[T](ctx, o)
	if err != nil {
		return Snapshot[T]{}, err
	}
	return Snapshot[T]{Config: l.cfg, Provenance: l.origins}, nil
}

// originNode is what set one value of the merged tree. It mirrors koanf's
// merge: a map merges key by key, an empty one changing nothing, and any
// other value — a list included — replaces what was there, so everything
// beneath it is the new source's. A map's entries and a list's elements are
// nodes of their own, so what the element defaults add after the merge is
// told from what a source set.
type originNode struct {
	src      Source
	children map[string]*originNode // a map's entries; nil for anything else
	elems    []*originNode          // a list's elements; nil for anything else
	// merges reports a map koanf merges into, key by key: a
	// map[string]any. Any other map is replaced whole.
	merges bool
}

func newOriginRoot() *originNode {
	return &originNode{children: map[string]*originNode{}, merges: true}
}

// labeler names the source of the values of one source's tree: one source
// for all of them, or one by raw path.
type labeler struct {
	fixed  Source
	byPath func(path []string) Source // nil: every value is fixed's
}

func constant(src Source) labeler { return labeler{fixed: src} }

// originMerge merges one source's tree into the origins, tracking the raw
// path only for a labeler that reads it.
type originMerge struct {
	label labeler
	path  []string
}

func (m *originMerge) source() Source {
	if m.label.byPath == nil {
		return m.label.fixed
	}
	return m.label.byPath(m.path)
}

func (m *originMerge) enter(key string) {
	if m.label.byPath != nil {
		m.path = append(m.path, key)
	}
}

func (m *originMerge) leave() {
	if m.label.byPath != nil {
		m.path = m.path[:len(m.path)-1]
	}
}

// merge merges incoming, a map of the source's tree, into n, a map node.
func (n *originNode) merge(incoming map[string]any, m *originMerge) {
	for key, val := range incoming {
		m.enter(key)
		child := n.children[key]
		if in, isMap := val.(map[string]any); isMap && child != nil && child.merges {
			child.merge(in, m)
		} else {
			n.children[key] = m.node(val)
		}
		m.leave()
	}
}

// node is the origin of val, set whole by the source at the merge's path.
func (m *originMerge) node(val any) *originNode {
	n := &originNode{src: m.source()}
	switch v := val.(type) {
	case map[string]any:
		n.merges = true
		n.children = make(map[string]*originNode, len(v))
		for key, child := range v {
			m.enter(key)
			n.children[key] = m.node(child)
			m.leave()
		}
	case []any:
		n.elems = make([]*originNode, len(v))
		for i, e := range v {
			n.elems[i] = setBy(e, n.src)
		}
	default:
		n.children, n.elems = structureOf(val, n.src)
	}
	return n
}

// setBy is the origin of val and everything in it, all set by src: a list's
// element, which its list's source set.
func setBy(val any, src Source) *originNode {
	m := originMerge{label: constant(src)}
	return m.node(val)
}

// structureOf gives a map or list of any other type its entries' or
// elements' nodes, all set by src.
func structureOf(val any, src Source) (map[string]*originNode, []*originNode) {
	v := reflect.ValueOf(val)
	switch {
	case !v.IsValid():
	case v.Kind() == reflect.Map && !v.IsNil():
		children := make(map[string]*originNode, v.Len())
		for it := v.MapRange(); it.Next(); {
			children[fmt.Sprint(it.Key().Interface())] = setBy(it.Value().Interface(), src)
		}
		return children, nil
	case v.Kind() == reflect.Slice && !v.IsNil(), v.Kind() == reflect.Array:
		elems := make([]*originNode, v.Len())
		for i := range v.Len() {
			elems[i] = setBy(v.Index(i).Interface(), src)
		}
		return nil, elems
	}
	return nil, nil
}

// fill attributes what merged, the tree after element defaults filled it,
// holds that n does not — the defaults — to the element's tag. It returns
// how many values merged holds: about the number of keys with an origin.
func (n *originNode) fill(merged any) int {
	v := reflect.ValueOf(merged)
	switch {
	case n.children != nil && v.IsValid() && v.Kind() == reflect.Map && v.Len() > 0:
		count := 0
		for it := v.MapRange(); it.Next(); {
			key := fmt.Sprint(it.Key().Interface())
			if child := n.children[key]; child != nil {
				count += child.fill(it.Value().Interface())
			} else {
				child = setBy(it.Value().Interface(), Source{Kind: SourceTag})
				n.children[key] = child
				count += child.values()
			}
		}
		return count
	case n.elems != nil && v.IsValid() && isList(v) && v.Len() > 0:
		count := 0
		for i := range min(v.Len(), len(n.elems)) {
			count += n.elems[i].fill(v.Index(i).Interface())
		}
		return count
	}
	return 1
}

// values counts the values at and beneath n.
func (n *originNode) values() int {
	if len(n.children) == 0 && len(n.elems) == 0 {
		return 1
	}
	count := 0
	for _, child := range n.children {
		count += child.values()
	}
	for _, child := range n.elems {
		count += child.values()
	}
	return count
}

// uniform is the one source everything at and beneath n came from, or false
// when several did.
func (n *originNode) uniform() (Source, bool) {
	src, first, ok := n.src, true, true
	visit := func(child *originNode) {
		s, same := child.uniform()
		switch {
		case !same:
			ok = false
		case first:
			src, first = s, false
		case s != src:
			ok = false
		}
	}
	for _, child := range n.children {
		visit(child)
	}
	for _, child := range n.elems {
		visit(child)
	}
	return src, ok
}

// originOf finds the input behind key p through the merged tree. found is
// false when no source set the key.
func (n *originNode) originOf(p keyPath) (o Origin, found bool) {
	node := n
	for _, s := range p {
		switch {
		case node.children == nil && node.elems == nil:
			// A value that is no map or list — a scalar, or null — that the
			// key lies within: decoded whole from what that source set.
			return Origin{Input: node.src, Exact: true}, true
		case s.kind == segIndex && node.elems != nil:
			if s.index >= len(node.elems) {
				return reshaped(node), true
			}
			node = node.elems[s.index]
		case s.kind == segIndex && s.index == 0:
			// Weak decoding takes a map as a list's one element.
		case s.kind == segIndex:
			return reshaped(node), true
		case node.children != nil:
			child := node.children[s.name]
			if child == nil {
				return Origin{}, false
			}
			node = child
		default:
			// Weak decoding merges a list of maps into one map, a later
			// element's entry replacing an earlier one's.
			var child *originNode
			for _, e := range slices.Backward(node.elems) {
				if child = e.children[s.name]; child != nil {
					break
				}
			}
			if child == nil {
				return Origin{}, false
			}
			node = child
		}
	}
	if node.children == nil && node.elems == nil {
		return Origin{Input: node.src, Exact: true}, true
	}
	// A map or list decoded whole: a leaf, a sensitive one, an empty one.
	if src, ok := node.uniform(); ok {
		return Origin{Input: src, Exact: true}, true
	}
	return Origin{Input: Source{Kind: SourceMixed}, Exact: true}, true
}

func reshaped(n *originNode) Origin {
	if src, ok := n.uniform(); ok {
		return Origin{Input: src}
	}
	return Origin{Input: Source{Kind: SourceUnknown}}
}

// envLabeler attributes each value of the environment layer to the variable
// that set it; flat maps each flat key to its variable.
func envLabeler(prefix string, flat map[string]string) labeler {
	return labeler{byPath: func(path []string) Source {
		// A key beneath one a variable set whole — a map's entries, a JSON
		// value's keys — is that variable's.
		for i := len(path); i > 0; i-- {
			if name, ok := flat[strings.Join(path[:i], ".")]; ok {
				return Source{Kind: SourceEnv, Name: name}
			}
		}
		return Source{Kind: SourceEnv, Name: prefix + "*"}
	}}
}

// annotationLabeler attributes values by an OriginProvider's annotations; a
// key with none takes its nearest annotated section's, then fallback. Each
// annotation must name a key of root that tree holds, by selectors of the
// right kind — a field, or a map entry — and no two may name one key with
// different sources.
func annotationLabeler(root *schemaNode, tree map[string]any, annotations map[string]Source, fallback Source) (labeler, error) {
	byPath := make(map[string]Source, len(annotations))
	named := make(map[string]string, len(annotations)) // raw path → the annotation naming it
	for _, key := range slices.Sorted(maps.Keys(annotations)) {
		src := annotations[key]
		p, err := parseKeyPath(key)
		if err != nil {
			return labeler{}, err
		}
		p = annotationPath(root, p)
		shown := diagnosticPath(root, p)
		if _, _, err := root.walkTo(cursor{}, p); err != nil {
			if len(shown) < len(p) {
				return labeler{}, fmt.Errorf("annotation %s: invalid key within sensitive contents", shown)
			}
			return labeler{}, fmt.Errorf("annotation %s: %w", key, err)
		}
		raw := make([]string, len(p))
		for i, s := range p {
			if s.kind != segField && s.kind != segKey {
				return labeler{}, fmt.Errorf("annotation %s: annotate a list as a whole, not an element", shown)
			}
			raw[i] = s.name
		}
		if !rawPresent(tree, raw) {
			return labeler{}, fmt.Errorf("annotation %s names a key the provider did not return", shown)
		}
		at := strings.Join(raw, "\x00")
		if prev, ok := named[at]; ok && byPath[at] != src {
			return labeler{}, fmt.Errorf("annotations %s and %s name one key with different sources", prev, shown)
		}
		byPath[at], named[at] = src, shown.String()
	}
	return labeler{byPath: func(path []string) Source {
		for i := len(path); i > 0; i-- {
			if src, ok := byPath[strings.Join(path[:i], "\x00")]; ok {
				return src
			}
		}
		return fallback
	}}, nil
}

// Annotations follow the same field spelling and aliases as provider data.
func annotationPath(root *schemaNode, p keyPath) keyPath {
	n := root
	for i := range p {
		switch n.kind {
		case nodeBranch:
			if p[i].kind != segField {
				return p
			}
			f := n.byKey[p[i].name]
			if f == nil {
				f = foldedField(n.byKey, p[i].name)
			}
			if f == nil {
				f = foldedField(n.byAlias, p[i].name)
			}
			if f == nil {
				return p
			}
			p[i].name, n = f.key, f.node
		case nodeMap, nodeList:
			n = n.elem
		default:
			return p
		}
	}
	return p
}

func rawPresent(tree map[string]any, path []string) bool {
	var cur any = tree
	for _, name := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[name]; !ok {
			return false
		}
	}
	return true
}

// originRecorder records provenance for one load: the merge's origins, the
// types a decode hook rebuilt, and the stages that changed the decoded
// config.
type originRecorder struct {
	schema *schemaNode
	tree   *originNode
	// rebuilt are the types a custom decode hook gave a map or list other
	// than the one it was handed: what is in them cannot be traced.
	rebuilt map[reflect.Type]bool
	// decoded is the config as decoding left it, before any stage: a copy,
	// once a stage has run.
	decoded reflect.Value
	stages  []stageChanges
	// values is about how many keys have an origin, for sizing them.
	values int
}

// stageChanges is what one stage changed: keys, and keys compared whole
// whose every key beneath changed with them.
type stageChanges struct {
	src   Source
	keys  map[string]bool
	under []keyPath
}

func newOriginRecorder(schema *schemaNode) *originRecorder {
	return &originRecorder{schema: schema, tree: newOriginRoot(), rebuilt: map[reflect.Type]bool{}}
}

// observeHook wraps hook, the composed custom decode hooks, to note each
// target type it hands a map or list other than the one it was given.
func (r *originRecorder) observeHook(hook mapstructure.DecodeHookFunc) mapstructure.DecodeHookFunc {
	return func(from, to reflect.Value) (any, error) {
		out, err := mapstructure.DecodeHookExec(hook, from, to)
		if err == nil && replacedStructure(from, out) {
			r.rebuilt[derefType(to.Type())] = true
		}
		return out, err
	}
}

// replacedStructure reports a map or list in that out is not: a hook that
// passes a value on returns it as it came.
func replacedStructure(in reflect.Value, out any) bool {
	for in.Kind() == reflect.Interface && !in.IsNil() {
		in = in.Elem()
	}
	if in.Kind() != reflect.Map && in.Kind() != reflect.Slice {
		return false
	}
	o := reflect.ValueOf(out)
	return !o.IsValid() || o.Type() != in.Type() || o.Pointer() != in.Pointer() || o.Len() != in.Len()
}

// stage runs one stage that may change cfg and records what it changed.
func (r *originRecorder) stage(cfg reflect.Value, src Source, run func() error) error {
	before := deepCopy(cfg)
	if !r.decoded.IsValid() {
		r.decoded = before
	}
	if err := run(); err != nil {
		return err
	}
	st := stageChanges{src: src, keys: map[string]bool{}}
	err := compare(r.schema, before, cfg, func(d *delta) {
		if d.whole {
			st.under = append(st.under, slices.Clone(d.path))
		} else {
			st.keys[d.path.String()] = true
		}
	})
	if err != nil {
		return fmt.Errorf("recording provenance: %w", err)
	}
	r.stages = append(r.stages, st)
	return nil
}

// origins attributes every value of cfg, the final config: each key that
// holds a value rather than keys of its own.
func (r *originRecorder) origins(cfg reflect.Value) (map[string]Origin, error) {
	decoded := r.decoded
	if !decoded.IsValid() {
		decoded = cfg
	}
	out := make(map[string]Origin, r.values)
	err := compareEvery(r.schema, cfg, func(d *delta) {
		s := d.sides[0]
		if d.mode == deltaShape && !d.whole && !emptyContainer(s) {
			return // its values have rows of their own
		}
		key := d.path.String()
		o, found := r.tree.originOf(d.path)
		if !found {
			at, _, _ := locate(r.schema, decoded, d.path)
			if at.present && !at.isNil && at.v.IsValid() && !at.v.IsZero() {
				o = Origin{Input: Source{Kind: SourceUnknown}} // a decoder made it
			} else {
				o = Origin{Input: Source{Kind: SourceZero}, Exact: true}
			}
		}
		if o.Exact && len(r.rebuilt) > 0 && r.rebuiltAt(d.path) {
			o.Exact = false
		}
		for _, st := range r.stages {
			if st.keys[key] || slices.ContainsFunc(st.under, func(p keyPath) bool { return len(d.path) >= len(p) && slices.Equal(d.path[:len(p)], p) }) {
				o.Transforms = append(o.Transforms, st.src)
			}
		}
		out[key] = o
	})
	if err != nil {
		return nil, fmt.Errorf("recording provenance: %w", err)
	}
	return out, nil
}

// rebuiltAt reports whether a custom decode hook rebuilt a map or list on
// the way to key p: for its declared type, or one it lies within.
func (r *originRecorder) rebuiltAt(p keyPath) bool {
	n := r.schema
	if r.rebuilt[n.typ] {
		return true
	}
	for _, s := range p {
		switch n.kind {
		case nodeBranch:
			f := n.byKey[s.name]
			if f == nil {
				return false
			}
			n = f.node
		case nodeMap, nodeList:
			n = n.elem
		default:
			return false // beneath a value decoded whole or an interface: judged at it
		}
		if r.rebuilt[n.typ] {
			return true
		}
	}
	return false
}

// emptyContainer reports a struct, map or list with nothing beneath it to
// attribute: nil, or a map or list with no entries.
func emptyContainer(s side) bool {
	if s.isNil {
		return true
	}
	return s.v.IsValid() && (s.v.Kind() == reflect.Map || s.v.Kind() == reflect.Slice) && s.v.Len() == 0
}

// positionSource is the Source of the provider at position i of kind.
func positionSource(kind SourceKind, i int) Source {
	return Source{Kind: kind, Name: strconv.Itoa(i)}
}

// errProvenanceFunc refuses WithProvenance on a reloader whose loader is the
// caller's: only the package's own pipeline records origins.
var errProvenanceFunc = errors.New("config: WithProvenance needs NewReloader's pipeline; NewReloaderFunc's loader is the caller's")
