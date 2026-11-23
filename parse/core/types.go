package core

import "strings"

// FieldType represents the data type of a field in filter and patch schemas.
type FieldType string

const (
	FieldTypeString    FieldType = "string"
	FieldTypeInt       FieldType = "int"
	FieldTypeFloat     FieldType = "float"
	FieldTypeDecimal   FieldType = "decimal"
	FieldTypeBool      FieldType = "bool"
	FieldTypeTimestamp FieldType = "timestamp"
	FieldTypeDate      FieldType = "date"
	FieldTypeTime      FieldType = "time"
	FieldTypeInterval  FieldType = "interval"
	FieldTypeInet      FieldType = "inet"
	FieldTypeCIDR      FieldType = "cidr"
	FieldTypeMACAddr   FieldType = "macaddr"
	FieldTypeUUID      FieldType = "uuid"
	FieldTypeJSON      FieldType = "json"
	FieldTypeArray     FieldType = "array"
	FieldTypeObject    FieldType = "object"
	FieldTypeAny       FieldType = "any"
	FieldTypeGeo       FieldType = "geo"
)

// IsKeyType reports a type a key column may have, and a relation, a key
// set or a membership may compare by: a scalar, whose equality tells rows
// apart. A document, an array, a geometry or an opaque value has none to
// identify a row by.
func IsKeyType(t FieldType) bool {
	switch t {
	case FieldTypeString, FieldTypeUUID, FieldTypeInt, FieldTypeFloat, FieldTypeDecimal, FieldTypeBool,
		FieldTypeTimestamp, FieldTypeDate, FieldTypeTime, FieldTypeInterval, FieldTypeInet, FieldTypeCIDR, FieldTypeMACAddr:
		return true
	}
	return false
}

// SQLType declares a resource column's storage role and any dialect read cast.
// JSON and native arrays remain distinct even when their Go shapes coincide.
type SQLType string

const (
	SQLDate          SQLType = "date"
	SQLInet          SQLType = "inet"
	SQLCIDR          SQLType = "cidr"
	SQLInterval      SQLType = "interval"
	SQLInetArray     SQLType = "inet[]"
	SQLCIDRArray     SQLType = "cidr[]"
	SQLIntervalArray SQLType = "interval[]"
	SQLJSON          SQLType = "json"
	SQLJSONB         SQLType = "jsonb"
	SQLTextArray     SQLType = "text[]"
	SQLInt32Array    SQLType = "integer[]"
	SQLUUIDArray     SQLType = "uuid[]"
)

// Valid reports whether t is one of the SQLType constants.
func (t SQLType) Valid() bool {
	switch t {
	case SQLDate, SQLInet, SQLCIDR, SQLInterval, SQLInetArray, SQLCIDRArray, SQLIntervalArray,
		SQLJSON, SQLJSONB, SQLTextArray, SQLInt32Array, SQLUUIDArray:
		return true
	}
	return false
}

// IsArray reports a declared native SQL array storage role.
func (t SQLType) IsArray() bool { return strings.HasSuffix(string(t), "[]") }

// IsJSON reports a declared JSON document storage role, including scalar JSON.
func (t SQLType) IsJSON() bool { return t == SQLJSON || t == SQLJSONB }
