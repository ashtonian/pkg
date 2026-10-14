package config

import (
	"fmt"
	"reflect"
)

// KeyOf returns the key of the field that field points at inside cfg:
//
//	key, err := config.KeyOf(&cfg, &cfg.DB.MaxConns) // "db.max_conns"
//
// It reads addresses, never values, so the field's contents do not matter,
// and nothing is allocated: a field behind a nil pointer has no address in
// cfg and is an error. A struct, map or list field has a key of its own; an
// element of a map or list does not. A pointer to cfg itself, to memory
// outside it, to a field that is not a key (unexported, koanf:"-",
// koanf:",remain", a squashed struct) or to an address two fields of one type
// share (zero-sized fields) is an ErrKey error. Field must point into cfg —
// not into a copy of it.
func KeyOf[T, F any](cfg *T, field *F) (string, error) {
	if field == nil {
		return "", fmt.Errorf("%w: nil field pointer", ErrKey)
	}
	return KeyOfValue(cfg, reflect.ValueOf(field).Elem())
}

// KeyOfValue is [KeyOf] for a field known by reflection: field is the field
// itself, addressable, as reflect.Value.Field or a flag target gives it.
func KeyOfValue[T any](cfg *T, field reflect.Value) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("%w: nil config", ErrKey)
	}
	if !field.IsValid() || !field.CanAddr() {
		return "", fmt.Errorf("%w: the field is not addressable", ErrKey)
	}
	root, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return "", err
	}
	want := visit{ptr: field.Addr().Pointer(), typ: field.Type()}
	if want == (visit{ptr: reflect.ValueOf(cfg).Pointer(), typ: reflect.TypeFor[T]()}) {
		return "", fmt.Errorf("%w: the config itself has no key", ErrKey)
	}
	f := &addressFinder{want: want, visiting: map[visit]bool{}}
	f.branch(nil, root, reflect.ValueOf(cfg).Elem())
	switch f.found {
	case 0:
		return "", fmt.Errorf("%w: %s at %#x is not a key of %s", ErrKey, typeText(want.typ), want.ptr, typeText(reflect.TypeFor[T]()))
	case 1:
		return f.key.String(), nil
	}
	return "", fmt.Errorf("%w: %d keys of %s share the address of this %s", ErrKey, f.found, typeText(reflect.TypeFor[T]()), typeText(want.typ))
}

// addressFinder walks the fields of a config value for the one at want.
type addressFinder struct {
	want     visit
	key      keyPath
	found    int
	visiting map[visit]bool // the structs on the current path: a pointer cycle ends
}

func (f *addressFinder) branch(p keyPath, n *schemaNode, v reflect.Value) {
	at := visit{ptr: v.Addr().Pointer(), typ: v.Type()}
	if f.visiting[at] {
		return
	}
	f.visiting[at] = true
	defer delete(f.visiting, at)
	for _, sf := range n.fields {
		fv, err := v.FieldByIndexErr(sf.index)
		if err != nil {
			continue // behind a nil pointer: not in cfg
		}
		key := p.field(sf.key)
		if (visit{ptr: fv.Addr().Pointer(), typ: fv.Type()}) == f.want {
			f.key = key
			f.found++
		}
		if sf.node.kind != nodeBranch {
			continue
		}
		for fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				break
			}
			fv = fv.Elem()
		}
		if fv.Kind() == reflect.Struct {
			f.branch(key, sf.node, fv)
		}
	}
}
