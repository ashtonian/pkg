package core

import (
	"fmt"
	"strings"
)

// ParsePointer splits an RFC 6901 JSON Pointer into its reference tokens,
// unescaping "~1" to "/" and "~0" to "~". The empty pointer (the whole
// document) yields no tokens. A non-empty pointer must start with "/"; a "~"
// not followed by "0" or "1" is malformed.
func ParsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if p[0] != '/' {
		return nil, fmt.Errorf("JSON Pointer %q must start with '/'", p)
	}
	tokens := strings.Split(p[1:], "/")
	for i, tok := range tokens {
		if !strings.Contains(tok, "~") {
			continue
		}
		unescaped, err := unescapeToken(tok)
		if err != nil {
			return nil, fmt.Errorf("JSON Pointer %q: %w", p, err)
		}
		tokens[i] = unescaped
	}
	return tokens, nil
}

// FormatPointer joins reference tokens into an RFC 6901 JSON Pointer,
// escaping "~" and "/" inside each token.
func FormatPointer(tokens ...string) string {
	var sb strings.Builder
	for _, tok := range tokens {
		sb.WriteByte('/')
		sb.WriteString(EscapePointerToken(tok))
	}
	return sb.String()
}

// EscapePointerToken escapes one reference token for inclusion in a JSON
// Pointer ("~" → "~0", then "/" → "~1").
func EscapePointerToken(tok string) string {
	if !strings.ContainsAny(tok, "~/") {
		return tok
	}
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~", "~0"), "/", "~1")
}

func unescapeToken(tok string) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(tok); i++ {
		if tok[i] != '~' {
			sb.WriteByte(tok[i])
			continue
		}
		if i+1 == len(tok) {
			return "", fmt.Errorf("dangling '~' in token %q", tok)
		}
		switch tok[i+1] {
		case '0':
			sb.WriteByte('~')
		case '1':
			sb.WriteByte('/')
		default:
			return "", fmt.Errorf("invalid escape '~%c' in token %q", tok[i+1], tok)
		}
		i++
	}
	return sb.String(), nil
}
