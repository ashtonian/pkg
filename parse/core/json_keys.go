package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// CanonicalJSONMapKey decodes a member name into a map key and returns its JSON
// spelling. Dummy values keep the map element's custom codec out of this path.
func CanonicalJSONMapKey(t reflect.Type, name string) (reflect.Value, string, error) {
	if t == nil || !t.Comparable() {
		return reflect.Value{}, "", fmt.Errorf("invalid JSON map key type %v", t)
	}
	keyMap := reflect.New(reflect.MapOf(t, reflect.TypeFor[json.RawMessage]()))
	raw, err := json.Marshal(map[string]any{name: nil})
	if err != nil {
		return reflect.Value{}, "", err
	}
	if err := json.Unmarshal(raw, keyMap.Interface()); err != nil {
		return reflect.Value{}, "", err
	}
	keys := keyMap.Elem().MapKeys()
	if len(keys) != 1 {
		return reflect.Value{}, "", fmt.Errorf("JSON member %q does not decode to one %s key", name, t)
	}
	canonical, err := jsonMapKeyName(keys[0])
	return keys[0], canonical, err
}

func jsonMapKeyName(key reflect.Value) (string, error) {
	probe := reflect.MakeMapWithSize(reflect.MapOf(key.Type(), reflect.TypeFor[json.RawMessage]()), 1)
	probe.SetMapIndex(key, reflect.Zero(reflect.TypeFor[json.RawMessage]()))
	raw, err := json.Marshal(probe.Interface())
	if err != nil {
		return "", err
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return "", err
	}
	for name := range encoded {
		return name, nil
	}
	return "", fmt.Errorf("JSON key %s encodes no member", key.Type())
}

// ValidateJSONKeyRepresentation checks physical member names against a freshly
// decoded value, passed by non-nil pointer, without decoding or encoding its
// element values again. Ordinary
// map keys and recognized struct fields must retain their JSON spelling. Unknown
// struct members and interface, RawMessage and custom JSON subtrees are opaque.
func ValidateJSONKeyRepresentation(raw []byte, decoded any) error {
	target := reflect.ValueOf(decoded)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("JSON key validation target must be a non-nil pointer, got %T", decoded)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON document")
	}
	return validateJSONKeys(raw, target.Elem(), "")
}

func validateJSONKeys(raw []byte, value reflect.Value, path string) error {
	if !value.IsValid() || value.Kind() == reflect.Interface {
		return nil
	}
	t := value.Type()
	if t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]()) ||
		t.Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateJSONKeys(raw, value.Elem(), path)
	}
	switch value.Kind() {
	case reflect.Map:
		return validateJSONMapKeys(raw, value, path)
	case reflect.Struct:
		return validateJSONStructKeys(raw, value, path)
	case reflect.Array, reflect.Slice:
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '[' {
			return nil
		}
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return err
		}
		for i := range min(len(elements), value.Len()) {
			if err := validateJSONKeys(elements[i], value.Index(i), fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateJSONMapKeys(raw []byte, value reflect.Value, path string) error {
	members, err := jsonObjectMembers(raw)
	if err != nil {
		return err
	}
	keys := make(map[string]reflect.Value, value.Len())
	for key := range value.Seq2() {
		name, err := jsonMapKeyName(key)
		if err != nil {
			return fmt.Errorf("JSON map at %q (%s): %w", path, value.Type(), err)
		}
		if _, duplicate := keys[name]; duplicate {
			return fmt.Errorf("JSON map at %q (%s) has keys that both encode as %q", path, value.Type(), name)
		}
		keys[name] = key
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		memberPath := jsonMemberPath(path, member.name)
		if seen[member.name] {
			return fmt.Errorf("duplicate JSON map member %q in %s", memberPath, value.Type())
		}
		seen[member.name] = true
		key, ok := keys[member.name]
		if !ok {
			return fmt.Errorf("JSON map member %q does not preserve its decoded key spelling in %s; use canonical JSON member names", memberPath, value.Type())
		}
		if err := validateJSONKeys(member.raw, value.MapIndex(key), memberPath); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONStructKeys(raw []byte, value reflect.Value, path string) error {
	members, err := jsonObjectMembers(raw)
	if err != nil || len(members) == 0 {
		return err
	}
	fields, err := StructFieldsOf(value.Type(), "json", "db")
	if err != nil {
		return fmt.Errorf("JSON object at %q: %w", path, err)
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		field, ok := fields.External(member.name)
		if !ok {
			for _, candidate := range fields.Fields {
				if !candidate.Hidden() && strings.EqualFold(candidate.External, member.name) {
					field, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			continue
		}
		memberPath := jsonMemberPath(path, member.name)
		if member.name != field.External {
			return fmt.Errorf("JSON member %q is noncanonical for %s; use %q", memberPath, value.Type(), field.External)
		}
		if seen[member.name] {
			return fmt.Errorf("duplicate JSON member %q in %s", memberPath, value.Type())
		}
		seen[member.name] = true
		fv, err := field.Value(value)
		if err != nil {
			return fmt.Errorf("JSON member %q: %w", memberPath, err)
		}
		if err := validateJSONKeys(member.raw, fv, memberPath); err != nil {
			return err
		}
	}
	return nil
}

type jsonObjectMember struct {
	name string
	raw  json.RawMessage
}

// Decode each raw member separately so duplicate names survive validation.
func jsonObjectMembers(raw []byte) ([]jsonObjectMember, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var members []jsonObjectMember
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object member has non-string name")
		}
		var member json.RawMessage
		if err := decoder.Decode(&member); err != nil {
			return nil, err
		}
		members = append(members, jsonObjectMember{name: name, raw: member})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return members, nil
}

func jsonMemberPath(path, name string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
