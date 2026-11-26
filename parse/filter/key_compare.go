package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// storedCanonical reports a key type whose stored value is its canonical
// value on every backend: its columns compare, order and cast to text as
// its keys do, so a statement reads them as they are.
func storedCanonical(t core.FieldType) bool {
	switch t {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
		return true
	}
	return false
}

// keyField is a key column, read as expr, typed t.
func keyField(name, expr string, t core.TypeInfo) *FieldSchema {
	field := &FieldSchema{ExternalName: name, Column: name, Expr: expr, DataType: t.Type, NumberKind: t.NumberKind}
	return field
}

// keyOperand is what a key column, read as field f, compares by: the
// operand a filter's equality on f compares — a decimal through
// Dialect.DecimalOrder, so "1.50" meets 1.5, and a date by its calendar
// day — FieldExpr otherwise. Joins, key sets and memberships compare both
// sides through it, as filters compare a field with its value.
func keyOperand(d Dialect, f *FieldSchema) string {
	if f.DataType == core.FieldTypeDecimal {
		return d.DecimalOrder(fieldDecimal(d, f))
	}
	return FieldExpr(d, f)
}

// KeyOperand renders what a key column, read as expr and typed t, compares
// by (keyOperand).
func (d PostgresDialect) KeyOperand(expr string, t core.TypeInfo) string {
	return keyOperand(d, keyField("", expr, t))
}

// KeyOperand renders what a key column, read as expr and typed t, compares
// by (keyOperand).
func (d SQLiteDialect) KeyOperand(expr string, t core.TypeInfo) string {
	return keyOperand(d, keyField("", expr, t))
}

// keyValueText renders a key column, read as expr and typed t, as its
// canonical text (core.KeyValueText): a string, UUID or integer column's
// text, a network value's display form (ReadColumn), and otherwise the text
// a terms facet keys the value by (termsKeyExpr).
func keyValueText(d Dialect, expr string, t core.TypeInfo) string {
	switch t.Type {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
		return d.textKey(expr)
	case core.FieldTypeInet, core.FieldTypeCIDR:
		return d.textKey(d.ReadColumn(expr, t.SQLType))
	}
	return termsKeyExpr(keyField("", expr, t), d)
}

// KeyOrder renders what orders a key column, read as expr and typed t, in
// CompareKeyValues' order on every dialect: the keyset's order (text by
// code point, decimals through Dialect.DecimalOrder, a date by its
// calendar day), and for an interval or a network value, whose stored
// order is not its canonical text's, that text by code point. A statement
// that locks rows in key order orders by it, so its order and a sort in Go
// agree.
func KeyOrder(d Dialect, expr string, t core.TypeInfo) string {
	switch t.Type {
	case core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR:
		return d.CodePointOrder(keyValueText(d, expr, t))
	}
	return keyExpr(d, CursorKey{Expr: expr, DataType: t.Type, NumberKind: t.NumberKind})
}

// correlation equates the key columns of left with those of right position
// by position, each qualified by its alias and typed by types, compared as
// a filter compares the type (keyOperand): a key of several columns
// correlates as all of its columns.
func correlation(d Dialect, leftAlias string, left []string, rightAlias string, right []string, types []core.TypeInfo) string {
	terms := make([]string, len(left))
	for i := range left {
		l := keyField(left[i], qualified(d, leftAlias, left[i]), types[i])
		r := keyField(right[i], qualified(d, rightAlias, right[i]), types[i])
		terms[i] = keyOperand(d, l) + " = " + keyOperand(d, r)
	}
	return strings.Join(terms, " AND ")
}

// keySetOn is a key set's columns, each read as read renders it, and its keys
// with each value normalized as its column's type (core.NormalizeKey).
type keySetOn struct {
	table  string
	fields []*FieldSchema
	keys   []core.Key
}

// aliasColumns reads a column as qualified by alias, or bare without one.
func aliasColumns(d Dialect, alias string) func(column string) string {
	return func(column string) string { return columnSQL(d, alias, column, "") }
}

func newKeySet(table string, read func(column string) string, columns []string, types []core.TypeInfo, keys []core.Key) (keySetOn, error) {
	if len(columns) == 0 || len(types) != len(columns) {
		return keySetOn{}, fmt.Errorf("key set on %s types %d of its %d columns", table, len(types), len(columns))
	}
	fields := make([]*FieldSchema, len(columns))
	for i, c := range columns {
		if !core.IsKeyType(types[i].Type) {
			return keySetOn{}, fmt.Errorf("key set on %s: column %s is %s; a key set compares scalar columns", table, c, types[i].Type)
		}
		fields[i] = keyField(c, read(c), types[i])
	}
	normalized := make([]core.Key, len(keys))
	for i, k := range keys {
		n, err := core.NormalizeKey(types, k)
		if err != nil {
			valueErr, ok := errors.AsType[*core.KeyValueError](err)
			if !ok {
				return keySetOn{}, fmt.Errorf("key set on %s: key %d: %w", table, i, err)
			}
			return keySetOn{}, fmt.Errorf("key set on %s: key %d: column %s: %w", table, i, columns[valueErr.Index], valueErr.Err)
		}
		normalized[i] = n
	}
	set := keySetOn{table: table, fields: fields, keys: normalized}
	return set, nil
}

// row is the set's columns as compared (keyOperand): one column's operand,
// several columns' parenthesized row value.
func (s keySetOn) row(d Dialect) string {
	operands := make([]string, len(s.fields))
	for i, f := range s.fields {
		operands[i] = keyOperand(d, f)
	}
	if len(operands) == 1 {
		return operands[0]
	}
	return "(" + strings.Join(operands, ", ") + ")"
}

// position is the set's keys' values in column i.
func (s keySetOn) position(i int) []any {
	out := make([]any, len(s.keys))
	for k, key := range s.keys {
		out[k] = key[i]
	}
	return out
}

// KeyIn renders the key columns of table, each qualified by alias and
// typed by types, as a row IN keys — NOT IN when member is false — the
// keys bound as one parameter whatever their number. A column and each
// key's value compare as a filter compares the column's type (keyOperand):
// a decimal by value, a date by its calendar day. One column compares
// `= ANY` its typed array, as `$in` does; several read the keys back as
// rows of table's own row type (jsonb_populate_recordset), so each
// position takes its column's type and the key index serves the semi-join.
func (d PostgresDialect) KeyIn(table, alias string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error) {
	return d.keyIn(table, aliasColumns(d, alias), columns, types, keys, member, bind)
}

// keyIn is KeyIn with each column read as read renders it. table, which
// may name its schema, types the keys of several columns.
func (d PostgresDialect) keyIn(table string, read func(column string) string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error) {
	s, err := newKeySet(table, read, columns, types, keys)
	if err != nil {
		return "", err
	}
	if len(s.fields) == 1 {
		param, err := pgSetParam(s.fields[0], s.position(0), bind)
		if err != nil {
			return "", fmt.Errorf("key set on %s: %w", table, err)
		}
		if member {
			return s.row(d) + " = ANY(" + param + ")", nil
		}
		return s.row(d) + " <> ALL(" + param + ")", nil
	}
	doc := make([]map[string]any, len(s.keys))
	for i, key := range s.keys {
		doc[i] = make(map[string]any, len(columns))
		for j, c := range columns {
			doc[i][c] = key[j]
		}
	}
	arr, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("key set on %s: %w", table, err)
	}
	selected := make([]string, len(s.fields))
	for i, f := range s.fields {
		selected[i] = keyOperand(d, keyField(f.ExternalName, qualified(d, "k", f.ExternalName), core.TypeInfo{Type: f.DataType, NumberKind: f.NumberKind}))
	}
	return s.row(d) + keyInKeyword(member) + "(SELECT " + strings.Join(selected, ", ") + " FROM jsonb_populate_recordset(NULL::" + quoteTable(d, table) +
		", " + bind(string(arr)) + ") AS " + d.QuoteIdent("k") + ")", nil
}

// pgSetParam binds a set's members as the array `= ANY` compares f with:
// a decimal's as numeric from their canonical text, any other type's as
// its typed array (pgArray).
func pgSetParam(f *FieldSchema, members []any, bind func(any) string) (string, error) {
	if f.DataType == core.FieldTypeDecimal {
		arr, err := typedSlice(members, core.DecimalText)
		if err != nil {
			return "", fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return bind(arr) + "::text[]::numeric[]", nil
	}
	arr, err := pgFieldArray(f, f.DataType, members)
	if err != nil {
		return "", err
	}
	return bind(arr), nil
}

// KeyIn renders the key columns of table, each qualified by alias and
// typed by types, as a row IN keys — NOT IN when member is false — the
// keys bound as one JSON parameter whatever their number, read back by
// json_each. A column and each key's value compare as a filter compares
// the column's type (keyOperand): a decimal through DecimalOrder on both
// sides, a date by its calendar day. An unsigned key past SQLite's signed
// integers is refused.
func (d SQLiteDialect) KeyIn(table, alias string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error) {
	return d.keyIn(table, aliasColumns(d, alias), columns, types, keys, member, bind)
}

// keyIn is KeyIn with each column read as read renders it.
func (d SQLiteDialect) keyIn(table string, read func(column string) string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error) {
	s, err := newKeySet(table, read, columns, types, keys)
	if err != nil {
		return "", err
	}
	positions := make([][]any, len(s.fields))
	for i, f := range s.fields {
		values, err := sqliteSetMembers(f, s.position(i))
		if err != nil {
			return "", fmt.Errorf("key set on %s: %w", table, err)
		}
		positions[i], _ = core.AnySlice(values)
	}
	var doc any
	operands := make([]string, len(s.fields))
	if len(s.fields) == 1 {
		doc = positions[0]
		operands[0] = sqliteSetOperand(d, s.fields[0], "value")
	} else {
		rows := make([][]any, len(s.keys))
		for k := range s.keys {
			rows[k] = make([]any, len(s.fields))
			for i := range s.fields {
				rows[k][i] = positions[i][k]
			}
		}
		doc = rows
		for i, f := range s.fields {
			operands[i] = sqliteSetOperand(d, f, "json_extract(value, '$["+strconv.Itoa(i)+"]')")
		}
	}
	arr, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("key set on %s: %w", table, err)
	}
	return s.row(d) + keyInKeyword(member) + "(SELECT " + strings.Join(operands, ", ") + " FROM json_each(" + bind(string(arr)) + "))", nil
}

// sqliteSetOperand is a set member, read as expr from its JSON array, as
// f's operand compares it (keyOperand): a decimal's canonical text through
// DecimalOrder, any other member as json_each reads it.
func sqliteSetOperand(d SQLiteDialect, f *FieldSchema, expr string) string {
	if f.DataType == core.FieldTypeDecimal {
		return d.DecimalOrder(DecimalParam(expr))
	}
	return expr
}

func keyInKeyword(member bool) string {
	if member {
		return " IN "
	}
	return " NOT IN "
}

// StoredKeyValue is a key column's canonical value (core.NormalizeScalar)
// as SQLite holds it: its canonical text, a timestamp's to the nanosecond.
func (SQLiteDialect) StoredKeyValue(_ core.TypeInfo, v any) (any, error) { return v, nil }

// StoredKeyValue is a key column's canonical value (core.NormalizeScalar)
// as PostgreSQL holds it: a timestamp to the microsecond, rounded as
// PostgreSQL rounds the canonical text it reads, the fraction parsed as a
// double and scaled with rint, ties to even. Two timestamps PostgreSQL
// holds equal have one value. A time of day is canonical at microseconds
// already (core.ParseLocalTime).
func (PostgresDialect) StoredKeyValue(t core.TypeInfo, v any) (any, error) {
	if t.Type != core.FieldTypeTimestamp {
		return v, nil
	}
	text, ok := v.(string)
	at, err := time.Parse(time.RFC3339Nano, text)
	if !ok || err != nil || core.FormatTimestamp(at) != text {
		return nil, fmt.Errorf("timestamp key %v is not canonical text", v)
	}
	// The fraction with its point and nine digits, as PostgreSQL parses it.
	const seconds = len("2006-01-02T15:04:05")
	f, err := strconv.ParseFloat(text[seconds:len(text)-1], 64)
	if err != nil {
		return nil, fmt.Errorf("timestamp key %q: %w", text, err)
	}
	micros := time.Duration(math.RoundToEven(f*1e6)) * time.Microsecond
	return core.FormatTimestamp(at.Truncate(time.Second).Add(micros)), nil
}
