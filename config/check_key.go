package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// KeyError distinguishes an unknown key (Unknown, ErrKey) from a known key
// with an invalid value (ErrValidation). Error contains no input value;
// Err preserves the decoder or validator's cause for trusted diagnostics.
type KeyError struct {
	Key     string `json:"key"`
	Unknown bool   `json:"unknown,omitempty"`
	Err     error  `json:"-"`
}

func (e *KeyError) Error() string {
	var problem keyProblem
	switch {
	case e.Unknown:
		return fmt.Sprintf("config: unknown key %s", e.Key)
	case errors.As(e.Err, &problem):
		return fmt.Sprintf("config: key %s: %s", e.Key, problem)
	}
	return fmt.Sprintf("config: invalid value for key %s", e.Key)
}

func (e *KeyError) Unwrap() error { return e.Err }

func (e *KeyError) Is(target error) bool {
	return target == ErrKey && e.Unknown || target == ErrValidation && !e.Unknown
}

// CheckKey checks one concrete key with all other fields at their declared
// and WithDefaults defaults. It decodes with Load's hooks and weak typing,
// then runs its validator and T.Validate. Unknown keys return a *KeyError
// matching ErrKey; invalid values match ErrValidation. Keys use ParseKey's
// grammar, accepting field aliases and case folding. A list index must exist
// in the defaults or append immediately after them; pass a whole list to
// create several new elements. Wildcards are not concrete keys.
//
// value sets key as a source above the defaults would: a map merges into
// what the defaults hold there key by key, typed or not, and any other value,
// a struct or a typed map included, replaces it. An index edits that element
// of the defaults' list.
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
// defaults, with no source reads or post-load stages. On any error it returns
// nil. The returned configuration and its collections are caller-owned.
func DecodeKey[T any](ctx context.Context, key string, value any, opts ...Option) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := newKeyChecker(reflect.TypeFor[T](), buildOptions(opts))
	if err != nil {
		return nil, err
	}
	result, err := c.check(ctx, key, value)
	if err != nil {
		return nil, err
	}
	return result.(*T), nil
}

// DecodeEdits returns the configuration a batch of edits makes of a
// baseline: each key of edits set to its value as DecodeKey sets one, over
// baseline — over the defaults when baseline is nil — and the result decoded
// and validated once, so a rule relating two edited fields judges the two
// edits together. It is the check an atomic settings writer runs before it
// commits a batch; persisting, authorizing and ordering commits stay the
// writer's.
//
// Keys and values follow DecodeKey: a map merges into what is beneath it key
// by key and anything else replaces it; an index sets one element of the
// list beneath or appends one immediately after its last. Edits apply in key
// order, an index by its number and a key before the keys beneath it: one
// batch can append several elements, and a key beneath another sets what it
// names in the value the other set. The batch is refused whole when any key
// is: a key naming nothing config reads is an Unknown *KeyError (ErrKey);
// one value set under two spellings and an index beyond the next element are
// *KeyErrors matching ErrValidation. Every refused key is reported, joined in
// key order. A candidate that does not decode or validate is an *EditsError.
//
// baseline is only read, and the result shares nothing with it or with
// edits. The candidate is decoded afresh from baseline's keys, so a field
// config does not decode (unexported, or koanf:"-") is zero in it.
// WithDefaults applies only when baseline is nil; otherwise the options
// apply as they do to CheckKey: no source is read and no secret populator,
// post-load hook or unknown-input handler runs. On any error the result is
// nil.
func DecodeEdits[T any](ctx context.Context, baseline *T, edits map[string]any, opts ...Option) (*T, error) {
	if err := ended(ctx); err != nil {
		return nil, err
	}
	c, err := newKeyChecker(reflect.TypeFor[T](), buildOptions(opts))
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if baseline == nil {
		base = c.defaultsTree()
	} else if base, err = c.baselineTree(reflect.ValueOf(baseline).Elem()); err != nil {
		return nil, err
	}
	resolved, failed := resolveEdits(c.schema, edits)
	if len(failed) > 0 {
		return nil, joinKeyErrors(failed)
	}
	keys := make([]string, len(resolved))
	for i, e := range resolved {
		keys[i] = e.safe
	}
	tree, failed := c.edited(base, resolved)
	if len(failed) > 0 {
		return nil, joinKeyErrors(failed)
	}
	result, err := c.decode(ctx, tree)
	if err != nil {
		if ctxErr := ended(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		slices.Sort(keys)
		return nil, &EditsError{Keys: slices.Compact(keys), Err: err}
	}
	return result.(*T), nil
}

// EditsError is a batch of edits whose configuration does not decode or
// validate. Keys are the batch's keys as diagnostics may show them, sorted.
// Error names them and no value; Err keeps the decoder's or validator's
// cause for trusted diagnostics. It matches ErrValidation.
type EditsError struct {
	Keys []string `json:"keys"`
	Err  error    `json:"-"`
}

func (e *EditsError) Error() string {
	return "config: invalid edits to keys " + strings.Join(e.Keys, ", ")
}

func (e *EditsError) Unwrap() error { return e.Err }

func (e *EditsError) Is(target error) bool { return target == ErrValidation }

type keyChecker struct {
	root     reflect.Type
	schema   *schemaNode
	defaults map[string]any
	options  *options
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
	return &keyChecker{root: t, schema: root, defaults: l.k.Raw(), options: o}, nil
}

func (c *keyChecker) check(ctx context.Context, key string, value any) (any, error) {
	if err := ended(ctx); err != nil {
		return nil, err
	}
	e, ke := resolveEdit(c.schema, key, value)
	if ke != nil {
		return nil, ke
	}
	tree, failed := c.edited(c.defaultsTree(), []edit{e})
	if len(failed) > 0 {
		return nil, failed[0]
	}
	result, err := c.decode(ctx, tree)
	if err != nil {
		if ctxErr := ended(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &KeyError{Key: e.safe, Err: err}
	}
	return result, nil
}

// defaultsTree is a copy of the defaults' tree.
func (c *keyChecker) defaultsTree() map[string]any {
	return deepcopy.Value(reflect.ValueOf(c.defaults)).Interface().(map[string]any)
}

// baselineTree is cfg's tree, as a source carrying it whole would merge:
// its keys, every map of them merging by key.
func (c *keyChecker) baselineTree(cfg reflect.Value) (map[string]any, error) {
	w := keyWalk{schema: c.schema}
	tree := w.section(c.root, deepcopy.Value(cfg), nil)
	if len(w.noText) > 0 {
		return nil, fmt.Errorf("%w: values that do not marshal to text: %s", ErrValidation, strings.Join(w.noText, ", "))
	}
	return settled(tree).(map[string]any), nil
}

// edited is base, which it changes, with edits set over it. An edit it
// cannot set is left out, in failed.
func (c *keyChecker) edited(base map[string]any, edits []edit) (map[string]any, []*KeyError) {
	tree, failed := editTree(c.schema, func() map[string]any { return base }, edits)
	mergeTrees(tree, base)
	return base, failed
}

// decode decodes tree, a tree as the merge holds it, with the options' hooks
// and validates the result.
func (c *keyChecker) decode(ctx context.Context, tree map[string]any) (any, error) {
	if err := newElementDefaults(c.options, false).fill(c.root, tree); err != nil {
		return nil, err
	}
	out := reflect.New(c.root)
	md, err := c.options.decodeInput(c.schema, tree, out.Interface(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	validation := c.options.validateConfig(ctx, out.Interface())
	if len(md.Unused) > 0 {
		unknown := &UnknownKeysError{Type: c.root}
		for _, name := range md.Unused {
			unknown.Keys = append(unknown.Keys, unusedKey(c.schema, tree, name))
		}
		return nil, errors.Join(validation, unknown)
	}
	if validation != nil {
		return nil, validation
	}
	return out.Interface(), nil
}
