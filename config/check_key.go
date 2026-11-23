package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"

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
	if e.Unknown {
		return fmt.Sprintf("config: unknown key %s", e.Key)
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
	e, err := resolveEdit(c.schema, key, value)
	if err != nil {
		return nil, err
	}
	tree, err := c.edited([]edit{e})
	if err != nil {
		return nil, err
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

// edited is a copy of the defaults with edits set over them.
func (c *keyChecker) edited(edits []edit) (map[string]any, error) {
	base := deepcopy.Value(reflect.ValueOf(c.defaults)).Interface().(map[string]any)
	tree, err := editTree(c.schema, func() map[string]any { return base }, edits)
	if err != nil {
		return nil, err
	}
	return base, mergeTrees(tree, base)
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
