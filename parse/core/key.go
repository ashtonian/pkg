package core

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// Key is a row's key: one value per key column, in the order the resource
// declares its key columns. A one-column key holds one value.
type Key []any

// KeyText is a key's text, as includes and facets name a row by it: a
// one-column key's value as text, a longer key the compact JSON array of
// its values' texts (`["w1","a"]`). Over the canonical values of string,
// UUID and integer columns — as NormalizeScalar gives them, or as the
// database casts them to text — two keys have one text exactly when the
// database holds them equal; KeyTextOf gives any key that text.
//
// The array is the text PostgreSQL's array_to_json and SQLite's json_array
// write, so a statement can name a row by the same text: only `"`, `\` and
// control characters are escaped, the latter as \b, \f, \n, \r, \t or
// \u00xx.
func KeyText(key Key) string {
	texts := make([]string, len(key))
	for i, v := range key {
		texts[i] = keyValueText(v)
	}
	return joinKeyTexts(texts)
}

// NormalizeKey is key with each value in its column's canonical form,
// paired by position with types: read as the database holds it
// (StoredValue), so a driver.Valuer stands for the value it writes, then
// normalized as its column's type (NormalizeScalar). It is the one rule a
// key is bound and compared by — an update's target, the owner of the
// relation rows it writes, a relation's member, a key set's key — so two
// spellings of one key (an int32 and a uint64, a UUID in either case) name
// one row wherever they are bound. A key of another number of values than
// types is an error; a value that is NULL, of no scalar type (IsKeyType)
// or one its column's type cannot hold is a *KeyValueError naming its
// position.
func NormalizeKey(types []TypeInfo, key Key) (Key, error) {
	if len(key) != len(types) {
		return nil, fmt.Errorf("%d values for the %d key columns", len(key), len(types))
	}
	out := make(Key, len(key))
	for i, v := range key {
		n, err := normalizeKeyValue(types[i], v)
		if err != nil {
			return nil, &KeyValueError{Index: i, Err: err}
		}
		out[i] = n
	}
	return out, nil
}

// KeyValueError is a key value NormalizeKey refuses: Index is its position
// in the key, which a caller names its column by.
type KeyValueError struct {
	Index int
	Err   error
}

func (e *KeyValueError) Error() string { return fmt.Sprintf("key value %d: %v", e.Index, e.Err) }

func (e *KeyValueError) Unwrap() error { return e.Err }

// normalizeKeyValue is one value of a key in its column's canonical form
// (NormalizeKey).
func normalizeKeyValue(t TypeInfo, v any) (any, error) {
	if !IsKeyType(t.Type) {
		return nil, fmt.Errorf("a %s value is no key value", t.Type)
	}
	stored, _, err := StoredValue(v)
	if err != nil {
		return nil, err
	}
	// NULL is nil here, which NormalizeScalar refuses for every key type.
	return NormalizeScalar(t.Type, t.NumberKind, stored)
}

// KeyTextOf is KeyText of key, each value read as its column's type,
// paired by position with types: the canonical text KeyValueText gives it,
// of the key NormalizeKey gives. Two keys of the columns have one text
// exactly when the database holds them equal, and it is the text a
// statement names their row by.
func KeyTextOf(types []TypeInfo, key Key) (string, error) {
	normalized, err := NormalizeKey(types, key)
	if err != nil {
		valueErr, ok := errors.AsType[*KeyValueError](err)
		if ok && len(key) == 1 {
			return "", valueErr.Err
		}
		return "", err
	}
	texts := make([]string, len(normalized))
	for i, v := range normalized {
		text, err := normalizedKeyText(types[i], v)
		if err != nil {
			return "", fmt.Errorf("key value %d: %w", i, err)
		}
		texts[i] = text
	}
	return joinKeyTexts(texts), nil
}

// KeyValueText is the canonical text of a key column's value, v normalized
// as one value of a key (NormalizeKey): the text of a text type's canonical
// value; a number's canonical decimal text, a float32 column's at float32
// precision, so -0 is 0; true or false. It is the text statements read the
// column's key as, so it is equal for two values exactly when the database
// holds them equal. NaN and the infinities are no key value.
func KeyValueText(t TypeInfo, v any) (string, error) {
	n, err := normalizeKeyValue(t, v)
	if err != nil {
		return "", err
	}
	return normalizedKeyText(t, n)
}

// normalizedKeyText is the text of a normalized key value n of type t.
func normalizedKeyText(t TypeInfo, n any) (string, error) {
	switch x := n.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		if t.NumberKind == reflect.Float32 {
			return DecimalText(float32(x))
		}
		return DecimalText(x)
	}
	return "", fmt.Errorf("%s value %s has no canonical text", t.Type, gowalker.TypeText(reflect.TypeOf(n)))
}

func joinKeyTexts(texts []string) string {
	if len(texts) == 1 {
		return texts[0]
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, text := range texts {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONText(&b, text)
	}
	b.WriteByte(']')
	return b.String()
}

func keyValueText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func writeJSONText(b *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}
