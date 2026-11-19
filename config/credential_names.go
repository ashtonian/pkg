package config

import (
	"iter"
	"strings"
	"unicode"
	"unicode/utf8"
)

// credentialWords are the words that make a field name or map key a
// credential's. A name matches when one of its words (see nameWords) is one
// of them or its plural, or — for the words of compoundSuffixMin letters or
// more — ends with one, as an unsplit compound such as dbpassword or
// accesstoken does. Shorter words match only whole, so Monkey and Keyspace are
// not keys. This is a fixed-size array, not a slice, so no caller can append
// to or reassign the set.
var credentialWords = [...]string{
	"password", "passwd", "pwd", "pass", "passphrase",
	"secret", "token", "key", "apikey", "accesskey", "secretkey", "privatekey",
	"credential", "cred", "auth", "authorization",
	"private", "dsn",
}

// compoundSuffixMin is the shortest credential word an unsplit compound may
// end with and still match. Below it, suffixes are ordinary words: monkey,
// turkey.
const compoundSuffixMin = 5

// isSensitiveName reports whether name, a Go field name or a map key, names a
// credential: whether one of its words is a credential word. It allocates
// nothing: map keys are judged on every diff and redacted print.
func isSensitiveName(name string) bool {
	for word := range nameWords(name) {
		if credentialWord(word) {
			return true
		}
	}
	return false
}

// credentialWord reports whether word, in any case, is a credential word or
// its plural, or ends with one of compoundSuffixMin letters or more.
func credentialWord(word string) bool {
	singular, plural := word, false
	if n := len(word); n > 1 && (word[n-1] == 's' || word[n-1] == 'S') {
		singular, plural = word[:n-1], true
	}
	for _, w := range credentialWords {
		if strings.EqualFold(word, w) || plural && strings.EqualFold(singular, w) {
			return true
		}
		if len(w) >= compoundSuffixMin && (hasSuffixFold(word, w) || plural && hasSuffixFold(singular, w)) {
			return true
		}
	}
	return false
}

// hasSuffixFold reports whether s ends with suffix, an ASCII word, in any
// case. A cut inside a multi-byte character cannot equal ASCII.
func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// nameWords yields the words of name, as substrings of it: name splits at
// every character that is neither a letter nor a digit, between a lower-case
// letter and an upper-case one, between a letter and a digit, and before the
// last letter of an upper-case run that a lower-case letter follows.
// HTTPAuthMode is HTTP Auth Mode, APIKey is API Key, db_password is db
// password and S3Secret is S 3 Secret; an all-capitals run such as APIKEY
// stays one word.
func nameWords(name string) iter.Seq[string] {
	return func(yield func(string) bool) {
		start := -1 // the byte offset of the word being read, or -1 between words
		var prev rune
		for i, r := range name {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				if start >= 0 && !yield(name[start:i]) {
					return
				}
				start = -1
				continue
			}
			if start >= 0 && wordBoundary(prev, r, name[i+utf8.RuneLen(r):]) {
				if !yield(name[start:i]) {
					return
				}
				start = i
			}
			if start < 0 {
				start = i
			}
			prev = r
		}
		if start >= 0 {
			yield(name[start:])
		}
	}
}

// wordBoundary reports whether a word starts at r, a letter or digit after
// prev, another one, with rest the text after r.
func wordBoundary(prev, r rune, rest string) bool {
	switch {
	case unicode.IsDigit(prev) != unicode.IsDigit(r):
		return true
	case unicode.IsLower(prev) && unicode.IsUpper(r):
		return true
	case unicode.IsUpper(prev) && unicode.IsUpper(r):
		next, _ := utf8.DecodeRuneInString(rest)
		return unicode.IsLower(next)
	}
	return false
}
