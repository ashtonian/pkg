package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// Naming and atomicity belong to config; discovery, collisions and physical
// routes belong to the shared engine. Neither cache retains configuration values.
var configView = gowalker.MustNew(
	gowalker.WithFieldResolver(configField),
	gowalker.WithCaseFoldNames(),
	gowalker.WithAtomicType(func(t reflect.Type) bool { return opaque(derefType(t)) }),
)

func configField(sf reflect.StructField) (gowalker.FieldRule, error) {
	if unexportedSquashedPointer(sf) {
		return gowalker.FieldRule{}, fmt.Errorf("%w: field %s squashes a pointer to an unexported struct, which decoding cannot allocate; embed the struct itself, or export its type", ErrSchema, sf.Name)
	}
	key, skip := fieldKey(sf)
	if skip {
		return gowalker.FieldRule{Skip: true}, nil
	}
	aliases := aliasNames(sf.Tag)
	if key == "" {
		if _, ok := sf.Tag.Lookup("alias"); ok {
			return gowalker.FieldRule{}, fmt.Errorf("%w: field %s is squashed, so it has no key an alias could stand for", ErrSchema, sf.Name)
		}
		return gowalker.FieldRule{Inline: isBranch(derefType(sf.Type)), Skip: !isBranch(derefType(sf.Type))}, nil
	}
	if !validFieldKey(key) {
		return gowalker.FieldRule{}, fmt.Errorf("%w: field %s has key %q, which holds a character keys use for structure", ErrSchema, sf.Name, key)
	}
	for i, a := range aliases {
		if !validFieldKey(a) || strings.EqualFold(a, key) {
			return gowalker.FieldRule{}, fmt.Errorf("%w: field %s has alias %q; an alias is another key, of the key grammar", ErrSchema, sf.Name, a)
		}
		for _, previous := range aliases[:i] {
			if strings.EqualFold(previous, a) {
				return gowalker.FieldRule{}, fmt.Errorf("%w: field %s repeats alias %q (ignoring case)", ErrSchema, sf.Name, a)
			}
		}
	}
	return gowalker.FieldRule{Name: key, Aliases: aliases}, nil
}

func compileView(t reflect.Type) (*gowalker.Schema, error) {
	s, err := configView.CompileType(derefType(t))
	if err == nil {
		return s, nil
	}
	// A name conflict names its type and both fields; matching is case-folded.
	var collision *gowalker.NameConflict
	if errors.As(err, &collision) {
		return nil, fmt.Errorf("%w: %w (ignoring case)", ErrSchema, collision)
	}
	// A resolver error is config's own tag diagnostic; report it with its type
	// rather than inside gowalker's wrapper.
	var failure *gowalker.Error
	if errors.As(err, &failure) && errors.Is(failure.Cause, ErrSchema) {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchema, typeText(failure.Type), failure.Cause)
	}
	return nil, fmt.Errorf("%w: %s: %w", ErrSchema, typeText(t), err)
}

func fieldSensitive(f *gowalker.Field) bool {
	if taggedSensitive(f.GoField().Tag) {
		return true
	}
	for _, parent := range f.InlineParents() {
		if taggedSensitive(parent.Tag) {
			return true
		}
	}
	return false
}

func shapeBase(s *gowalker.Shape) *gowalker.Shape {
	for range maxPointerDepth {
		if s.Kind() != reflect.Pointer {
			break
		}
		s = s.Elem()
	}
	return s
}

func (n *schemaNode) lookup(name string) (*schemaField, bool) {
	f, ok := n.view.Root().Lookup(name)
	if !ok {
		return nil, false
	}
	field := n.byKey[f.Name()]
	return field, !strings.EqualFold(name, field.key)
}

func (f *schemaField) value(v reflect.Value) (reflect.Value, error) {
	got, err := f.access.GetValue(v)
	if err != nil {
		return reflect.Value{}, err
	}
	if got.State == gowalker.Missing {
		return reflect.Value{}, gowalker.ErrMissing
	}
	return got.Reflect(), nil
}
