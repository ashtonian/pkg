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

// ForeignTagDeclarer is implemented by a struct type whose fields carry a
// tag key another library reads that sits close to one config reads: one
// character inserted, deleted, replaced or swapped away, or equal ignoring
// case, '-' and '_'. Config refuses such a key as a misspelling of its own,
// which it would otherwise ignore without a word, unless the struct
// declaring the field lists it. The list covers that struct's own fields,
// whatever reaches them: a type another package owns is covered by a
// defined type over it (type Section lib.Section) that lists its keys.
// ForeignTagKeys is called on a zero value, once per type.
type ForeignTagDeclarer interface {
	ForeignTagKeys() []string
}

var foreignTagDeclarerType = reflect.TypeFor[ForeignTagDeclarer]()

// misspeltTagKey is a key on a field's tag that config does not read, so
// close to one it does that it most likely means that one.
type misspeltTagKey struct {
	declaring reflect.Type
	field     string
	written   string
	meant     string
}

func (m misspeltTagKey) err() error {
	return fmt.Errorf("%w: field %s of %s has tag key %q; did you mean %q? List a key another library reads in %s's ForeignTagKeys method", ErrSchema, m.field, gowalker.TypeText(m.declaring), m.written, m.meant, gowalker.TypeText(m.declaring))
}

// misspeltTagKeys lists the misspelt keys of the fields shape's view
// selects, through the structs it squashes, but for those the declaring
// struct lists as another library's. foreign caches each declaring type's
// list across one graph.
func misspeltTagKeys(shape *gowalker.Shape, foreign map[reflect.Type][]string) []misspeltTagKey {
	var found []misspeltTagKey
	for _, d := range shape.Declarations() {
		if d.State == gowalker.Skipped {
			continue
		}
		for key := range d.TagKeys() {
			meant, ok := gowalker.MisspeltTagKey(key, tagKeys)
			if ok && !slices.Contains(foreignTagKeys(d.DeclaringType, foreign), key) {
				found = append(found, misspeltTagKey{declaring: d.DeclaringType, field: d.GoField.Name, written: key, meant: meant})
			}
		}
	}
	return found
}

func foreignTagKeys(t reflect.Type, cache map[reflect.Type][]string) []string {
	keys, ok := cache[t]
	if !ok {
		if reflect.PointerTo(t).Implements(foreignTagDeclarerType) {
			keys = reflect.New(t).Interface().(ForeignTagDeclarer).ForeignTagKeys()
		}
		cache[t] = keys
	}
	return keys
}

// misspeltError joins the refusals of every misspelt key, nil for none.
func misspeltError(found []misspeltTagKey) error {
	errs := make([]error, 0, len(found))
	for _, m := range found {
		errs = append(errs, m.err())
	}
	return errors.Join(errs...)
}

// loadSchema is rootSchema for a load or a key check: when the default
// validator judges the load, it also refuses the validate tags that
// validator cannot read.
func loadSchema(t reflect.Type, o *options) (*schemaNode, error) {
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	if o.validator == nil && !o.skipValid {
		if err := defaultRules(t); err != nil {
			return nil, err
		}
	}
	return root, nil
}
