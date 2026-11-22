package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// tagKeys are the struct tag keys config reads.
var tagKeys = []string{"alias", "default", "desc", "env", "envref", "example", "format", "koanf", "reload", "secret", "sensitive", "sep", "validate"}

// misspeltTagKey is a key on a field's tag that config does not read, so
// close to one it does that it most likely means that one.
type misspeltTagKey struct {
	declaring reflect.Type
	field     string
	written   string
	meant     string
}

func (m misspeltTagKey) err() error {
	return fmt.Errorf("%w: field %s of %s has tag key %q; did you mean %q? Name a key another library reads with WithForeignTagKeys", ErrSchema, m.field, typeText(m.declaring), m.written, m.meant)
}

// misspeltTagKeys lists the misspelt keys of the fields shape's view
// selects, through the structs it squashes.
func misspeltTagKeys(shape *gowalker.Shape) []misspeltTagKey {
	var found []misspeltTagKey
	for _, d := range shape.Declarations() {
		if d.State == gowalker.Skipped {
			continue
		}
		// Compiling the view already refused a tag TagKeys cannot read.
		keys, _ := gowalker.TagKeys(d.GoField.Tag)
		for _, key := range keys {
			if meant, ok := meantTagKey(key); ok {
				found = append(found, misspeltTagKey{declaring: d.DeclaringType, field: d.GoField.Name, written: key, meant: meant})
			}
		}
	}
	return found
}

// meantTagKey returns the key config reads that written most likely
// misspells: one equal to it ignoring case, '-' and '_', or one character
// inserted, deleted, replaced or swapped with its neighbour away from it.
func meantTagKey(written string) (string, bool) {
	if slices.Contains(tagKeys, written) {
		return "", false
	}
	folded := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(written))
	for _, key := range tagKeys {
		if folded == key || oneEdit(folded, key) {
			return key, true
		}
	}
	return "", false
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

// loadSchema is rootSchema for a load or a key check: it also refuses the
// misspelt tag keys of t's graph, but for those o names as another library's.
func loadSchema(t reflect.Type, o *options) (*schemaNode, error) {
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, m := range schemaMetadata(t).misspelt {
		if !slices.Contains(o.foreignTagKeys, m.written) {
			errs = append(errs, m.err())
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return root, nil
}
