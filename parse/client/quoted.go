package client

import (
	"encoding/json"
	"fmt"
)

// QuotedScalar is a type a field tagged json:",string" can hold on the
// server: a bool, an integer or a float.
type QuotedScalar interface {
	~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Quoted is a value of a field the server declares json:",string" (its
// schema's wire is "string"): it travels as a JSON string holding the
// value's JSON text, "9007199254740993" rather than 9007199254740993, as
// encoding/json writes the field. Filter operands, merge-patch members and
// JSON Patch values of such a field take a Quoted, and a response decodes
// into one, so a 64-bit integer stays exact end to end.
type Quoted[T QuotedScalar] struct{ Value T }

// Quote is v as a Quoted.
func Quote[T QuotedScalar](v T) Quoted[T] { return Quoted[T]{Value: v} }

// MarshalJSON writes the value's JSON text inside a JSON string.
func (q Quoted[T]) MarshalJSON() ([]byte, error) {
	text, err := json.Marshal(q.Value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

// UnmarshalJSON reads a JSON string holding the value's JSON text, as
// encoding/json reads a quoted field.
func (q *Quoted[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil // as encoding/json leaves a value on null
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("client: a quoted value is a JSON string: %w", err)
	}
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return fmt.Errorf("client: quoted value %q: %w", text, err)
	}
	q.Value = v
	return nil
}
