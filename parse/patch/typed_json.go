package patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
)

// checkJSONMembers rejects fields encoding/json would silently discard, including
// unknown fields inside typed maps, arrays and embedded structs. Custom JSON
// decoders own their input contract; replacement still uses their encoded result.
func checkJSONMembers(t reflect.Type, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(reflect.New(t).Interface()); err != nil {
		return fmt.Errorf("value does not fit %s: %w", t, err)
	}
	return nil
}

func normalizeMergeValue(ps *PathSchema, op PatchOp, v any, codec core.Codec) (any, error) {
	obj, object := v.(map[string]any)
	if !object || ps.GoType == nil || ps.Relation != nil {
		return normalizeOpValue(ps, op, v, 1, codec)
	}
	return normalizeTypedMerge(ps.GoType, obj, codec, true)
}

// normalizeTypedMerge preserves the set of supplied members. Full replacement
// normalization would fill omitted struct fields with zeros and overwrite siblings.
// present means the existing canonical document necessarily contains this object.
// Nullable objects and map entries can be absent, so their patch must also be a
// complete canonical value when applied to an empty object.
func normalizeTypedMerge(t reflect.Type, obj map[string]any, codec core.Codec, present bool) (any, error) {
	for t.Kind() == reflect.Pointer {
		present = false
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface || t == reflect.TypeFor[json.RawMessage]() {
		// RawMessage is an untyped JSON document, despite implementing the
		// marshaler interfaces; its object shape is preserved verbatim.
		return obj, nil
	}
	if t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]()) ||
		t.Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil, fmt.Errorf("object merge for custom JSON type %s is not supported; use JSON Patch replace", t)
	}

	out := make(map[string]any, len(obj))
	switch t.Kind() {
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("object merge requires string map keys for %s; use JSON Patch replace", t)
		}
		for name, value := range obj {
			if value == nil {
				out[name] = nil // map members can actually disappear
				continue
			}
			n, err := normalizeMergeMember(t.Elem(), value, codec, false)
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", name, err)
			}
			out[name] = n
		}
		return out, nil
	case reflect.Struct:
		fields, err := mergeStructFields(t)
		if err != nil {
			return nil, err
		}
		// Decode once to check the entire input, including unknown nested fields.
		canonical, err := normalizeTyped(t, obj, codec)
		if err != nil {
			return nil, err
		}
		encoded, ok := canonical.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("object merge for %s does not encode an object; use JSON Patch replace", t)
		}
		for name, value := range obj {
			field, ok := fields[name]
			if !ok {
				return nil, fmt.Errorf("unknown or noncanonical JSON member %q in %s", name, t)
			}
			_, emitted := encoded[name]
			if value == nil {
				if emitted {
					return nil, fmt.Errorf("cannot delete JSON member %q: %s would re-emit it; use JSON Patch replace", name, t)
				}
				out[name] = nil
				continue
			}
			if !emitted {
				return nil, fmt.Errorf("JSON member %q is omitted by %s; use null to delete it or JSON Patch replace", name, t)
			}
			// Full typed decoding above already checks scalar field values,
			// including json:",string". Objects need partial-merge validation;
			// arrays replace whole but still need exact fixed-array lengths.
			n := encoded[name]
			if nested, object := value.(map[string]any); object {
				if fieldOmits(field) && mergeMayOmitMap(field.Type, nested) {
					return nil, fmt.Errorf("JSON member %q may become empty and omitted; use JSON Patch replace", name)
				}
				n, err = normalizeMergeMember(field.Type, value, codec, present && !fieldOmits(field))
			} else if _, array := core.AnySlice(value); array {
				_, err = normalizeTyped(field.Type, value, codec)
			}
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", name, err)
			}
			out[name] = n
		}
		if !present {
			for name := range encoded {
				if _, supplied := out[name]; !supplied {
					return nil, fmt.Errorf("object merge into potentially absent %s must supply member %q; use JSON Patch replace", t, name)
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("cannot merge an object into %s", t)
	}
}

func normalizeMergeMember(t reflect.Type, value any, codec core.Codec, present bool) (any, error) {
	if obj, ok := value.(map[string]any); ok {
		return normalizeTypedMerge(t, obj, codec, present)
	}
	return normalizeTyped(t, value, codec)
}

// mergeStructFields are the members a JSON object merge into t may name:
// the fields encoding/json writes, embedded structs promoted. A name
// encoding/json resolves by depth is refused (core.StructFields).
func mergeStructFields(t reflect.Type) (map[string]*core.Field, error) {
	walk, err := core.StructFieldsOf(t, "json", "db")
	if err != nil {
		return nil, fmt.Errorf("object merge into %w; use JSON Patch replace", err)
	}
	fields := make(map[string]*core.Field, len(walk.Fields))
	for _, f := range walk.Fields {
		if !f.Hidden() {
			fields[f.External] = f
		}
	}
	return fields, nil
}

func fieldOmits(f *core.Field) bool {
	return f.HasFieldNameOption("omitempty") || f.HasFieldNameOption("omitzero")
}

// A merge containing only removals can empty an existing map. SQL preserves
// that empty object, but an omission-sensitive Go field may disappear on encode.
// A non-null member guarantees at least one key survives regardless of old data.
func mergeMayOmitMap(t reflect.Type, obj map[string]any) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Map {
		return false
	}
	for _, value := range obj {
		if value != nil {
			return false
		}
	}
	return true
}
