package core

import "strings"

// NamingConvention transforms an identifier string according to a convention.
type NamingConvention func(string) string

// The built-in naming conventions. Each is a function, so a
// generator's default cannot be changed from outside the package.

// SnakeCase converts PascalCase or camelCase to snake_case: the default
// column convention. An acronym is one word ("HTTPStatus" is
// "http_status"), plural too ("userIDs" is "user_ids").
func SnakeCase(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isUpperASCII(c) {
			result = append(result, c)
			continue
		}
		if i > 0 {
			prev := s[i-1]
			switch {
			case isLowerASCII(prev) || isDigit(prev):
				result = append(result, '_')
			case isUpperASCII(prev) && i+1 < len(s) && isLowerASCII(s[i+1]) && !acronymPlural(s, i+1):
				// The last capital of an acronym starts the next word.
				result = append(result, '_')
			}
		}
		result = append(result, c+'a'-'A')
	}
	return string(result)
}

// acronymPlural reports s[i] the s that makes the acronym before it plural
// and ends the word: the s of "IDs" in "userIDs" and "userIDsByTenant".
func acronymPlural(s string, i int) bool {
	return s[i] == 's' && (i+1 == len(s) || !isLowerASCII(s[i+1]))
}

func isUpperASCII(c byte) bool { return 'A' <= c && c <= 'Z' }

func isLowerASCII(c byte) bool { return 'a' <= c && c <= 'z' }

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

// Pluralize returns the English plural of a word by the regular rules: -y
// after a consonant to -ies, -sis to -ses, a sibilant (-s, -x, -z, -ch,
// -sh) to -es with a one-syllable word's single final z doubled ("quiz" to
// "quizzes"), and -s otherwise. A word that already ends in one of those
// plurals is returned as it is. Irregular nouns ("person", "leaf", "child")
// take the regular rule, so a table named after one needs an explicit name
// (WithSQLTableName).
func Pluralize(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	switch {
	case hasAnySuffix(lower, "ies", "ses", "xes", "zes", "ches", "shes"):
		return s // already plural
	case strings.HasSuffix(lower, "y"):
		if len(lower) >= 2 && !isVowel(lower[len(lower)-2]) {
			return s[:len(s)-1] + "ies"
		}
		return s + "s"
	case strings.HasSuffix(lower, "sis"):
		return s[:len(s)-2] + "es"
	case strings.HasSuffix(lower, "z") && oneSyllableAfterVowel(lower):
		return s + s[len(s)-1:] + "es"
	case hasAnySuffix(lower, "s", "x", "z", "ch", "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// oneSyllableAfterVowel reports a word with one group of vowels that ends
// in a consonant right after it: "quiz", "fez", not "topaz" or "waltz".
func oneSyllableAfterVowel(lower string) bool {
	n := len(lower)
	if n < 2 || !isVowel(lower[n-2]) {
		return false
	}
	groups := 0
	for i := range n {
		if isVowel(lower[i]) && (i == 0 || !isVowel(lower[i-1])) {
			groups++
		}
	}
	return groups == 1
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return true
	}
	return false
}
