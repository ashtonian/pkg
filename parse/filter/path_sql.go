package filter

import (
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
)

// FieldExpr is the SQL expression of a field's value: its column (a decimal
// column as the dialect's exact decimal, an interval as its canonical text,
// which compares field by field where PostgreSQL's interval equality equates
// a month with 30 days), or for a path the dialect's typed extraction.
func FieldExpr(d Dialect, f *FieldSchema) string {
	if f.Path != nil {
		return d.PathExpr(f.Path, f.DataType)
	}
	switch {
	case f.DataType == core.FieldTypeDate:
		return d.ReadColumn(f.SQLExpr, core.SQLDate)
	case f.DataType == core.FieldTypeDecimal:
		return d.DecimalColumn(f.SQLExpr)
	case f.DataType == core.FieldTypeInterval:
		return d.ReadColumn(f.SQLExpr, core.SQLInterval)
	case f.DataType == core.FieldTypeArray && f.Elem == core.FieldTypeInterval:
		return d.ReadColumn(f.SQLExpr, core.SQLIntervalArray)
	}
	return f.SQLExpr
}

const (
	timestampRegex = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`
	uuidRegex      = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
)

// maxDecimalMember is decimals.MaxMemberLength as SQL text.
var maxDecimalMember = strconv.Itoa(decimals.MaxMemberLength)

// pgPathLiteral is the text[] literal of a path's segments; segments are
// identifiers, so no element needs quoting.
func pgPathLiteral(segs []string) string {
	return "'{" + strings.Join(segs, ",") + "}'"
}

// sqlitePathLiteral is the JSON path of a path's segments, every label
// quoted so a hyphen is a name character.
func sqlitePathLiteral(segs []string) string {
	var b strings.Builder
	b.WriteString("'$")
	for _, s := range segs {
		b.WriteString(`."`)
		b.WriteString(s)
		b.WriteString(`"`)
	}
	b.WriteString("'")
	return b.String()
}

// orderedAsText reports whether ordering comparisons on f compare text and so
// need code-point collation: strings, and a path's timestamp and UUID members,
// which compare as canonical text.
func orderedAsText(f *FieldSchema) bool {
	if f.DataType == core.FieldTypeString {
		return true
	}
	return f.Path != nil && (f.DataType == core.FieldTypeTimestamp || f.DataType == core.FieldTypeUUID)
}

// pgPathDocument is p's document as jsonb.
func pgPathDocument(p *JSONPath) string {
	if p.Storage != StorageJSONB {
		return "(" + p.Column + ")::jsonb"
	}
	return p.Column
}

// pgTypedMember is a JSON value of type t, NULL when it has another kind:
// kind is its jsonb_typeof, text its text (#>>).
func pgTypedMember(t core.FieldType, kind, text string) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'string' THEN " + text + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric END)"
	case core.FieldTypeDecimal:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric WHEN " + kind + " = 'string' AND length" + text + " <= " + maxDecimalMember +
			" AND " + text + " ~ '" + decimals.PlainRegex + "' THEN " + text + "::numeric END)"
	case core.FieldTypeBool:
		return "(CASE WHEN " + kind + " = 'boolean' THEN " + text + "::boolean END)"
	case core.FieldTypeTimestamp:
		// Pad the fraction to nine digits: the canonical text request values carry.
		return "(CASE WHEN " + kind + " = 'string' AND " + text + " ~ '" + timestampRegex + "' THEN substr(" + text + ", 1, 19) || '.' || rpad(coalesce(substring(" + text + " from '[.]([0-9]+)Z$'), ''), 9, '0') || 'Z' END)"
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'string' AND lower" + text + " ~ '" + uuidRegex + "' THEN lower" + text + " END)"
	}
	return "NULL"
}

// sqliteTimestampGlob and sqliteUUIDGlob are the canonical shapes as GLOB
// patterns; SQLite has no built-in regular expressions.
var (
	sqliteTimestampGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z"
	sqliteUUIDGlob      = hexGlob(8) + "-" + hexGlob(4) + "-" + hexGlob(4) + "-" + hexGlob(4) + "-" + hexGlob(12)
)

// sqliteTimestamp is the canonical text of a UTC RFC 3339 member val, or
// NULL: the 19-character date-time, then "Z" alone (length 20) or a "."
// and one to nine digits, the fraction padded to nine.
func sqliteTimestamp(kind, val string) string {
	frac := "substr(" + val + ", 21, length(" + val + ") - 21)"
	return "(CASE WHEN " + kind + " = 'text' AND " + val + " GLOB '" + sqliteTimestampGlob + "' AND (length(" + val + ") = 20 OR (substr(" + val + ", 20, 1) = '.' AND length(" + val + ") BETWEEN 22 AND 30 AND " + frac + " NOT GLOB '*[^0-9]*'))" +
		" THEN substr(" + val + ", 1, 19) || '.' || substr((CASE WHEN length(" + val + ") = 20 THEN '' ELSE " + frac + " END) || '000000000', 1, 9) || 'Z' END)"
}

func hexGlob(n int) string { return strings.Repeat("[0-9a-f]", n) }

// sqlitePathMember is p's member as SQLite reads it: its json_type, its
// json_extract value and its JSON text as written (`->`). An element path's
// are its json_each row's type and value, and the source document's text at
// the row's fullkey.
func sqlitePathMember(p *JSONPath) (kind, val, literal string) {
	if p.element {
		row := p.Column
		return row + ".type", row + ".value", row + ".json -> " + row + ".fullkey"
	}
	path := sqlitePathLiteral(p.Segments)
	return "json_type(" + p.Column + ", " + path + ")", "json_extract(" + p.Column + ", " + path + ")", p.Column + " -> " + path
}

// sqliteTypedMember is a JSON value of type t, NULL when it has another
// kind: kind is its json_type, val its json_extract and literal its JSON
// text as written (`->`). A date is an element of SQLite's JSON text array
// column, read as ReadColumn reads a date column; a time, interval or
// network element is the canonical text the column was written with.
func sqliteTypedMember(t core.FieldType, kind, val, literal string) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + val + " END)"
	case core.FieldTypeDecimal:
		return decimals.SQLiteValueFunc + "(" + literal + ")"
	case core.FieldTypeDate:
		return "(CASE WHEN " + kind + " = 'text' THEN substr(" + val + ", 1, 10) END)"
	case core.FieldTypeTime, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeBool:
		return "(CASE " + kind + " WHEN 'true' THEN 1 WHEN 'false' THEN 0 END)"
	case core.FieldTypeTimestamp:
		return sqliteTimestamp(kind, val)
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'text' AND lower(" + val + ") GLOB '" + sqliteUUIDGlob + "' THEN lower(" + val + ") END)"
	}
	return "NULL"
}
