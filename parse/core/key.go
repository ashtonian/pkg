package core

import (
	"fmt"
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
// database holds them equal.
//
// The array is the text PostgreSQL's array_to_json and SQLite's json_array
// write, so a statement can name a row by the same text: only `"`, `\` and
// control characters are escaped, the latter as \b, \f, \n, \r, \t or
// \u00xx.
func KeyText(key Key) string {
	if len(key) == 1 {
		return keyValueText(key[0])
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range key {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONText(&b, keyValueText(v))
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
