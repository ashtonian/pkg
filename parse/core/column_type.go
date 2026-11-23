package core

import (
	"fmt"
	"maps"
)

// WithColumnTypes supplies physical storage declarations keyed by column name.
// The generator owns a copy of the map.
func WithColumnTypes(types map[string]SQLType) GeneratorOption {
	return func(o *GeneratorOptions) { o.ColumnTypes = maps.Clone(types) }
}

// FieldType combines a field's Go representation with its declared storage.
// JSON scalar and scalar-slice fields use document operators, while typed
// objects retain their member schema and native arrays retain array semantics.
func (g *SchemaGenerator) FieldType(field *Field) (TypeInfo, error) {
	info := TypeOf(field.Type)
	if len(g.opts.ColumnTypes) == 0 || field.Tags.Relation != "" {
		return info, nil
	}
	column, ok, err := g.FieldColumn(field)
	if err != nil {
		return TypeInfo{}, err
	}
	if !ok {
		return info, nil
	}
	storage, declared := g.opts.ColumnTypes[column]
	if !declared {
		return info, nil
	}
	if !storage.Valid() {
		return TypeInfo{}, fmt.Errorf("column %q declares type %q, which has no read rule", column, storage)
	}
	if storage == SQLDate && info.Type != FieldTypeDate {
		return TypeInfo{}, fmt.Errorf("column %q declares a civil date; use core.Date, not %s", column, field.Type)
	}
	if err := CheckValueStorage(column, info, storage); err != nil {
		return TypeInfo{}, err
	}
	if storage.IsJSON() && !IsDocument(info.Type) {
		return TypeInfo{Type: FieldTypeJSON, SQLType: storage}, nil
	}
	info.SQLType = storage
	return info, nil
}

// valueStorage is the storage declaration a value type's column may make
// other than a JSON document: the SQLType PostgreSQL reads it through, or ""
// for a type whose column needs none. valueArrayStorage is the same for a
// native array of the type.
var (
	valueStorage = map[FieldType]SQLType{
		FieldTypeDate: SQLDate, FieldTypeInterval: SQLInterval, FieldTypeInet: SQLInet, FieldTypeCIDR: SQLCIDR,
		FieldTypeTime: "", FieldTypeMACAddr: "",
	}
	valueArrayStorage = map[FieldType]SQLType{
		FieldTypeInterval: SQLIntervalArray, FieldTypeInet: SQLInetArray, FieldTypeCIDR: SQLCIDRArray,
		FieldTypeTime: "", FieldTypeMACAddr: "",
	}
)

// IsValueType reports a value type with one canonical text (Date,
// LocalTime, Interval, Inet, CIDR, MACAddr), or a native array of one other
// than Date, whose column declarations CheckValueStorage governs.
func IsValueType(info TypeInfo) bool {
	if info.Type == FieldTypeArray {
		_, ok := valueArrayStorage[info.Elem]
		return ok
	}
	_, ok := valueStorage[info.Type]
	return ok
}

// CheckValueStorage refuses a declaration that disagrees with a value type
// or an array of one, such as SQLInet on a CIDR: other than a JSON type, the
// only declaration it takes is the one PostgreSQL reads it through.
func CheckValueStorage(column string, info TypeInfo, storage SQLType) error {
	if storage.IsJSON() || !IsValueType(info) {
		return nil
	}
	want, held := valueStorage[info.Type], string(info.Type)
	if info.Type == FieldTypeArray {
		want, held = valueArrayStorage[info.Elem], "an array of "+string(info.Elem)
	}
	switch {
	case storage == want:
		return nil
	case want == "":
		return fmt.Errorf("column %q declares %s, which %s does not use; declare none, or a JSON type", column, storage, held)
	}
	return fmt.Errorf("column %q declares %s; %s is stored as %s", column, storage, held, want)
}

// ImpliedSQLType is the declaration a column of a value type takes when it
// declares none: SQLDate for a Date, whose legacy SQLite text reads through
// it, and SQLInterval or SQLIntervalArray for an Interval or an array of
// them, which PostgreSQL then reads as ISO 8601 text under any
// IntervalStyle. Other types imply none.
func ImpliedSQLType(info TypeInfo) (SQLType, bool) {
	switch {
	case info.Type == FieldTypeDate:
		return SQLDate, true
	case info.Type == FieldTypeInterval:
		return SQLInterval, true
	case info.Type == FieldTypeArray && info.Elem == FieldTypeInterval:
		return SQLIntervalArray, true
	}
	return "", false
}
