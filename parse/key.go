package parse

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Key is a row's key: a value per key column, in the order PrimaryKey
// names the columns. A one-column key holds one value.
type Key = core.Key

// ownerKeys are keys of r's rows as read back (Key), checked to hold a
// value per key column before statements over the rows' relations bind
// them.
func (r *Resource[T]) ownerKeys(keys []Key) ([]core.Key, error) {
	for i, k := range keys {
		if len(k) != len(r.pk) {
			return nil, fmt.Errorf("parse: %s: owner key %d: %w: %d values for the %d key columns (%s)",
				r.table, i, ErrKeyInvalid, len(k), len(r.pk), strings.Join(r.pk, ", "))
		}
	}
	return keys, nil
}

// keyColumns are a resource's key columns and their types: what a key is
// normalized, compared and ordered by.
type keyColumns struct {
	names []string
	types []core.TypeInfo
}

func (r *Resource[T]) keyColumns() keyColumns {
	kc := keyColumns{names: r.pk, types: make([]core.TypeInfo, len(r.pk))}
	for i, column := range r.pk {
		kc.types[i] = r.columnTypeInfo(column)
	}
	return kc
}

// columnTypeInfo is column's type with the storage it declares (SQLType),
// which reads a network value's canonical text.
func (r *Resource[T]) columnTypeInfo(column string) core.TypeInfo {
	info := core.TypeOf(r.tableColumns[column])
	t, ok := r.sqlTypes[column]
	if ok {
		info.SQLType = t
	}
	return info
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

// requested is a key a caller names a row by — a target's, a batch row's,
// an item route's texts — normalized, its texts valid UTF-8 without U+0000
// (core.CheckText): a backend compares a key's text as written, and
// PostgreSQL refuses either in a parameter. ErrKeyInvalid otherwise. Keys
// read from stored rows are normalized without the check.
func (kc keyColumns) requested(key Key) (Key, error) {
	out, err := kc.normalize(key)
	if err != nil {
		return nil, err
	}
	for i, v := range out {
		if err := core.CheckValueText(v); err != nil {
			if len(kc.names) == 1 {
				return nil, fmt.Errorf("%w: %w", ErrKeyInvalid, err)
			}
			return nil, fmt.Errorf("%w: key column %s: %w", ErrKeyInvalid, kc.names[i], err)
		}
	}
	return out, nil
}

// stored is a normalized key as d's database holds it
// (Dialect.StoredKeyValue): a timestamp at the database's precision.
func (kc keyColumns) stored(d Dialect, key Key) (Key, error) {
	out := make(Key, len(key))
	for i, info := range kc.types {
		v, err := d.StoredKeyValue(info, key[i])
		if err != nil {
			return nil, fmt.Errorf("%w: key column %s: %w", ErrKeyInvalid, kc.names[i], err)
		}
		out[i] = v
	}
	return out, nil
}

// identity is key, normalized, as a map key: its canonical text
// (core.KeyTextOf), equal for two keys exactly when the database holds
// them equal — decimals by value, timestamps at the instant, -0 as 0.
// Keys bound for one database compare there at its precision
// (keyColumns.stored).
func (kc keyColumns) identity(key Key) (any, error) {
	normalized, err := kc.normalize(key)
	if err != nil {
		return nil, err
	}
	text, err := core.KeyTextOf(kc.types, normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyInvalid, err)
	}
	return text, nil
}

// compare orders two normalized keys column by column as a statement
// ordered by filter.KeyOrder reads them (filter.CompareKeyValues): strings
// by code point, numbers and decimals by value, timestamps by instant,
// false before true.
func (kc keyColumns) compare(a, b Key) int {
	for i, info := range kc.types {
		c := filter.CompareKeyValues(filter.CursorKey{DataType: info.Type, NumberKind: info.NumberKind}, a[i], b[i])
		if c != 0 {
			return c
		}
	}
	return 0
}

// order is the ORDER BY list that reads the key columns of table in
// compare's order (filter.KeyOrder).
func (kc keyColumns) order(d Dialect, table string) string {
	order := make([]string, len(kc.names))
	for i, column := range kc.names {
		order[i] = filter.KeyOrder(d, d.QuoteIdent(table)+"."+d.QuoteIdent(column), kc.types[i])
	}
	return strings.Join(order, ", ")
}

// checkKeyColumns refuses a key of no column, a key column listed twice,
// one that is no column of model, and one of no scalar type
// (core.IsKeyType), whose values have no equality to identify a row by.
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
			return fmt.Errorf("key column %q is not a column of %s", column, gowalker.TypeText(model))
		}
		info := core.TypeOf(t)
		if !core.IsKeyType(info.Type) {
			return fmt.Errorf("key column %q is %s; a key column is a scalar", column, info.Type)
		}
	}
	return nil
}
