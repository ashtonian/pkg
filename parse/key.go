package parse

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// Key is a row's key: a value per key column, in the order PrimaryKey
// names the columns. A one-column key holds one value.
type Key = core.Key

// keyColumns are a resource's key columns and their types: what a key is
// normalized, compared and ordered by.
type keyColumns struct {
	names []string
	types []core.TypeInfo
}

func (r *Resource[T]) keyColumns() keyColumns {
	kc := keyColumns{names: r.pk, types: make([]core.TypeInfo, len(r.pk))}
	for i, column := range r.pk {
		kc.types[i] = core.TypeOf(r.tableColumns[column])
	}
	return kc
}

// valueTypes are the column types a key of several columns, and every
// column of a key an update batch names, may have: the types every backend
// compares by value and filter's key sets bind (PolicyConstraint.Fields).
var valueTypes = map[core.FieldType]bool{
	core.FieldTypeString: true,
	core.FieldTypeUUID:   true,
	core.FieldTypeInt:    true,
}

// normalize is key with each value in its column's canonical form, as a
// policy on the column normalizes it; ErrKeyInvalid when key holds another
// number of values than there are columns, or a column cannot hold its
// value.
func (kc keyColumns) normalize(key Key) (Key, error) {
	if len(key) != len(kc.names) {
		return nil, fmt.Errorf("%w: %d values for the %d key columns (%s)", ErrKeyInvalid, len(key), len(kc.names), strings.Join(kc.names, ", "))
	}
	out := make(Key, len(key))
	for i, info := range kc.types {
		v, err := core.NormalizeScalar(info.Type, info.NumberKind, key[i])
		switch {
		case err != nil && len(kc.names) == 1:
			return nil, fmt.Errorf("%w: %w", ErrKeyInvalid, err)
		case err != nil:
			return nil, fmt.Errorf("%w: key column %s: %w", ErrKeyInvalid, kc.names[i], err)
		}
		out[i] = v
	}
	return out, nil
}

// identity is key, normalized, as a comparable map key: equal for two keys
// exactly when the database holds them equal. A one-column key is its
// value; a longer one its values' JSON text, canonical for the strings and
// integers such a key holds (valueTypes).
func (kc keyColumns) identity(key Key) (any, error) {
	normalized, err := kc.normalize(key)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 1 {
		if !reflect.ValueOf(normalized[0]).Comparable() {
			return nil, fmt.Errorf("key %T is not comparable", normalized[0])
		}
		return normalized[0], nil
	}
	text, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encoding key: %w", err)
	}
	return string(text), nil
}

// batchType refuses a key an update batch cannot order and compare: one
// with a column of a type outside valueTypes.
func (kc keyColumns) batchType() error {
	for i, info := range kc.types {
		if !valueTypes[info.Type] {
			return fmt.Errorf("update batch key column %s is %s; a batch keys string, UUID and integer columns", kc.names[i], info.Type)
		}
	}
	return nil
}

// compare orders two normalized keys of batch-typed columns (batchType)
// column by column: strings by code point, as Dialect.CodePointOrder orders
// the column, and integers by value.
func (kc keyColumns) compare(a, b Key) int {
	for i := range kc.names {
		var c int
		switch x := a[i].(type) {
		case string:
			y, _ := b[i].(string)
			c = strings.Compare(x, y)
		case int64:
			y, _ := b[i].(int64)
			c = cmp.Compare(x, y)
		case uint64:
			y, _ := b[i].(uint64)
			c = cmp.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

// checkKeyColumns refuses a key of no column, a key column listed twice,
// one that is no column of model, and, in a key of several columns, one
// whose type is outside valueTypes.
func checkKeyColumns(key []string, columns map[string]reflect.Type, model reflect.Type) error {
	if len(key) == 0 {
		return errors.New("the key names no column; PrimaryKey takes one or more")
	}
	for i, column := range key {
		if slices.Contains(key[:i], column) {
			return fmt.Errorf("key column %q is listed twice", column)
		}
		t, ok := columns[column]
		if !ok {
			return fmt.Errorf("key column %q is not a column of %s", column, model)
		}
		if info := core.TypeOf(t); len(key) > 1 && !valueTypes[info.Type] {
			return fmt.Errorf("key column %q is %s; a key of several columns holds string, UUID and integer columns", column, info.Type)
		}
	}
	return nil
}
