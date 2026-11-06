package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/knadh/koanf/v2"
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
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	l := &loader{k: koanf.New("."), root: t, schema: root}
	if err := l.addDeclaredDefaults(true); err != nil {
		return nil, err
	}
	if err := l.addFlat("WithDefaults", o.defaults, constant(Source{Kind: SourceDefaults})); err != nil {
		return nil, err
	}
	return &keyChecker{root: t, schema: root, defaults: l.k.Raw(), options: o}, nil
}

func (c *keyChecker) check(ctx context.Context, key string, value any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := parseKeyPath(key)
	if err != nil {
		return nil, &KeyError{Key: RedactText(key), Unknown: true, Err: err}
	}
	p, err = c.canonical(p)
	if err != nil {
		return nil, &KeyError{Key: diagnosticPath(c.schema, p).String(), Unknown: true, Err: err}
	}
	safe := diagnosticPath(c.schema, p).String()
	tree := deepCopy(reflect.ValueOf(c.defaults)).Interface().(map[string]any)
	value = copyInput(value)
	if _, err := setInput(tree, p, value); err != nil {
		return nil, &KeyError{Key: safe, Err: err}
	}
	result, err := c.decode(ctx, tree)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &KeyError{Key: safe, Err: err}
	}
	return result, nil
}

func copyInput(v any) any {
	if v == nil {
		return nil
	}
	return deepCopy(reflect.ValueOf(v)).Interface()
}

func (c *keyChecker) canonical(p keyPath) (keyPath, error) {
	p = slices.Clone(p)
	n := c.schema
	for i := range p {
		if p[i].kind == segAny {
			return p, fmt.Errorf("%w: key must be concrete", ErrKey)
		}
		if n.kind == nodeBranch && p[i].kind == segField {
			if f, _ := n.lookup(p[i].name); f != nil {
				p[i].name = f.key
			}
		}
		_, next, err := n.walkTo(cursor{}, p[i:i+1])
		if err != nil {
			return p, err
		}
		n = next
	}
	return p, nil
}

func (c *keyChecker) decode(ctx context.Context, tree map[string]any) (any, error) {
	l := &loader{root: c.root, schema: c.schema}
	tree, err := l.rewrite("key check", tree)
	if err != nil {
		return nil, err
	}
	fillElementDefaults(c.root, tree)
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

// setInput never creates sparse lists from an untrusted index.
func setInput(in any, path keyPath, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	s := path[0]
	if s.kind == segIndex {
		var list []any
		v := reflect.ValueOf(in)
		if v.IsValid() && (v.Kind() == reflect.Array || v.Kind() == reflect.Slice) {
			list = make([]any, v.Len())
			for i := range list {
				list[i] = v.Index(i).Interface()
			}
		}
		if s.index > len(list) {
			return nil, fmt.Errorf("%w: list index is beyond the next element", ErrValidation)
		}
		if s.index == len(list) {
			list = append(list, nil)
		}
		next, err := setInput(list[s.index], path[1:], value)
		if err != nil {
			return nil, err
		}
		list[s.index] = next
		return list, nil
	}
	m, ok := in.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	next, err := setInput(m[s.name], path[1:], value)
	if err != nil {
		return nil, err
	}
	m[s.name] = next
	return m, nil
}
