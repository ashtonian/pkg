package client

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// ErrKeyValue reports a key value no item path can carry: no value at all,
// a value of a type with no text, a non-finite float, or a value whose text
// is "", "." or "..", which URL parsing drops from a path. It wraps the
// cause.
var ErrKeyValue = errors.New("client: a key value no path segment carries")

// ItemPath is the path of the row key names below its collection's path: a
// segment per key value, in key order, each escaped on its own
// (url.PathEscape), so ItemPath("w1", "a/b") is "w1/a%2Fb" and a slash in a
// value stays in its segment. A request to the collection's path, a slash
// and ItemPath reaches the item route the server registers with
// parsehttp.ItemPattern.
//
// Each value is written as the server reads its column's text
// (parse.Resource.ParseKey): a string as itself, an integer in decimal, a
// float as its JSON number, a bool as true or false, a Quoted value as its
// value, and a value with a MarshalText method (a UUID, a decimal, a
// time.Time) as the text it marshals.
func ItemPath(key ...any) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: a key has a value per key column", ErrKeyValue)
	}
	segments := make([]string, len(key))
	for i, v := range key {
		text, err := keyText(v)
		if err != nil {
			return "", fmt.Errorf("key value %d: %w", i, err)
		}
		switch text {
		case "", ".", "..":
			return "", fmt.Errorf("key value %d: %w: %q", i, ErrKeyValue, text)
		}
		segments[i] = url.PathEscape(text)
	}
	return strings.Join(segments, "/"), nil
}

// quoted is a Quoted of any scalar type.
type quoted interface{ quotedValue() any }

func (q Quoted[T]) quotedValue() any { return q.Value }

func keyText(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		return "", fmt.Errorf("%w: a %s value has no key text", ErrKeyValue, rv.Kind())
	}
	switch v := v.(type) {
	case quoted:
		return keyText(v.quotedValue())
	case encoding.TextMarshaler:
		text, err := v.MarshalText()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrKeyValue, err)
		}
		return string(text), nil
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		// encoding/json writes the shortest text at the value's own
		// precision and refuses NaN and the infinities.
		text, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrKeyValue, err)
		}
		return string(text), nil
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool()), nil
	}
	if !rv.IsValid() {
		return "", fmt.Errorf("%w: nil has no key text", ErrKeyValue)
	}
	// The kind, not the type: an unnamed struct type's text holds its tags,
	// and this package, importing only the standard library, has no
	// gowalker.TypeText to leave them out.
	return "", fmt.Errorf("%w: a %s value has no key text", ErrKeyValue, rv.Kind())
}
