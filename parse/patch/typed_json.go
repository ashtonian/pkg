package patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// checkJSONMembers rejects fields encoding/json would silently discard, including
// unknown fields inside typed maps, arrays and embedded structs. Custom JSON
// decoders own their input contract.
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
	return normalizeTypedMerge(ps.GoType, obj, codec, true, ps.SQLType.IsJSON())
}

// normalizeTypedMerge preserves the set of supplied members. Full replacement
// normalization would fill omitted struct fields with zeros and overwrite siblings.
// present means the existing canonical document necessarily contains this object.
// Nullable objects and map entries can be absent, so their patch must also be a
// complete canonical value when applied to an empty object.
func normalizeTypedMerge(t reflect.Type, obj map[string]any, codec core.Codec, present, declared bool) (any, error) {
	for t.Kind() == reflect.Pointer {
		present = false
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface || t == reflect.TypeFor[json.RawMessage]() {
		// RawMessage is an untyped JSON document, despite implementing the
		// marshaler interfaces; its object shape is preserved verbatim.
		return obj, nil
	}
	if customJSONType(t) {
		return nil, fmt.Errorf("object merge for custom JSON type %s is not supported; use JSON Patch replace", t)
	}

	out := make(map[string]any, len(obj))
	switch t.Kind() {
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("object merge requires string map keys for %s; use JSON Patch replace", t)
		}
		sources := make(map[string]string, len(obj))
		for _, name := range slices.Sorted(maps.Keys(obj)) {
			value := obj[name]
			canonical := name
			if declared {
				_, key, err := core.CanonicalJSONMapKey(t.Key(), name)
				if err != nil {
					return nil, err
				}
				canonical = key
				if err := claimJSONMapKey(sources, name, canonical); err != nil {
					return nil, err
				}
			}
			if value == nil {
				out[canonical] = nil // map members can actually disappear
				continue
			}
			n, err := normalizeMergeMember(t.Elem(), value, codec, false, declared)
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", name, err)
			}
			out[canonical] = n
		}
		return out, nil
	case reflect.Struct:
		fields, err := mergeStructFields(t)
		if err != nil {
			return nil, err
		}
		// Decode once to check the entire input, including unknown nested fields.
		canonical, err := normalizeMergeReplacement(t, obj, codec, declared)
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
				n, err = normalizeMergeMember(field.Type, value, codec, present && !fieldOmits(field), declared)
			} else if _, array := core.AnySlice(value); array && !declared {
				_, err = normalizeTyped(field.Type, value, codec)
			}
			if err == nil && declared {
				err = rejectCustomObjectMerge(field.Type, value, n)
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

func customJSONType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	return t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]()) ||
		t.Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]())
}

// Declared JSON uses Go's JSON contract before scalar/driver conversion. The
// decoded value validates the input and controls ordinary field omission; custom
// subtrees retain their original representation for the single storage decode.
func normalizeDeclaredJSON(t reflect.Type, value any) (any, error) {
	if jsonNil(reflect.ValueOf(value)) {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoded := reflect.New(t)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(decoded.Interface()); err != nil {
		return nil, fmt.Errorf("value does not fit %s: %w", t, err)
	}
	if jsonNil(decoded.Elem()) {
		return nil, fmt.Errorf("JSON document decodes to nil %s; use json.RawMessage to preserve JSON null", t)
	}
	var document any
	if err := decodeJSONDocument(raw, &document); err != nil {
		return nil, err
	}
	canonical, err := canonicalJSONReplacement(decoded.Elem(), document)
	if err == nil && canonical == nil {
		// A present JSON null is different from the nil Go input handled above.
		return json.RawMessage(raw), nil
	}
	return canonical, err
}

func decodeJSONDocument(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(dst)
}

func jsonNil(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return false
}

func canonicalJSONReplacement(value reflect.Value, document any) (any, error) {
	t := value.Type()
	if customJSONType(t) || t.Kind() == reflect.Interface {
		return document, nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, nil
		}
		return canonicalJSONReplacement(value.Elem(), document)
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, err := mergeStructFields(t)
		if err != nil {
			return nil, err
		}
		input, _ := document.(map[string]any)
		out := make(map[string]any, len(fields))
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			field := fields[name]
			fv, err := field.Value(value)
			if err != nil { // encoding/json omits fields behind a nil embedded pointer.
				continue
			}
			if jsonFieldOmitted(field, fv) {
				continue
			}
			member, present, err := replacementMember(input, name)
			if err != nil {
				return nil, err
			}
			if !present {
				member, present, err = missingJSONMember(field, fv)
				if err != nil {
					return nil, fmt.Errorf("member %q: %w", name, err)
				}
				if !present {
					continue
				}
			} else if !jsonStringField(field) {
				member, err = canonicalJSONReplacement(fv, member)
				if err != nil {
					return nil, fmt.Errorf("member %q: %w", name, err)
				}
			}
			out[name] = member
		}
		return out, nil
	case reflect.Map:
		input, ok := document.(map[string]any)
		if !ok {
			return document, nil
		}
		out := make(map[string]any, len(input))
		sources := make(map[string]string, len(input))
		for _, name := range slices.Sorted(maps.Keys(input)) {
			key, canonical, err := core.CanonicalJSONMapKey(t.Key(), name)
			if err != nil {
				return nil, err
			}
			if err := claimJSONMapKey(sources, name, canonical); err != nil {
				return nil, err
			}
			member, err := canonicalJSONReplacement(value.MapIndex(key), input[name])
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", name, err)
			}
			out[canonical] = member
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return encodedJSONDocument(value.Interface())
		}
		input, ok := document.([]any)
		if !ok {
			return document, nil
		}
		if t.Kind() == reflect.Array && len(input) != t.Len() {
			return nil, fmt.Errorf("expected exactly %d elements for %s, got %d", t.Len(), t, len(input))
		}
		out := make([]any, len(input))
		for i, member := range input {
			normalized, err := canonicalJSONReplacement(value.Index(i), member)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return encodedJSONDocument(value.Interface())
	}
}

func encodedJSONDocument(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	if err := decodeJSONDocument(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func replacementMember(input map[string]any, name string) (any, bool, error) {
	var result any
	found := false
	for key, value := range input {
		if strings.EqualFold(key, name) {
			if found {
				return nil, false, fmt.Errorf("multiple JSON members resolve to %q", name)
			}
			result, found = value, true
		}
	}
	return result, found, nil
}

func jsonFieldOmitted(field *core.Field, value reflect.Value) bool {
	if field.HasFieldNameOption("omitempty") {
		switch value.Kind() {
		case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
			if value.Len() == 0 {
				return true
			}
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
			if value.IsZero() {
				return true
			}
		}
	}
	if field.HasFieldNameOption("omitzero") {
		zeroer := reflect.TypeFor[interface{ IsZero() bool }]()
		if value.Type().Implements(zeroer) {
			if (value.Kind() == reflect.Pointer && value.IsNil()) ||
				(value.Kind() == reflect.Interface && (value.IsNil() || (value.Elem().Kind() == reflect.Pointer && value.Elem().IsNil()))) {
				return true
			}
			zero, ok := value.Interface().(interface{ IsZero() bool })
			return ok && zero.IsZero()
		}
		if reflect.PointerTo(value.Type()).Implements(zeroer) {
			copy := reflect.New(value.Type())
			copy.Elem().Set(value)
			zero, ok := copy.Interface().(interface{ IsZero() bool })
			return ok && zero.IsZero()
		}
		return value.IsZero()
	}
	return false
}

func jsonStringField(field *core.Field) bool {
	if !field.HasFieldNameOption("string") {
		return false
	}
	t := field.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.String:
		return true
	}
	return false
}

func missingJSONMember(field *core.Field, value reflect.Value) (any, bool, error) {
	// The stdlib owns ,string and custom-method/addressability rules. Encoding
	// this missing zero field cannot replay a supplied custom value's codec.
	wrapper := reflect.New(reflect.StructOf([]reflect.StructField{{
		Name: "Value", Type: field.Type, Tag: reflect.StructTag("json:" + strconv.Quote("value,"+field.Tags.FieldNameOptions)),
	}})).Elem()
	wrapper.Field(0).Set(value)
	raw, err := json.Marshal(wrapper.Interface())
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(raw, reflect.New(wrapper.Type()).Interface()); err != nil {
		return nil, false, err
	}
	var document map[string]any
	if err := decodeJSONDocument(raw, &document); err != nil {
		return nil, false, err
	}
	member, present := document["value"]
	return member, present, nil
}

func normalizeMergeMember(t reflect.Type, value any, codec core.Codec, present, declared bool) (any, error) {
	if obj, ok := value.(map[string]any); ok {
		return normalizeTypedMerge(t, obj, codec, present, declared)
	}
	normalized, err := normalizeMergeReplacement(t, value, codec, declared)
	if err == nil && declared {
		err = rejectCustomObjectMerge(t, value, normalized)
	}
	return normalized, err
}

func normalizeMergeReplacement(t reflect.Type, value any, codec core.Codec, declared bool) (any, error) {
	if declared {
		return normalizeDeclaredJSON(t, value)
	}
	return normalizeTyped(t, value, codec)
}

func rejectCustomObjectMerge(t reflect.Type, value, normalized any) error {
	if _, object := normalized.(map[string]any); !object {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if customJSONType(t) || customJSONType(reflect.TypeOf(value)) {
		return fmt.Errorf("object merge for custom JSON type %s is not supported; use JSON Patch replace", t)
	}
	return nil
}

func claimJSONMapKey(sources map[string]string, name, canonical string) error {
	if previous, exists := sources[canonical]; exists {
		return fmt.Errorf("JSON map keys %q and %q both encode as %q", previous, name, canonical)
	}
	sources[canonical] = name
	return nil
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
