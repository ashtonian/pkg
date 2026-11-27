package parse

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Key is a row's key: a value per key column, in the order PrimaryKey
// names the columns. A one-column key holds one value.
type Key = core.Key

// ownerKeys are keys of r's rows as read back (Key), normalized
// (keyColumns.normalize) before statements over the rows' relations bind
// them, so they bind as the rows' targets did.
func (r *resourceCore) ownerKeys(keys []Key) ([]core.Key, error) {
	kc := r.keyColumns()
	out := make([]core.Key, len(keys))
	for i, k := range keys {
		normalized, err := kc.normalize(k)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: owner key %d: %w", r.table, i, err)
		}
		out[i] = normalized
	}
	return out, nil
}

// keyColumns are a resource's key columns and their types: what a key is
// normalized, compared and ordered by.
type keyColumns struct {
	names []string
	types []core.TypeInfo
}

func (r *resourceCore) keyColumns() keyColumns {
	kc := keyColumns{names: r.pk, types: make([]core.TypeInfo, len(r.pk))}
	for i, column := range r.pk {
		kc.types[i] = r.columnTypeInfo(column)
	}
	return kc
}

// columnTypeInfo is column's type with the storage it declares (SQLType),
// which reads a network value's canonical text.
func (r *resourceCore) columnTypeInfo(column string) core.TypeInfo {
	info := core.TypeOf(r.tableColumns[column])
	t, ok := r.sqlTypes[column]
	if ok {
		info.SQLType = t
	}
	return info
}

// normalize is key with each value in its column's canonical form
// (core.NormalizeKey), as a policy on the column normalizes it and as patch
// binds a relation's keys; ErrKeyInvalid when key holds another number of
// values than there are columns, or a column cannot hold its value.
func (kc keyColumns) normalize(key Key) (Key, error) {
	out, err := core.NormalizeKey(kc.types, key)
	if err != nil {
		return nil, kc.keyError(ErrKeyInvalid, err)
	}
	return out, nil
}

// keyError is a core.NormalizeKey refusal as sentinel: a key of another
// length naming every column, a value a key of several columns refused
// naming its column (columnError), a one-column key's value as it is.
func (kc keyColumns) keyError(sentinel, err error) error {
	valueErr, ok := errors.AsType[*core.KeyValueError](err)
	if ok && len(kc.names) == 1 {
		return fmt.Errorf("%w: %w", sentinel, valueErr.Err)
	}
	return kc.columnError(sentinel, err)
}

// columnError is a core.NormalizeKey refusal as sentinel, naming the key
// column whose value it refused.
func (kc keyColumns) columnError(sentinel, err error) error {
	valueErr, ok := errors.AsType[*core.KeyValueError](err)
	if !ok {
		return fmt.Errorf("%w: %w (%s)", sentinel, err, strings.Join(kc.names, ", "))
	}
	return fmt.Errorf("%w: key column %s: %w", sentinel, kc.names[valueErr.Index], valueErr.Err)
}

// requested is a key a caller names a row by — a target's, a batch row's —
// normalized, its texts valid UTF-8 without U+0000
// (core.CheckText): a backend compares a key's text as written, and
// PostgreSQL refuses either in a parameter. ErrKeyInvalid otherwise. Keys
// read from stored rows are normalized without the check.
func (kc keyColumns) requested(key Key) (Key, error) {
	out, err := kc.normalize(key)
	if err != nil {
		return nil, err
	}
	for i, v := range out {
		err = core.CheckValueText(v)
		if err != nil {
			if len(kc.names) == 1 {
				return nil, fmt.Errorf("%w: %w", ErrKeyInvalid, err)
			}
			return nil, fmt.Errorf("%w: key column %s: %w", ErrKeyInvalid, kc.names[i], err)
		}
	}
	return out, nil
}

// stored is a normalized key as d's database holds it
// (filter.Dialect.StoredKeyValue): a timestamp at the database's precision.
func (kc keyColumns) stored(d filter.Dialect, key Key) (Key, error) {
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

// compare orders two keys column by column as a statement ordered by
// filter.KeyOrder reads them, each column in its type's one order
// (core.CompareCanonical): strings by code point, numbers and decimals by
// value, timestamps by instant, false before true.
func (kc keyColumns) compare(a, b Key) (int, error) {
	for i, info := range kc.types {
		c, err := core.CompareCanonical(info, a[i], b[i])
		if err != nil {
			return 0, fmt.Errorf("%w: key column %s: %w", ErrKeyInvalid, kc.names[i], err)
		}
		if c != 0 {
			return c, nil
		}
	}
	return 0, nil
}

// order is the ORDER BY list that reads the key columns of table in
// compare's order (filter.KeyOrder).
func (kc keyColumns) order(d filter.Dialect, table string) string {
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

// ParseKey reads a key from its columns' texts, one per key column in key
// order, as an item route's path segments carry them (parsehttp.ItemKey).
// An integer column's text is decimal digits with an optional minus sign, a
// float column's a JSON number, a bool column's what strconv.ParseBool
// reads, as a filter's query parameter is read, and every other column's
// its value's text: a string as itself, a UUID, decimal, date, time,
// timestamp, interval or network address in any spelling NormalizeScalar
// reads. The key is then normalized as ByKey's is, so ParseKey's key and
// ByKey's name a row alike. A text its column cannot hold is ErrNoSuchKey
// naming the column; another number of texts than key columns is the
// caller's ErrKeyInvalid.
func (r *resourceCore) ParseKey(texts ...string) (Key, error) {
	kc := r.keyColumns()
	if len(texts) != len(kc.names) {
		return nil, fmt.Errorf("parse: %s: %w: %d texts for the %d key columns (%s)",
			r.table, ErrKeyInvalid, len(texts), len(kc.names), strings.Join(kc.names, ", "))
	}
	key := make(Key, len(texts))
	for i, text := range texts {
		err := core.CheckText(text)
		if err == nil {
			key[i], err = scalarText(kc.types[i], text)
		}
		if err != nil {
			return nil, fmt.Errorf("parse: %s: %w: key column %s: %w", r.table, ErrNoSuchKey, kc.names[i], err)
		}
	}
	normalized, err := core.NormalizeKey(kc.types, key)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, kc.columnError(ErrNoSuchKey, err))
	}
	return normalized, nil
}

// scalarText is text as a column of type info holds it.
func scalarText(info core.TypeInfo, text string) (any, error) {
	switch info.Type {
	case core.FieldTypeInt:
		return integerText(info.NumberKind, text)
	case core.FieldTypeFloat:
		// As a JSON number the text has no hexadecimal, NaN or infinity, and
		// it is read at the column's own precision.
		return core.NormalizeScalar(info.Type, info.NumberKind, json.Number(text))
	case core.FieldTypeBool:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", text)
		}
		return b, nil
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeDecimal, core.FieldTypeDate, core.FieldTypeTime,
		core.FieldTypeTimestamp, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		return core.NormalizeScalar(info.Type, info.NumberKind, text)
	}
	return nil, fmt.Errorf("a %s column has no text form", info.Type)
}

// integerText is text as an integer column of kind holds it: decimal digits
// with an optional minus sign, the text an integer key is written as, and no
// other spelling of it.
func integerText(kind reflect.Kind, text string) (any, error) {
	var (
		n   any
		err error
	)
	if strings.HasPrefix(text, "-") {
		n, err = strconv.ParseInt(text, 10, 64)
	} else {
		n, err = strconv.ParseUint(text, 10, 64)
	}
	switch {
	case errors.Is(err, strconv.ErrRange):
		return nil, fmt.Errorf("%s overflows %s", text, kind)
	case err != nil:
		return nil, fmt.Errorf("%q is not a decimal integer", text)
	}
	return core.NormalizeScalar(core.FieldTypeInt, kind, n)
}
