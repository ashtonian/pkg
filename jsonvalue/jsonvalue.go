// Package jsonvalue decodes JSON into fresh values before replacing a destination.
package jsonvalue

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// UnmarshalFresh replaces the pointed-to value only after decoding into a fresh
// value succeeds. Reused maps retain no old members, and errors leave v intact.
// Custom unmarshal methods receive zero-valued receivers; their external side
// effects are outside this replacement guarantee. Callers synchronize access to v.
func UnmarshalFresh(data []byte, v any) error {
	return unmarshalFresh(data, v, false)
}

// UnmarshalPresent is UnmarshalFresh for a document that must retain a non-nil
// Go representation. It rejects nil maps, slices, pointers and interfaces without
// replacing v. Use json.RawMessage to retain JSON null as a present document.
func UnmarshalPresent(data []byte, v any) error {
	return unmarshalFresh(data, v, true)
}

func unmarshalFresh(data []byte, v any, requirePresent bool) error {
	target := reflect.ValueOf(v)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
	}
	fresh := reflect.New(target.Elem().Type())
	if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		return err
	}
	if requirePresent {
		switch value := fresh.Elem(); value.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
			if value.IsNil() {
				return fmt.Errorf("JSON document decodes to nil %s; use json.RawMessage to preserve JSON null", value.Type())
			}
		}
	}
	target.Elem().Set(fresh.Elem())
	return nil
}
