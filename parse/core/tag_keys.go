package core

import (
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
)

// parseTagKeys are the struct tag keys parse reads on a walked field
// (readTags and readBounds), beside the generator's field-name and column
// tags.
var parseTagKeys = []string{
	"parse", "relation", "relation_name", "filter_ops", "patch_ops", "facet", "paths", "elements",
	"storage", "geo", "geo_srid", "geo_lat", "geo_lon", "indexed", "immutable",
	"label", "description", "group", "order", "sort_default", "search_weight",
	ConstraintEnum, ConstraintMinLen, ConstraintMaxLen, ConstraintMin, ConstraintMax, ConstraintMinItems, ConstraintMaxItems,
}

// misspeltTagKey is a key on a walked field's tag that parse does not read,
// so close to one it does that it most likely means that one.
type misspeltTagKey struct {
	declaring reflect.Type
	field     string
	written   string
	meant     string
}

func (m misspeltTagKey) err() error {
	return fmt.Errorf("field %s of %s has tag key %q; did you mean %q? Name a key another library reads with core.WithForeignTagKeys", m.field, m.declaring, m.written, m.meant)
}

// misspeltTagKeys lists the misspelt keys of a walked field's tag. The
// field-name and column tags are not compared: other libraries' keys sit one
// character from them, bson beside json.
func misspeltTagKeys(d gowalker.Declaration, k fieldsKey) []misspeltTagKey {
	var found []misspeltTagKey
	for key := range d.TagKeys() {
		if key == k.name || key == k.column {
			continue
		}
		if meant, ok := gowalker.MisspeltTagKey(key, parseTagKeys); ok {
			found = append(found, misspeltTagKey{declaring: d.DeclaringType, field: d.GoField.Name, written: key, meant: meant})
		}
	}
	return found
}
