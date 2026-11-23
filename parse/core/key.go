package core

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
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

// KeyTextOf is KeyText of key, each value read as its column's type,
// paired by position with types: the canonical text KeyValueText gives it.
// Two keys of the columns have one text exactly when the database holds
// them equal, and it is the text a statement names their row by.
func KeyTextOf(types []TypeInfo, key Key) (string, error) {
	if len(key) != len(types) {
		return "", fmt.Errorf("key has %d values for %d columns", len(key), len(types))
	}
	texts := make([]string, len(key))
	for i, v := range key {
		text, err := KeyValueText(types[i], v)
		if err != nil {
			if len(key) == 1 {
				return "", err
			}
			return "", fmt.Errorf("key value %d: %w", i, err)
		}
		texts[i] = text
	}
	return joinKeyTexts(texts), nil
}

// KeyValueText is the canonical text of a key column's value, v normalized
// as t (NormalizeScalar): the text of a text type's canonical value; a
// number's canonical decimal text, a float32 column's at float32
// precision, so -0 is 0; true or false. It is the text statements read the
// column's key as, so it is equal for two values exactly when the database
// holds them equal. NaN and the infinities are no key value.
func KeyValueText(t TypeInfo, v any) (string, error) {
	if !IsKeyType(t.Type) {
		return "", fmt.Errorf("a %s value is no key value", t.Type)
	}
	n, err := NormalizeScalar(t.Type, t.NumberKind, v)
	if err != nil {
		return "", err
	}
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
	return "", fmt.Errorf("%s value %T has no canonical text", t.Type, n)
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
