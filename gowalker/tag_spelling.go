package gowalker

import (
	"slices"
	"strings"
)

// MisspeltTagKey returns the key of known that the tag key written most
// likely misspells, for a library to refuse a key it would otherwise ignore
// without a word: first one equal to written ignoring case, '-' and '_',
// else the first one character inserted, deleted, replaced or swapped with
// its neighbour away from it under that folding. ok is false when written is
// one of known, or near none of them. [TagKeys] lists the keys a tag holds.
func MisspeltTagKey(written string, known []string) (meant string, ok bool) {
	if slices.Contains(known, written) {
		return "", false
	}
	folded := foldTagKey(written)
	for _, key := range known {
		if foldTagKey(key) == folded {
			return key, true
		}
	}
	for _, key := range known {
		if oneEdit(folded, foldTagKey(key)) {
			return key, true
		}
	}
	return "", false
}

var tagKeySeparators = strings.NewReplacer("-", "", "_", "")

func foldTagKey(key string) string {
	return strings.ToLower(tagKeySeparators.Replace(key))
}

// oneEdit reports whether a and b differ by exactly one inserted, deleted or
// replaced byte, or by two neighbouring bytes swapped.
func oneEdit(a, b string) bool {
	switch len(a) - len(b) {
	case 1:
		return deletesTo(a, b)
	case -1:
		return deletesTo(b, a)
	case 0:
		i := 0
		for i < len(a) && a[i] == b[i] {
			i++
		}
		switch {
		case i == len(a):
			return false
		case a[i+1:] == b[i+1:]:
			return true
		}
		return i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
	}
	return false
}

// deletesTo reports whether deleting one byte of long, which is one byte
// longer than short, gives short.
func deletesTo(long, short string) bool {
	i := 0
	for i < len(short) && long[i] == short[i] {
		i++
	}
	return long[i+1:] == short[i:]
}
