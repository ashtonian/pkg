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
var configView = func() *gowalker.Engine {
	e, err := gowalker.New(
		gowalker.WithFieldResolver(configField),
		gowalker.WithCaseFoldNames(),
		gowalker.WithAtomicType(func(t reflect.Type) bool { return opaque(derefType(t)) }),
	)
	if err != nil {
		panic(err) // all options are fixed and valid
	}
	return e
}()

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
	var collision *gowalker.NameConflict
	if errors.As(err, &collision) {
		a, b := collision.First, collision.Second
		aKey := strings.EqualFold(a.Name(), collision.Name)
		bKey := strings.EqualFold(b.Name(), collision.Name)
		var detail string
		switch {
		case aKey && bKey:
			detail = fmt.Sprintf("fields %s and %s of %s both have key %q (ignoring case)", a.GoField().Name, b.GoField().Name, typeText(collision.Owner), collision.Name)
		case bKey:
			detail = fmt.Sprintf("field %s of %s has key %q, which field %s names as an alias", b.GoField().Name, typeText(collision.Owner), collision.Name, a.GoField().Name)
		case aKey:
			detail = fmt.Sprintf("field %s of %s has alias %q, which is the key of field %s", b.GoField().Name, typeText(collision.Owner), collision.Name, a.GoField().Name)
		default:
			detail = fmt.Sprintf("fields %s and %s of %s both have alias %q (ignoring case)", a.GoField().Name, b.GoField().Name, typeText(collision.Owner), collision.Name)
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrSchema, detail, err)
	}
	// Resolver errors contain config's tag diagnostics; the shared error's
	// default text intentionally omits field names and tag values.
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
