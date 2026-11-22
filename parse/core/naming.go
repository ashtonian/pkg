package core

import "strings"

// NamingConvention transforms an identifier string according to a convention.
type NamingConvention func(string) string

// Built-in naming conventions.
var (
	// SnakeCase converts PascalCase/camelCase to snake_case (default).
	SnakeCase NamingConvention = ToSnakeCase

	// CamelCase converts PascalCase to camelCase (first letter lowercase).
	CamelCase NamingConvention = toCamelCase

	// PascalCase returns the string unchanged (Go's default naming).
	PascalCase NamingConvention = func(s string) string { return s }

	// LowerCase converts to all lowercase with no separators.
	LowerCase NamingConvention = strings.ToLower
)

// TableConvention transforms a type name into a SQL table name.
type TableConvention func(string) string

// Built-in table conventions.
var (
	// PluralSnake converts PascalCase to plural snake_case: "Product" → "products" (default).
	PluralSnake TableConvention = func(s string) string { return Pluralize(ToSnakeCase(s)) }

	// Singular converts PascalCase to singular snake_case: "Product" → "product".
	Singular TableConvention = func(s string) string { return ToSnakeCase(s) }

	// PluralCamel converts PascalCase to plural camelCase: "Product" → "products".
	PluralCamel TableConvention = func(s string) string { return Pluralize(toCamelCase(s)) }
)

// Pluralize returns the English plural form of a word using common rules.
// Handles -y → -ies, sibilant suffixes → -es, and default -s.
func Pluralize(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "ies") || strings.HasSuffix(lower, "ses") ||
		strings.HasSuffix(lower, "xes") || strings.HasSuffix(lower, "zes") ||
		strings.HasSuffix(lower, "ches") || strings.HasSuffix(lower, "shes"):
		return s // already plural
	case strings.HasSuffix(lower, "y"):
		if len(lower) >= 2 && !isVowel(lower[len(lower)-2]) {
			return s[:len(s)-1] + "ies"
		}
		return s + "s"
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"),
		strings.HasSuffix(lower, "z"), strings.HasSuffix(lower, "ch"),
		strings.HasSuffix(lower, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return true
	}
	return false
}

// ToSnakeCase converts PascalCase or camelCase to snake_case.
func ToSnakeCase(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				prev := s[i-1]
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
					result = append(result, '_')
				} else if prev >= 'A' && prev <= 'Z' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
					result = append(result, '_')
				}
			}
			result = append(result, c+32) // toLower
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}

func toCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}
	// Lowercase first letter.
	first := s[0]
	if first >= 'A' && first <= 'Z' {
		return string(first+32) + s[1:]
	}
	return s
}
