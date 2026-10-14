package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

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
	SourceUnknown  SourceKind = "unknown"           // decoding reshaped the input: no source can be named
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
	// Input is the source whose value the merge kept for the key.
	Input Source `json:"input"`
	// Transforms are the stages that changed the decoded value afterwards,
	// in order: the secret populator, then post-load hooks by position. A
	// stage that wrote the value the key already held is not listed.
	Transforms []Source `json:"transforms,omitempty"`
	// Exact is false when decoding reshaped the input so that the value
	// cannot be traced to one source: Input is then the source of what the
	// value was decoded from, or unknown.
	Exact bool `json:"exact"`
}

// OriginProvider is a map provider that names a source for each key it
// returns, read once with the load's context: kongprovider names the flag
// behind each value. Annotations are keyed in the package's key grammar; an
// annotation on a section covers the keys beneath it, and a key with none is
// attributed to the provider. An annotation naming a key the data does not
// hold fails the load with [ErrSource]. Only provenance reads the
// annotations; it is used for providers loaded with a nil parser.
type OriginProvider interface {
	koanf.Provider
	ReadWithOrigins(ctx context.Context) (map[string]any, map[string]Source, error)
}

// LoadSnapshot runs [Load] and records where every key's value came from:
// the snapshot's Provenance holds an [Origin] for each key, and its
// Generation is 0. Recording costs a copy and a comparison of the decoded
// config around the secret populator and each post-load hook; Load records
// nothing.
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
// beneath it is the new source's.
type originNode struct {
	src      Source
	children map[string]*originNode // non-nil exactly when the value is a map
}

func newOriginRoot() *originNode {
	return &originNode{children: map[string]*originNode{}}
}

// labeler names the source of the value at raw path in one source's tree.
type labeler func(path []string) Source

func constant(src Source) labeler {
	return func([]string) Source { return src }
}

func (n *originNode) merge(incoming map[string]any, label labeler, path []string) {
	for key, val := range incoming {
		p := append(slices.Clip(path), key)
		child := n.children[key]
		if in, isMap := val.(map[string]any); isMap && child != nil && child.children != nil {
			child.merge(in, label, p)
			continue
		}
		n.children[key] = newOrigin(val, label, p)
	}
}

func newOrigin(val any, label labeler, path []string) *originNode {
	n := &originNode{src: label(path)}
	if m, ok := val.(map[string]any); ok {
		n.children = make(map[string]*originNode, len(m))
		for key, child := range m {
			n.children[key] = newOrigin(child, label, append(slices.Clip(path), key))
		}
	}
	return n
}

// fill attributes the keys merged holds that no source set — element
// defaults, filled after the merge — to the element's tag.
func (n *originNode) fill(merged map[string]any, path []string) {
	for key, val := range merged {
		child := n.children[key]
		if child == nil {
			n.children[key] = newOrigin(val, constant(Source{Kind: SourceTag}), append(slices.Clip(path), key))
			continue
		}
		if m, ok := val.(map[string]any); ok && child.children != nil {
			child.fill(m, append(slices.Clip(path), key))
		}
	}
}

// uniform is the one source everything at and beneath n came from, or false
// when several did.
func (n *originNode) uniform() (Source, bool) {
	if n.children == nil {
		return n.src, true
	}
	var src Source
	first := true
	for _, child := range n.children {
		s, ok := child.uniform()
		if !ok || !first && s != src {
			return Source{}, false
		}
		src, first = s, false
	}
	if first {
		return n.src, true // an empty map: its own source
	}
	return src, true
}

// envLabeler attributes each value of the environment layer to the variable
// that set it; flat maps each flat key to its variable.
func envLabeler(prefix string, flat map[string]string) labeler {
	return func(path []string) Source {
		if name, ok := flat[strings.Join(path, ".")]; ok {
			return Source{Kind: SourceEnv, Name: name}
		}
		return Source{Kind: SourceEnv, Name: prefix + "*"}
	}
}

// annotationLabeler attributes values by an OriginProvider's annotations,
// keyed by raw path; a key with none takes its nearest annotated section's,
// then fallback.
func annotationLabeler(root reflect.Type, tree map[string]any, annotations map[string]Source, fallback Source) (labeler, error) {
	byPath := make(map[string]Source, len(annotations))
	for key, src := range annotations {
		p, err := parseKeyPath(key)
		if err != nil {
			return nil, err
		}
		raw := make([]string, len(p))
		for i, s := range p {
			if s.kind != segField && s.kind != segKey {
				return nil, fmt.Errorf("annotation %s: annotate a list as a whole, not an element", key)
			}
			raw[i] = s.name
		}
		if !rawPresent(tree, raw) {
			return nil, fmt.Errorf("annotation %s names a key the provider did not return", key)
		}
		byPath[strings.Join(raw, "\x00")] = src
	}
	return func(path []string) Source {
		for i := len(path); i > 0; i-- {
			if src, ok := byPath[strings.Join(path[:i], "\x00")]; ok {
				return src
			}
		}
		return fallback
	}, nil
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

// originRecorder records provenance for one load: the merge's origins, and the
// stages that changed the decoded config.
type originRecorder struct {
	schema     *schemaNode
	tree       *originNode
	transforms []transform
	before     []flatEntry
}

// transform is one stage's change: the key it changed, or with subtree
// everything beneath it.
type transform struct {
	key     string
	ord     []ordSeg
	subtree bool
	src     Source
}

// snapshot takes a private copy of cfg's keys before a stage runs.
func (r *originRecorder) snapshot(cfg reflect.Value) error {
	entries, err := flatten(r.schema, deepCopy(cfg))
	r.before = entries
	return err
}

// changed records what the stage src changed since snapshot.
func (r *originRecorder) changed(cfg reflect.Value, src Source) error {
	after, err := flatten(r.schema, cfg)
	if err != nil {
		return err
	}
	for _, c := range compareEntries(r.before, after) {
		r.transforms = append(r.transforms, transform{key: c.Key, ord: c.ord, subtree: c.subtree, src: src})
	}
	r.before = nil
	return nil
}

// origins attributes every value of the decoded config cfg: each key that
// holds a value rather than keys of its own.
func (r *originRecorder) origins(cfg reflect.Value) (map[string]Origin, error) {
	entries, err := flatten(r.schema, cfg)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Origin, len(entries))
	for _, e := range entries {
		if e.kind == entryContainer && !emptyContainer(e.side) || e.kind == entryDynamic && e.node != nil && e.node.composite() {
			continue // its values have origins of their own
		}
		o := r.tree.originOf(e)
		for _, t := range r.transforms {
			if t.key == e.path.String() || t.subtree && within(e.ord, t.ord) {
				o.Transforms = append(o.Transforms, t.src)
			}
		}
		out[e.path.String()] = o
	}
	return out, nil
}

// emptyContainer reports a struct, map or list with nothing beneath it to
// attribute: nil, or a map or list with no entries.
func emptyContainer(s side) bool {
	if s.isNil {
		return true
	}
	return s.v.IsValid() && (s.v.Kind() == reflect.Map || s.v.Kind() == reflect.Slice) && s.v.Len() == 0
}

// originOf finds the input behind entry e by its key's path through the
// merged tree.
func (n *originNode) originOf(e flatEntry) Origin {
	node := n
	for _, s := range e.path {
		if node.children == nil {
			// A value no map: a list or a scalar the key lies within,
			// decoded whole from what that source set.
			return Origin{Input: node.src, Exact: true}
		}
		if s.kind == segIndex {
			if s.index == 0 {
				continue // weak decoding takes a map as a list's one element
			}
			return reshaped(node)
		}
		child := node.children[s.name]
		if child == nil {
			if e.side.isNil || !e.side.v.IsValid() || e.side.v.IsZero() {
				return Origin{Input: Source{Kind: SourceZero}, Exact: true}
			}
			return Origin{Input: Source{Kind: SourceUnknown}} // a decoder made it
		}
		node = child
	}
	if node.children == nil {
		return Origin{Input: node.src, Exact: true}
	}
	// A map decoded whole, or compared whole as sensitive.
	if src, ok := node.uniform(); ok {
		return Origin{Input: src, Exact: true}
	}
	return Origin{Input: Source{Kind: SourceMixed}, Exact: true}
}

func reshaped(n *originNode) Origin {
	if src, ok := n.uniform(); ok {
		return Origin{Input: src}
	}
	return Origin{Input: Source{Kind: SourceUnknown}}
}

// positionSource is the Source of the provider at position i of kind.
func positionSource(kind SourceKind, i int) Source {
	return Source{Kind: kind, Name: strconv.Itoa(i)}
}

// errProvenanceFunc refuses WithProvenance on a reloader whose loader is the
// caller's: only the package's own pipeline records origins.
var errProvenanceFunc = errors.New("config: WithProvenance needs NewReloader's pipeline; NewReloaderFunc's loader is the caller's")
