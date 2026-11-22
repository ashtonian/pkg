package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"

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
			if meant, ok := gowalker.MisspeltTagKey(key, tagKeys); ok {
				found = append(found, misspeltTagKey{declaring: d.DeclaringType, field: d.GoField.Name, written: key, meant: meant})
			}
		}
	}
	return found
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
