package config

import (
	"fmt"
	"reflect"
	"slices"
)

// KeyOf returns the key of the field that field points at inside cfg:
//
//	key, err := config.KeyOf(&cfg, &cfg.DB.MaxConns) // "db.max_conns"
//
// It reads addresses, never values, so the field's contents do not matter,
// and never creates pointer targets: a field behind a nil pointer has no address in
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
	matches, err := MatchFields(cfg, []reflect.Value{field})
	if err != nil {
		return "", err
	}
	if matches[0].Path == "" {
		return "", fmt.Errorf("%w: target is not an addressable config field", ErrKey)
	}
	return matches[0].Path, nil
}

// MatchFields associates reflected targets, such as CLI flag destinations, with
// fields in cfg in one traversal. Results correspond to targets in the same
// order. A result with an empty Path means the target is not a config field:
// invalid/unaddressable values, cfg itself, skipped fields, collection elements
// and values outside cfg do not match. A field behind a nil pointer has no address.
//
// Matching reads addresses, not contents, and does not decode tags. It returns
// ErrSchema for invalid config metadata and ErrKey for nil cfg or ambiguous
// addresses (including shared zero-sized fields). Results and their indexes are
// caller-owned. They describe this call's cfg; call again after replacing pointers
// or targets. No instance addresses are retained in the metadata cache.
func MatchFields[T any](cfg *T, targets []reflect.Value) ([]Field, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrKey)
	}
	root, err := rootMetadata(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	self := visit{ptr: reflect.ValueOf(cfg).Pointer(), typ: reflect.TypeFor[T]()}
	matcher := fieldMatcher{wanted: map[visit]*fieldMatch{}, visiting: map[visit]bool{}}
	slots := make([]*fieldMatch, len(targets))
	for i, target := range targets {
		if !target.IsValid() || !target.CanAddr() {
			continue
		}
		at := visit{ptr: target.Addr().Pointer(), typ: target.Type()}
		if at == self {
			continue
		}
		match := matcher.wanted[at]
		if match == nil {
			match = &fieldMatch{}
			matcher.wanted[at] = match
		}
		slots[i] = match
	}
	if len(matcher.wanted) > 0 {
		matcher.branch(cursor{}, root, reflect.ValueOf(cfg).Elem())
	}
	results := make([]Field, len(targets))
	for i, match := range slots {
		if match == nil || match.count == 0 {
			continue
		}
		if match.count > 1 {
			return nil, fmt.Errorf("%w: target %d matches %d config fields sharing an address", ErrKey, i, match.count)
		}
		results[i] = match.field
		results[i].Index = slices.Clone(match.field.Index)
	}
	return results, nil
}

type fieldMatch struct {
	field Field
	count int
}

type fieldMatcher struct {
	wanted   map[visit]*fieldMatch
	visiting map[visit]bool
}

func (f *fieldMatcher) branch(c cursor, n *schemaNode, v reflect.Value) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	at := visit{ptr: v.Addr().Pointer(), typ: v.Type()}
	if f.visiting[at] {
		return
	}
	f.visiting[at] = true
	defer delete(f.visiting, at)
	for _, sf := range n.fields {
		fv, err := sf.value(v)
		if err != nil {
			continue
		}
		match := f.wanted[visit{ptr: fv.Addr().Pointer(), typ: fv.Type()}]
		if match == nil && sf.node.kind != nodeBranch {
			continue
		}
		next := c.intoField(sf)
		if match != nil {
			match.field = next.owner
			match.count++
		}
		if sf.node.kind == nodeBranch {
			f.branch(next, sf.node, fv)
		}
	}
}
