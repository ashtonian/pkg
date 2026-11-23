package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// CheckKey checks one concrete key with all other fields at their declared
// and WithDefaults defaults. It decodes with Load's hooks and weak typing,
// then runs its validator and T.Validate. Unknown keys — the key, or a key
// inside its value that no field reads — return *KeyErrors matching ErrKey,
// as does a key that does not parse or is not concrete, its message saying
// which; invalid values match ErrValidation. A validate tag the default
// validator cannot apply to its field, such as dive on an int, is the
// declaration's fault and returns ErrSchema with no *KeyError. Keys use
// ParseKey's grammar, accepting field aliases and case folding (see
// CanonicalKey). A list index must exist in the defaults or append
// immediately after them; pass a whole list to create several new elements.
// Wildcards are not concrete keys.
//
// value sets key as a source above the defaults would. A map[string]any, as
// a file or JSON decodes to, merges into what the defaults hold there key by
// key. Any other value replaces it: a struct, and a typed map such as
// map[string]int, are the whole section or map, as WithDefaults sets them;
// nil sets the zero value. An index edits that element of the defaults'
// list.
//
// Only WithDefaults, WithDecodeHook, WithValidator and WithSkipValidation
// affect this check. It reads no provider, file or environment and runs no
// secret populator, post-load or unknown-input handler. Rules relating fields
// judge the candidate against defaults; the final Load still validates the
// combined sources. No check can establish that arbitrary combinations of
// individually valid entries are valid together.
func CheckKey[T any](ctx context.Context, key string, value any, opts ...Option) error {
	_, err := DecodeKey[T](ctx, key, value, opts...)
	return err
}

// DecodeKey returns the configuration CheckKey validates: one key set over
// defaults, with no source reads or post-load stages. A typed section, of
// WithDefaults or of value, holds its koanf:"-" fields as in a load. On any error it returns
// nil. The returned configuration and its collections are caller-owned.
func DecodeKey[T any](ctx context.Context, key string, value any, opts ...Option) (*T, error) {
	if err := ended(ctx); err != nil {
		return nil, err
	}
	c, err := newKeyChecker(reflect.TypeFor[T](), buildOptions(opts))
	if err != nil {
		return nil, err
	}
	result, edits, refused, err := c.apply(ctx, c.defaultsTree(), c.unkeyed, map[string]any{key: value})
	switch {
	case err != nil:
		return nil, err
	case refused == nil:
		return result.(*T), nil
	case len(refused.Refused) > 0:
		keys := make([]*KeyError, len(refused.Refused))
		for i := range refused.Refused {
			keys[i] = &refused.Refused[i]
		}
		return nil, joinKeyErrors(keys)
	}
	return nil, newKeyError(c.schema, edits[0].path, false, refused.Err)
}

// CanonicalKey returns key spelled the one way this package prints it, for
// T: each field by its key, not an alias tag's former name or another letter
// case; a map entry quoted, by the text of the key a load reads it as
// (ports["0x50"] is ports["80"], timeouts["60s"] is timeouts["1m0s"]); and a
// key beneath an interface, or one a remain field collects, as a field
// wherever it can be one, what lies beneath the latter spelled by the
// field's element type. FieldFor resolves every key it returns. A settings
// store keys its rows by it, so two spellings of one key are one row. It
// reads an entry's key as a load with no options does: a WithDecodeHook that
// reads a key type from other text is not consulted, so a key naming an
// entry by such text is refused. A key that does not parse, is not concrete
// or names nothing config reads returns an Unknown *KeyError matching
// ErrKey, whose message says which. It reads no value and no validate tag:
// whether a value is valid for the key is CheckKey's to say. A declaration a
// load refuses for anything but its validate tags, such as a misspelt tag
// key, matches ErrSchema.
func CanonicalKey[T any](key string) (string, error) {
	schema, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return "", err
	}
	p, _, _, ke := resolveKey(schema, key)
	if ke != nil {
		return "", ke
	}
	return p.String(), nil
}

// DecodeEdits returns the configuration a batch of edits makes of a
// baseline: each key of edits set to its value as DecodeKey sets one, over
// baseline — over the defaults when baseline is nil — and the result decoded
// and validated once, so a rule relating two edited fields judges the two
// edits together. It is the check an atomic settings writer runs before it
// commits a batch; persisting, authorizing and ordering commits stay the
// writer's.
//
// Keys and values follow DecodeKey: a map[string]any merges into what is
// beneath it key by key and anything else replaces it; nil sets the zero
// value; an index sets one element of the list beneath or appends one
// immediately after its last. Edits apply in key order, an index by its
// number and a key before the keys beneath it: one batch can append several
// elements, and a key beneath another sets what it names in the value the
// other set, a map[string]any merging into that value as into a baseline's.
//
// The batch is refused whole, with an *EditsError, when any key is refused,
// every refused key in it: a key that does not parse, holds [*] or names
// nothing config reads, or a key inside a value that no field reads
// (Unknown, ErrKey); one value set under two spellings; an index beyond the
// next element; a key beneath one set to a value that cannot hold it, as
// primary set to 5 cannot hold primary.port; keys selecting both elements
// and fields of one value. When every key applies but the candidate does not
// decode or validate, the *EditsError names the batch's keys and holds the
// cause.
//
// What it checks is the batch over baseline and nothing else: the same
// decode hooks, validator and Validate method as Load, and no source.
// Whether the next load makes the same configuration depends on what that
// load reads — a source beneath the batch that changed, a row the store
// deleted, other options — so a writer whose batch is stored entries checks
// the load itself (see the settings-store recipe).
//
// baseline is only read, and the result shares nothing with it or with
// edits. The candidate is decoded afresh from baseline's keys, so an
// unexported field is zero in it; an exported koanf:"-" field, which no key
// reaches, holds a copy of baseline's, as a typed section's does in a load,
// unless an edit replaces what holds it. A baseline holding a value that
// does not marshal to text is an error of its own, neither ErrKey nor
// ErrValidation. WithDefaults applies only when baseline is nil; otherwise
// the options apply as they do to CheckKey: no source is read and no secret
// populator, post-load hook or unknown-input handler runs. On any error the
// result is nil.
func DecodeEdits[T any](ctx context.Context, baseline *T, edits map[string]any, opts ...Option) (*T, error) {
	if err := ended(ctx); err != nil {
		return nil, err
	}
	c, err := newKeyChecker(reflect.TypeFor[T](), buildOptions(opts))
	if err != nil {
		return nil, err
	}
	base, unkeyed := c.defaultsTree(), c.unkeyed
	if baseline != nil {
		if base, unkeyed, err = c.baselineTree(reflect.ValueOf(baseline).Elem()); err != nil {
			return nil, err
		}
	}
	result, _, refused, err := c.apply(ctx, base, unkeyed, edits)
	switch {
	case err != nil:
		return nil, err
	case refused != nil:
		return nil, refused
	}
	return result.(*T), nil
}

// EditsError is a batch of edits DecodeEdits refuses. Refused holds every
// key it refused, in key order (pool[2] before pool[10]): each an Unknown *KeyError (ErrKey) or one
// whose message says why the key cannot be set (ErrValidation), as
// DecodeKey refuses the key alone. When every key applies but the
// configuration the batch makes does not decode or validate, Keys are the
// batch's keys as diagnostics may show them, sorted — none for an empty
// batch over a configuration already invalid — and Err is the decoder's or
// validator's cause (ErrValidation), for trusted diagnostics.
// The error wraps each refused *KeyError and Err, so errors.Is matches the
// category of each and errors.As reaches the first refused key. Error names
// keys and never a value. A validate tag the default validator cannot apply
// is no batch's fault: DecodeEdits returns that ErrSchema as it is, not in
// an EditsError.
type EditsError struct {
	Refused []KeyError `json:"refused,omitempty"`
	Keys    []string   `json:"keys,omitempty"`
	Err     error      `json:"-"`
}

func (e *EditsError) Error() string {
	var b strings.Builder
	b.WriteString("config: edits refused")
	for i, r := range e.Refused {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(strings.TrimPrefix(r.Error(), "config: "))
	}
	if e.Err != nil {
		if len(e.Refused) > 0 {
			b.WriteString("; ")
		} else {
			b.WriteString(": ")
		}
		if len(e.Keys) == 0 {
			b.WriteString("the configuration is invalid with no key edited")
		} else {
			b.WriteString("the configuration of keys " + strings.Join(e.Keys, ", ") + " is invalid")
		}
	}
	return b.String()
}

func (e *EditsError) Unwrap() []error {
	errs := make([]error, 0, len(e.Refused)+1)
	for i := range e.Refused {
		errs = append(errs, &e.Refused[i])
	}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}

type keyChecker struct {
	root     reflect.Type
	schema   *schemaNode
	defaults map[string]any
	// unkeyed is the koanf:"-" fields of the defaults' typed sections,
	// which a candidate holds as a load would.
	unkeyed unkeyedSections
	options *options
}

func newKeyChecker(t reflect.Type, o *options) (*keyChecker, error) {
	root, err := loadSchema(t, o)
	if err != nil {
		return nil, err
	}
	l := &loader{k: koanf.New("."), root: t, schema: root}
	if _, err := l.addSchemaDefaults(o); err != nil {
		return nil, err
	}
	if err := l.addFlat("WithDefaults", o.defaults, constant(Source{Kind: SourceDefaults})); err != nil {
		return nil, err
	}
	return &keyChecker{root: t, schema: root, defaults: l.k.Raw(), unkeyed: l.unkeyed, options: o}, nil
}

// apply sets entries over base, which it changes, and decodes and validates
// the result, holding the koanf:"-" fields of below, base's, and of the
// typed sections entries set. A refused key, or a candidate that does not
// decode or validate, is the *EditsError; edits are the entries that
// applied, those the candidate was decoded from. err is why ctx ended, or a
// validate tag the validator cannot apply (ErrSchema), which is the
// declaration's fault and no key's.
func (c *keyChecker) apply(ctx context.Context, base map[string]any, below unkeyedSections, entries map[string]any) (any, []edit, *EditsError, error) {
	edits, refused := prepareEdits(c.schema, entries)
	for _, e := range edits {
		for _, u := range e.unknown {
			refused = append(refused, unknownKeyError(u))
		}
	}
	tree, unset := setEdits(c.schema, func() map[string]any { return base }, edits)
	if refused = append(refused, unset...); len(refused) > 0 {
		return nil, nil, &EditsError{Refused: keyErrorValues(refused)}, nil
	}
	unkeyed := below.over(tree, editsUnkeyed(edits)) // before mergeTrees settles tree's whole maps
	mergeTrees(tree, base)
	result, unknown, err := c.decode(ctx, base, unkeyed)
	if ctxErr := ended(ctx); ctxErr != nil && (err != nil || unknown != nil) {
		return nil, nil, nil, ctxErr
	}
	if errors.Is(err, ErrSchema) {
		return nil, nil, nil, err
	}
	if err == nil && unknown == nil {
		return result, edits, nil, nil
	}
	out := &EditsError{}
	if unknown != nil {
		for _, u := range unknown.Keys {
			refused = append(refused, unknownKeyError(u))
		}
		out.Refused = keyErrorValues(refused)
	}
	if err != nil {
		for _, e := range edits {
			out.Keys = append(out.Keys, e.safe)
		}
		slices.Sort(out.Keys)
		out.Keys, out.Err = slices.Compact(out.Keys), err
	}
	return nil, edits, out, nil
}

func keyErrorValues(errs []*KeyError) []KeyError {
	out := make([]KeyError, len(errs))
	for i, e := range sortKeyErrors(errs) {
		out[i] = *e
	}
	return out
}

// defaultsTree is a copy of the defaults' tree.
func (c *keyChecker) defaultsTree() map[string]any {
	return deepcopy.Value(reflect.ValueOf(c.defaults)).Interface().(map[string]any)
}

// baselineTree is cfg's tree, as a source carrying it whole would merge:
// its keys, every map of them merging by key, and the koanf:"-" fields of
// cfg and of each section in it.
func (c *keyChecker) baselineTree(cfg reflect.Value) (map[string]any, unkeyedSections, error) {
	w := keyWalk{schema: c.schema}
	tree := w.section(c.root, deepcopy.Value(cfg), nil)
	if len(w.noText) > 0 {
		return nil, nil, fmt.Errorf("config: the baseline holds values that do not marshal to text: %s", keyList(w.noText))
	}
	base := settled(tree).(map[string]any)
	var unkeyed unkeyedSections
	unkeyed.merge(base, w.unkeyed)
	return base, unkeyed, nil
}

// decode decodes tree, a tree as the merge holds it, as decodeTree does, and
// validates the result as a load does. The keys of tree no field reads are
// unknown, apart from err. result is the decoded config, a *T, valid only
// when neither err nor unknown is set.
func (c *keyChecker) decode(ctx context.Context, tree map[string]any, unkeyed unkeyedSections) (result any, unknown *UnknownKeysError, err error) {
	out, md, err := c.decodeTree(tree, unkeyed)
	if err != nil {
		return nil, nil, err
	}
	validation := c.options.validateConfig(ctx, out.Interface())
	if len(md.Unused) > 0 {
		unknown = &UnknownKeysError{Type: c.root}
		for _, name := range md.Unused {
			unknown.Keys = append(unknown.Keys, unusedKey(c.schema, tree, name))
		}
	}
	return out.Interface(), unknown, validation
}

// decodeTree decodes tree, a tree as the merge holds it, into a new *T with
// the options' hooks, filling the element defaults and setting the
// koanf:"-" fields unkeyed holds, as a load decodes its merge. md records
// the keys of tree no field read. A failure to decode is ErrValidation.
func (c *keyChecker) decodeTree(tree map[string]any, unkeyed unkeyedSections) (out reflect.Value, md mapstructure.Metadata, err error) {
	if err := newElementDefaults(c.options, false).fill(c.root, tree); err != nil {
		return reflect.Value{}, md, err
	}
	out = reflect.New(c.root)
	md, err = c.options.decodeInput(c.schema, tree, out.Interface(), nil)
	if err != nil {
		return reflect.Value{}, md, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	unkeyed.apply(c.schema, out.Elem())
	return out, md, nil
}
