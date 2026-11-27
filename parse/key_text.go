package parse

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrNoSuchKey is ParseKey's refusal of a key's text: text a key column
// cannot hold, such as letters for an integer column, or text holding
// U+0000 or invalid UTF-8 (core.CheckText). The text is the request's, an
// item route's path segment, and no row has that key, so answer it as the
// row not found. ErrKeyInvalid, unlike it, is the server's.
var ErrNoSuchKey = errors.New("no key of the key columns has this text")

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
