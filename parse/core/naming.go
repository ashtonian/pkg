package core

import "strings"

// NamingConvention transforms an identifier string according to a convention.
type NamingConvention func(string) string

// The built-in naming conventions. Each is a function, so a
// generator's default cannot be changed from outside the package.

// SnakeCase converts PascalCase or camelCase to snake_case: the default
// column convention.
func SnakeCase(s string) string {
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

// CamelCase converts PascalCase to camelCase: the first letter lowercase.
func CamelCase(s string) string {
	if len(s) == 0 {
		return s
	}
	if first := s[0]; first >= 'A' && first <= 'Z' {
		return string(first+32) + s[1:]
	}
	return s
}

// PascalCase returns s unchanged: Go's own naming.
func PascalCase(s string) string { return s }

// LowerCase converts s to lowercase, with no separators.
func LowerCase(s string) string { return strings.ToLower(s) }

// TableConvention transforms a type name into a SQL table name.
type TableConvention func(string) string

// PluralSnake converts PascalCase to plural snake_case, "Product" to
// "products": the default table convention.
func PluralSnake(s string) string { return Pluralize(SnakeCase(s)) }

// Singular converts PascalCase to singular snake_case: "Product" to
// "product".
func Singular(s string) string { return SnakeCase(s) }

// PluralCamel converts PascalCase to plural camelCase: "Product" to
// "products".
func PluralCamel(s string) string { return Pluralize(CamelCase(s)) }

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
