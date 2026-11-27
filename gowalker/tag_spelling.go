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

// TagMisspelling is a tag key on a declared field that is not one of a
// library's keys but most likely misspells one ([MisspeltTagKey]).
type TagMisspelling struct {
	// Declaration is the field whose tag holds the key.
	Declaration
	// Written is the key the tag holds, and Meant the known key it most
	// likely means.
	Written, Meant string
}

// MisspeltTagKeys holds the tag keys of every field [Shape.Declarations]
// lists to known, the keys a library reads, and returns each that
// [MisspeltTagKey] finds misspells one, in declaration and then tag order,
// with its declaration; nil when there is none. It checks every declaration,
// skipped and private ones included, and words no error: which states to
// hold to known, and which keys to leave alone because another library reads
// them, stay the caller's, who filters the result by State, DeclaringType or
// Written and words the refusal. It copies only the declarations it returns.
func (s *Shape) MisspeltTagKeys(known []string) []TagMisspelling {
	var found []TagMisspelling
	for i := range s.declarations {
		d := &s.declarations[i]
		for _, key := range d.keys {
			meant, ok := MisspeltTagKey(key, known)
			if ok {
				found = append(found, TagMisspelling{Declaration: d.public(), Written: key, Meant: meant})
			}
		}
	}
	return found
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
