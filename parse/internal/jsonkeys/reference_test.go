//go:build unit

package jsonkeys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// The implementation Validate replaced, kept as the
// reference its rewrite is fuzzed against.

func referenceValidate(raw []byte, decoded any) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON document")
	}
	return referenceKeys(raw, reflect.ValueOf(decoded).Elem(), "")
}

func referenceMapKeyName(key reflect.Value) (string, error) {
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

func referenceKeys(raw []byte, value reflect.Value, path string) error {
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
		return referenceKeys(raw, value.Elem(), path)
	}
	switch value.Kind() {
	case reflect.Map:
		return referenceMapKeys(raw, value, path)
	case reflect.Struct:
		return referenceStructKeys(raw, value, path)
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
			if err := referenceKeys(elements[i], value.Index(i), fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func referenceMapKeys(raw []byte, value reflect.Value, path string) error {
	members, err := referenceObjectMembers(raw)
	if err != nil {
		return err
	}
	keys := make(map[string]reflect.Value, value.Len())
	for key := range value.Seq2() {
		name, err := referenceMapKeyName(key)
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
		memberPath := referenceMemberPath(path, member.name)
		if seen[member.name] {
			return fmt.Errorf("duplicate JSON map member %q in %s", memberPath, value.Type())
		}
		seen[member.name] = true
		key, ok := keys[member.name]
		if !ok {
			return fmt.Errorf("JSON map member %q does not preserve its decoded key spelling in %s; use canonical JSON member names", memberPath, value.Type())
		}
		if err := referenceKeys(member.raw, value.MapIndex(key), memberPath); err != nil {
			return err
		}
	}
	return nil
}

func referenceStructKeys(raw []byte, value reflect.Value, path string) error {
	members, err := referenceObjectMembers(raw)
	if err != nil || len(members) == 0 {
		return err
	}
	fields, err := core.StructFieldsOf(value.Type(), core.JSONTagKey, core.DefaultColumnTagKey)
	if err != nil {
		return fmt.Errorf("JSON object at %q: %w", path, err)
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		field, ok := fields.External(member.name)
		if !ok {
			// Decoding matches by case breadth first: the shallowest
			// candidate, then the first declared.
			for _, candidate := range fields.Fields {
				if !candidate.Hidden() && strings.EqualFold(candidate.External, member.name) && (!ok || len(candidate.Route) < len(field.Route)) {
					field, ok = candidate, true
				}
			}
		}
		if !ok {
			continue
		}
		memberPath := referenceMemberPath(path, member.name)
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
		if err := referenceKeys(member.raw, fv, memberPath); err != nil {
			return err
		}
	}
	return nil
}

type referenceObjectMember struct {
	name string
	raw  json.RawMessage
}

// Decode each raw member separately so duplicate names survive validation.
func referenceObjectMembers(raw []byte) ([]referenceObjectMember, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var members []referenceObjectMember
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
		members = append(members, referenceObjectMember{name: name, raw: member})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return members, nil
}

func referenceMemberPath(path, name string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
