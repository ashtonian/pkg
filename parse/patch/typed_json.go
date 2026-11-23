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

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/jsonkeys"
)

// checkJSONMembers rejects members of data, a value the codec encoded, that a
// decode into t would silently discard, inside typed maps, arrays and
// embedded structs too. The check decodes with encoding/json because the
// Codec interface has no mode that refuses an unknown member, and the patch
// schema names members by encoding/json's rules; the codec still does the
// decode that keeps the value. Custom JSON decoders own their input contract.
func checkJSONMembers(t reflect.Type, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(reflect.New(t).Interface()); err != nil {
		return fmt.Errorf("value does not fit %s: %w", gowalker.TypeText(t), err)
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
	if untypedDocument(t) {
		return obj, nil
	}
	if customJSONType(t) {
		return nil, fmt.Errorf("object merge for custom JSON type %s is not supported; use JSON Patch replace", gowalker.TypeText(t))
	}

	out := make(map[string]any, len(obj))
	switch t.Kind() {
	case reflect.Map:
		// A member names its key as encoding/json decodes one (an integer
		// kind in base 10, a type with text methods through them) and is
		// written under the name encoding/json writes for that key, so the
		// stored document holds one spelling per key and "01" merges into
		// "1". A string key without text methods is its own name.
		canonicalize := declared || t.Key().Kind() != reflect.String
		sources := make(map[string]string, len(obj))
		for _, name := range slices.Sorted(maps.Keys(obj)) {
			value := obj[name]
			canonical := name
			if canonicalize {
				_, key, err := jsonkeys.CanonicalMapKey(t.Key(), name)
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
			return nil, fmt.Errorf("object merge for %s does not encode an object; use JSON Patch replace", gowalker.TypeText(t))
		}
		// Sorted, as the map branch is: the first failure reported must not
		// depend on map order.
		for _, name := range slices.Sorted(maps.Keys(obj)) {
			value := obj[name]
			field, ok := fields[name]
			if !ok {
				return nil, fmt.Errorf("unknown or noncanonical JSON member %q in %s", name, gowalker.TypeText(t))
			}
			_, emitted := encoded[name]
			if value == nil {
				if emitted {
					return nil, fmt.Errorf("deleting JSON member %q: %s would re-emit it; use JSON Patch replace", name, gowalker.TypeText(t))
				}
				out[name] = nil
				continue
			}
			if !emitted {
				return nil, fmt.Errorf("JSON member %q is omitted by %s; use null to delete it or JSON Patch replace", name, gowalker.TypeText(t))
			}
			// Full typed decoding above already checks scalar field values,
			// including json:",string". Objects need partial-merge validation;
			// arrays replace whole but still need exact fixed-array lengths.
			n := encoded[name]
			if nested, object := value.(map[string]any); object {
				if fieldOmits(field) && mergeMayOmitMap(field.Type(), nested) {
					return nil, fmt.Errorf("JSON member %q may become empty and omitted; use JSON Patch replace", name)
				}
				n, err = normalizeMergeMember(field.Type(), value, codec, present && !fieldOmits(field), declared)
			} else if _, array := core.AnySlice(value); array && !declared {
				_, err = normalizeTyped(field.Type(), value, codec)
			}
			if err == nil && declared {
				err = rejectCustomObjectMerge(field.Type(), value, n)
			}
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", name, err)
			}
			out[name] = n
		}
		if !present {
			for _, name := range slices.Sorted(maps.Keys(encoded)) {
				if _, supplied := out[name]; !supplied {
					return nil, fmt.Errorf("object merge into potentially absent %s must supply member %q; use JSON Patch replace", gowalker.TypeText(t), name)
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("merging an object into %s: it holds no members", gowalker.TypeText(t))
	}
}

// untypedDocument reports a type that holds any JSON document: an interface,
// or json.RawMessage, which implements the marshaler interfaces but keeps a
// merged object's shape verbatim, so a merge into it merges as into any
// document.
func untypedDocument(t reflect.Type) bool {
	return t.Kind() == reflect.Interface || t == reflect.TypeFor[json.RawMessage]()
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
		return nil, fmt.Errorf("value does not fit %s: %w", gowalker.TypeText(t), err)
	}
	if jsonNil(decoded.Elem()) {
		return nil, fmt.Errorf("JSON document decodes to nil %s; use json.RawMessage to preserve JSON null", gowalker.TypeText(t))
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

// checkDocumentMember refuses an operation below a field whose Go type
// declares its document's shape, where ApplyValidatedPatch, decoding the
// field once the patch applies, would refuse the result: a member a struct
// does not name exactly, a map key encoding/json does not decode or writes
// under another name (two names for one key), a member of a value that has
// none, and a value an add or a replace writes that does not decode as the
// member. SQL writes a stored document without decoding it, so this is what
// holds that document to its type. Below an interface, json.RawMessage or a
// type that decodes itself, the document's shape is its own.
func checkDocumentMember(schema *PatchSchema, op PatchOp, path string, value any) error {
	tokens, err := core.ParsePointer(path)
	if err != nil {
		return err
	}
	field, ok := schema.pathSet().resolve(core.FormatPointer(tokens[0]))
	if !ok {
		return nil
	}
	t := field.GoType
	for i, token := range tokens[1:] {
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t == nil || untypedDocument(t) || customJSONType(t) {
			return nil
		}
		parent := t
		switch {
		case t.Kind() == reflect.Map:
			_, canonical, err := jsonkeys.CanonicalMapKey(t.Key(), token)
			if err != nil {
				return fmt.Errorf("member %q: %w", token, err)
			}
			if canonical != token {
				return fmt.Errorf("member %q is the %s key encoding/json writes as %q", token, gowalker.TypeText(t.Key()), canonical)
			}
			t = t.Elem()
		case t.Kind() == reflect.Struct:
			f := jsonMember(t, token)
			if f == nil {
				return fmt.Errorf("%s has no member %q", gowalker.TypeText(t), token)
			}
			t = f.Type()
		case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
			return fmt.Errorf("%s encodes as a string, which has no members", gowalker.TypeText(t))
		case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
			t = t.Elem()
		default:
			return fmt.Errorf("%s has no members", gowalker.TypeText(t))
		}
		if i == len(tokens)-2 && (op == PatchOpAdd || op == PatchOpReplace) {
			return checkMemberFits(parent, token, value)
		}
	}
	return nil
}

// checkMemberFits decodes value as member name of parent inside a
// one-member instance of parent, so the member's tag options apply as
// encoding/json applies them.
func checkMemberFits(parent reflect.Type, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var wrapped []byte
	if parent.Kind() == reflect.Slice || parent.Kind() == reflect.Array {
		wrapped = slices.Concat([]byte("["), raw, []byte("]"))
	} else {
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		wrapped = slices.Concat([]byte("{"), key, []byte(":"), raw, []byte("}"))
	}
	err = checkJSONMembers(parent, wrapped)
	if err != nil {
		return fmt.Errorf("member %q: %w", name, err)
	}
	return nil
}

// jsonMember is the field of struct type t that encoding/json names name,
// matched exactly: RFC 6901 names a member by its exact name, where a
// decode would also take a field's name in another case.
func jsonMember(t reflect.Type, name string) *core.Field {
	walk, err := core.JSONFieldsOf(t)
	if err != nil {
		return nil
	}
	f, _ := walk.External(name)
	return f
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
			} else if !field.Quoted() {
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
			key, canonical, err := jsonkeys.CanonicalMapKey(t.Key(), name)
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
			return nil, fmt.Errorf("expected exactly %d elements for %s, got %d", t.Len(), gowalker.TypeText(t), len(input))
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
	if field.HasFieldNameOption(jsonOmitEmpty) {
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
	if field.HasFieldNameOption(jsonOmitZero) {
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

func missingJSONMember(field *core.Field, value reflect.Value) (any, bool, error) {
	// The stdlib owns ,string and custom-method/addressability rules. Encoding
	// this missing zero field cannot replay a supplied custom value's codec.
	wrapper := reflect.New(reflect.StructOf([]reflect.StructField{{
		Name: "Value", Type: field.Type(), Tag: reflect.StructTag("json:" + strconv.Quote("value,"+field.Tags().FieldNameOptions)),
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
	// A json.RawMessage member is a document like an interface one; a value
	// of a custom-encoded Go type merges into neither.
	if (customJSONType(t) && !untypedDocument(t)) || customJSONType(reflect.TypeOf(value)) {
		return fmt.Errorf("object merge for custom JSON type %s is not supported; use JSON Patch replace", gowalker.TypeText(t))
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
// the fields encoding/json writes, as it names them (core.JSONFieldsOf).
func mergeStructFields(t reflect.Type) (map[string]*core.Field, error) {
	walk, err := core.JSONFieldsOf(t)
	if err != nil {
		return nil, fmt.Errorf("object merge into %w; use JSON Patch replace", err)
	}
	fields := make(map[string]*core.Field, walk.Len())
	for f := range walk.Fields() {
		if !f.Hidden() {
			fields[f.External()] = f
		}
	}
	return fields, nil
}

// The encoding/json tag options that omit a member's value.
const (
	jsonOmitEmpty = "omitempty"
	jsonOmitZero  = "omitzero"
)

func fieldOmits(f *core.Field) bool {
	return f.HasFieldNameOption(jsonOmitEmpty) || f.HasFieldNameOption(jsonOmitZero)
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
